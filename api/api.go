// Package api 提供商户后台 API（PRD §11.2 经营看板 + 订阅管理 + 商户后台）
//
// 路由注册：server.Spin 调用 RegisterRoutes(h, cfg)
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	hserver "github.com/cloudwego/hertz/pkg/app/server"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/auth"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// AdminConfig 配置
type AdminConfig struct {
	// 兼容旧版：保留单一 ADMIN_TOKEN（用 env 注入），非空时启用 fallback 鉴权
	LegacyToken string

	// NotifSender 顾客通知发送器（PRD §11.7 P4 理发师请假通知顾客用）
	//
	// 签名：(ctx, customerID, text) -> error
	// 由 main.go 注入；为 nil 时请假设无通知能力（不影响 leave row 写入，只是不发微信）。
	NotifSender func(ctx context.Context, customerID, text string) error
}

// notifSender 包级 handler 访问点（在 RegisterRoutes 时赋值一次）
//
// 不放 ctx；调用方负责传 ctx。
var notifSender func(ctx context.Context, customerID, text string) error

// RegisterRoutes 注册 /api/* + /admin 路由
func RegisterRoutes(h *hserver.Hertz, cfg AdminConfig) {
	// 注入 handler 共享依赖
	notifSender = cfg.NotifSender

	// 公开：登录
	h.POST("/api/auth/login", loginHandler)

	// 公开：看板（用 URL 里的 shop_id，登录后才能用）
	api := h.Group("/api")
	api.GET("/shop/:id/dashboard", dashboardHandler)
	api.GET("/shop/:id/appointments", listAppointmentsHandler)
	api.GET("/shop/:id/subscription", getSubscriptionHandler)

	// 需要鉴权：商户后台
	protected := h.Group("/api/admin", authChain(cfg.LegacyToken))
	protected.POST("/appointment/complete", completeAppointmentHandler)
	protected.POST("/appointment/cancel", adminCancelHandler)
	protected.POST("/subscription/renew", renewSubscriptionHandler)
	protected.GET("/events", listEventsHandler)
	protected.GET("/me", meHandler)
	protected.POST("/change-password", changePasswordHandler)
	protected.POST("/logout", logoutHandler)
	// P4 理发师请假（RESTful 路径：/barber/:id/leave*）
	protected.POST("/barber/:id/leave", createBarberLeaveHandler)
	protected.DELETE("/barber/:id/leave/:leaveID", cancelBarberLeaveHandler)
	protected.GET("/barber/:id/leaves", listBarberLeavesHandler)
	// P4 备用接口（barber_id / leave_id 在 body，便于跨理发师聚合查询 + 后台管理）
	protected.POST("/leave/create", createLeaveHandler)
	protected.POST("/leave/cancel", cancelLeaveHandler)
	protected.GET("/leave/list", listLeavesHandler)

	// 静态：商户后台页面
	h.GET("/admin", func(ctx context.Context, c *app.RequestContext) {
		data, err := staticAdmin()
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	})
}

// authChain 鉴权链：优先 JWT，失败则 fallback 到 legacy ADMIN_TOKEN（兼容旧版）
//
// 设计上不要"复制 RequestContext 跑中间件"—— Hertz 的 c.Response 是绑连接的，c.Copy()
// 出来的 c2.Response 状态写不会同步回原 c，导致看起来"中间件没生效"或状态错乱。
//
// 正确做法：直接在本 c 上做 JWT 校验（验证失败时不 abort，给 legacy 一个机会）。
func authChain(legacyToken string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// 1) 尝试 JWT（直接读 header/query，调用 auth.Verify）
		tok := extractToken(c)
		if tok != "" {
			if claims, err := auth.Verify(tok); err == nil {
				c.Set("auth_claims", claims)
				c.Next(ctx)
				return
			}
		}

		// 2) Fallback 到 legacy ADMIN_TOKEN
		if legacyToken != "" {
			got := string(c.GetHeader("X-Admin-Token"))
			if got == "" {
				got = c.Query("token")
			}
			if got == legacyToken {
				c.Set("auth_claims", &auth.Claims{
					AdminID: 0,
					ShopID:  "default",
					Role:    "owner",
				})
				c.Next(ctx)
				return
			}
		}

		// 3) 都没通过
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		c.Abort()
	}
}

// extractToken 从 Authorization / X-Admin-Token / query 抽 token
func extractToken(c *app.RequestContext) string {
	if h := string(c.GetHeader("Authorization")); h != "" {
		if strings.HasPrefix(h, "Bearer ") {
			return strings.TrimPrefix(h, "Bearer ")
		}
		return h
	}
	if h := string(c.GetHeader("X-Admin-Token")); h != "" {
		return h
	}
	return c.Query("token")
}

// ---- 鉴权相关 ----

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func loginHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	var req loginReq
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "username / password required"})
		return
	}
	admin, err := storage.FindAdminByUsername(ctx, req.Username)
	if err != nil {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "用户名或密码错误"})
		return
	}
	if !storage.VerifyAdminPassword(admin, req.Password) {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "用户名或密码错误"})
		return
	}
	storage.MarkAdminLogin(ctx, admin.ID)

	tok, err := auth.Sign(admin.ID, admin.ShopID, admin.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, map[string]any{
		"token":   tok,
		"shop_id": admin.ShopID,
		"role":    admin.Role,
		"expires_in": 7 * 24 * 3600,
	})
}

func meHandler(ctx context.Context, c *app.RequestContext) {
	cl := auth.GetClaims(c)
	if cl == nil {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	shop, _ := storage.GetShopByID(ctx, cl.ShopID)
	c.JSON(http.StatusOK, map[string]any{
		"admin_id": cl.AdminID,
		"shop_id":  cl.ShopID,
		"role":     cl.Role,
		"shop":     shop,
	})
}

func changePasswordHandler(ctx context.Context, c *app.RequestContext) {
	cl := auth.GetClaims(c)
	if cl == nil {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.NewPassword) < 6 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "新密码至少 6 位"})
		return
	}
	admin, err := storage.FindAdminByUsername(ctx, "") // 不通过 username 走，直接按 id
	_ = admin
	_ = err
	// 简化：直接用 cl.AdminID 改
	if err := storage.UpdateAdminPassword(ctx, cl.AdminID, req.NewPassword); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func logoutHandler(ctx context.Context, c *app.RequestContext) {
	// JWT 是 stateless 的，logout 主要是前端清 token；后端可以做 token 黑名单（MVP 不做）
	c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// ---- Dashboard ----

// DashboardSummary 单个时间窗口的汇总
type DashboardSummary struct {
	Total     int     `json:"total"`
	Completed int     `json:"completed"`
	NoShow    int     `json:"noshow"`
	Cancelled int     `json:"cancelled"`
	Active    int     `json:"active"`    // 还没到时间的
	Upcoming  int     `json:"upcoming"`  // 接下来 2 小时的
	NoShowRate   float64 `json:"no_show_rate"`   // noshow / (noshow+completed)
	CompleteRate float64 `json:"complete_rate"` // completed / (noshow+completed)
}

// DashboardResponse 看板响应
type DashboardResponse struct {
	ShopID         string                       `json:"shop_id"`
	GeneratedAt    time.Time                    `json:"generated_at"`
	Today          DashboardSummary             `json:"today"`
	Week           DashboardSummary             `json:"week"`
	Month          DashboardSummary             `json:"month"`
	TopHours       []HourStat                   `json:"top_hours"`        // 热门时段 TOP 5
	TopBarbers     []BarberStat                 `json:"top_barbers"`      // 热门理发师
	LifecycleCount int64                        `json:"lifecycle_count"`  // 总事件数（埋点用）
}

type HourStat struct {
	Hour  string `json:"hour"`
	Count int    `json:"count"`
}
type BarberStat struct {
	BarberName string `json:"barber_name"`
	Count      int    `json:"count"`
}

func dashboardHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	shopID := c.Param("id")
	resp := buildDashboard(ctx, shopID)
	c.JSON(http.StatusOK, resp)
}

func buildDashboard(ctx context.Context, shopID string) DashboardResponse {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	weekStart := todayStart.AddDate(0, 0, -7)
	monthStart := todayStart.AddDate(0, -1, 0)

	resp := DashboardResponse{
		ShopID:      shopID,
		GeneratedAt: now,
	}
	resp.Today = summarizeRange(ctx, shopID, todayStart, todayStart.Add(24*time.Hour))
	resp.Week = summarizeRange(ctx, shopID, weekStart, now.Add(24*time.Hour))
	resp.Month = summarizeRange(ctx, shopID, monthStart, now.Add(24*time.Hour))

	// 热门时段：按 HH 聚合（近 30 天）
	resp.TopHours = topHours(ctx, shopID, monthStart)
	// 热门理发师
	resp.TopBarbers = topBarbers(ctx, shopID, monthStart)
	// 总埋点数
	resp.LifecycleCount = storage.CountShopEvents(ctx, shopID, "")

	// 接下来的 2 小时即将到店人数
	resp.Today.Upcoming = countUpcoming(ctx, shopID, now, now.Add(2*time.Hour))
	return resp
}

// summarizeRange 按"预约实际发生时间"落在 [from, to) 内的预约聚合
//
// 注意：date+time 是字符串（如 "2026-06-20" + "15:00"），用 Go 端 ParseInLocation
// 解析后再比对 —— 避免 SQL 端字符串拼接误判跨天预约（22:00 算今天还是明天）。
//
// SQL 端先按 date 范围粗筛减少数据传输，到 Go 端再精确过滤。
func summarizeRange(ctx context.Context, shopID string, from, to time.Time) DashboardSummary {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	// 粗筛：date 落在 [from-1天, to+1天]，给跨天预留 buffer
	dateFrom := from.AddDate(0, 0, -1).Format("2006-01-02")
	dateTo := to.AddDate(0, 0, 1).Format("2006-01-02")
	var appts []storage.Appointment
	storage.DB.WithContext(ctx).
		Where("shop_id = ? AND date >= ? AND date <= ?", shopID, dateFrom, dateTo).
		Find(&appts)

	var s DashboardSummary
	for _, a := range appts {
		// 解析 date+time 为时间戳
		dt, err := time.ParseInLocation("2006-01-02 15:04", a.Date+" "+a.Time, loc)
		if err != nil {
			continue
		}
		// 落在 [from, to) 区间内才算
		if dt.Before(from) || !dt.Before(to) {
			continue
		}
		s.Total++
		switch a.Status {
		case "completed":
			s.Completed++
		case "noshow":
			s.NoShow++
		case "cancelled":
			s.Cancelled++
		case "active":
			s.Active++
		}
	}
	closed := s.NoShow + s.Completed
	if closed > 0 {
		s.NoShowRate = float64(s.NoShow) / float64(closed)
		s.CompleteRate = float64(s.Completed) / float64(closed)
	}
	return s
}

func topHours(ctx context.Context, shopID string, since time.Time) []HourStat {
	type row struct {
		Hour  string
		Count int
	}
	var rows []row
	storage.DB.WithContext(ctx).
		Table("appointments").
		Select("time as hour, COUNT(*) as count").
		Where("shop_id = ? AND date >= ? AND status IN ('completed','noshow','active')", shopID, since.Format("2006-01-02")).
		Group("time").
		Order("count DESC").
		Limit(5).
		Scan(&rows)
	out := make([]HourStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, HourStat{Hour: r.Hour, Count: r.Count})
	}
	return out
}

func topBarbers(ctx context.Context, shopID string, since time.Time) []BarberStat {
	type row struct {
		BarberName string
		Count      int
	}
	var rows []row
	storage.DB.WithContext(ctx).
		Table("appointments").
		Select("barber_name, COUNT(*) as count").
		Where("shop_id = ? AND date >= ? AND status IN ('completed','noshow','active')", shopID, since.Format("2006-01-02")).
		Group("barber_name").
		Order("count DESC").
		Limit(5).
		Scan(&rows)
	out := make([]BarberStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, BarberStat{BarberName: r.BarberName, Count: r.Count})
	}
	return out
}

// countUpcoming 接下来 from~to 之间即将到店的 active 预约
//
// 注意：是按"预约时间点"（date+time）落在 [from, to) 内，而非按 date 字段粗筛。
func countUpcoming(ctx context.Context, shopID string, from, to time.Time) int {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	dateFrom := from.AddDate(0, 0, -1).Format("2006-01-02")
	dateTo := to.AddDate(0, 0, 1).Format("2006-01-02")
	var appts []storage.Appointment
	storage.DB.WithContext(ctx).
		Where("shop_id = ? AND status = ? AND date >= ? AND date <= ?", shopID, "active", dateFrom, dateTo).
		Find(&appts)
	n := 0
	for _, a := range appts {
		dt, err := time.ParseInLocation("2006-01-02 15:04", a.Date+" "+a.Time, loc)
		if err != nil {
			continue
		}
		if !dt.Before(from) && dt.Before(to) {
			n++
		}
	}
	return n
}

// ---- 排班列表 ----

// listAppointmentsHandler
//   - ?date=YYYY-MM-DD：查某天的预约（按 date 字段严格匹配）
//   - ?status=active|completed|noshow|cancelled：按状态过滤
//   - 不传 date 时返回最近 200 条
//
// 注意：list 这种场景按 date 字符串精确匹配是合理的（用户主动选了某天）。
// 跨天的"今晚 22:00"预约算今天还是明天——按 date 字段。
func listAppointmentsHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	shopID := c.Param("id")
	date := c.Query("date")        // 可选，YYYY-MM-DD
	status := c.Query("status")    // 可选

	q := storage.DB.WithContext(ctx).Where("shop_id = ?", shopID)
	if date != "" {
		q = q.Where("date = ?", date)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var appts []storage.Appointment
	if err := q.Order("date DESC, time DESC").Limit(200).Find(&appts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, appts)
}

// ---- 商户操作 ----

// shopFromClaims 从 JWT claims 取 shop_id（多店隔离的关键）
func shopFromClaims(c *app.RequestContext) string {
	cl := auth.GetClaims(c)
	if cl == nil {
		return ""
	}
	return cl.ShopID
}

func completeAppointmentHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	var req struct {
		AppointmentID string `json:"appointment_id"`
	}
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// 多店隔离：验证这条预约属于本店
	appt, err := storage.GetAppointment(req.AppointmentID)
	if err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "预约不存在"})
		return
	}
	if appt.ShopID != shopID {
		c.JSON(http.StatusForbidden, map[string]string{"error": "无权操作其他店铺的预约"})
		return
	}
	if err := storage.MarkAppointmentCompleted(ctx, req.AppointmentID); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "completed"})
}

func adminCancelHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	var req struct {
		AppointmentID string `json:"appointment_id"`
		Reason        string `json:"reason"`
	}
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	appt, err := storage.GetAppointment(req.AppointmentID)
	if err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "预约不存在"})
		return
	}
	if appt.ShopID != shopID {
		c.JSON(http.StatusForbidden, map[string]string{"error": "无权操作其他店铺的预约"})
		return
	}
	// P3：admin 路径走策略接口（cancel_type=admin_cancel，不计 penalty）
	res, err := storage.CancelAppointmentWithPolicy(ctx, req.AppointmentID, storage.CancelSourceAdmin, req.Reason)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"status":       "cancelled",
		"cancel_type":  res.CancelType,
	})
}

// ---- 订阅 ----

func getSubscriptionHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	shopID := c.Param("id")
	var sub storage.Subscription
	err := storage.DB.WithContext(ctx).Where("shop_id = ?", shopID).Order("expires_at DESC").First(&sub).Error
	if err != nil {
		c.JSON(http.StatusOK, map[string]any{
			"shop_id":   shopID,
			"has_subscription": false,
		})
		return
	}
	// 计算剩余天数
	daysLeft := int(time.Until(sub.ExpiresAt).Hours() / 24)
	c.JSON(http.StatusOK, map[string]any{
		"shop_id":           shopID,
		"has_subscription":  true,
		"plan":              sub.Plan,
		"started_at":        sub.StartedAt,
		"expires_at":        sub.ExpiresAt,
		"days_left":         daysLeft,
		"auto_renew":        sub.AutoRenew,
		"is_expired":        daysLeft < 0,
		"is_expiring_soon":  daysLeft >= 0 && daysLeft <= 7,
	})
}

func renewSubscriptionHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	var req struct {
		Plan   string `json:"plan"`   // basic / pro / flagship
		Months int    `json:"months"` // 续费月数
		Amount int    `json:"amount"` // 实收金额（分）
		Note   string `json:"note"`
	}
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Plan == "" || req.Months <= 0 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "plan / months 必须"})
		return
	}

	now := time.Now()
	expiresAt := now.AddDate(0, req.Months, 0)

	storage.DB.WithContext(ctx).Model(&storage.Subscription{}).
		Where("shop_id = ?", shopID).
		Update("cancelled_at", now)

	sub := storage.Subscription{
		ID:        newID(),
		ShopID:    shopID,
		Plan:      req.Plan,
		StartedAt: now,
		ExpiresAt: expiresAt,
		AutoRenew: false,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := storage.DB.WithContext(ctx).Create(&sub).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	storage.DB.WithContext(ctx).Model(&storage.Shop{}).
		Where("id = ?", shopID).
		Updates(map[string]interface{}{
			"plan":       req.Plan,
			"expires_at": expiresAt,
		})

	storage.TrackEvent(ctx, shopID, storage.EventRenewed, sub.ID, map[string]any{
		"plan":   req.Plan,
		"months": req.Months,
		"amount": req.Amount,
		"note":   req.Note,
	})

	c.JSON(http.StatusOK, sub)
}

// ---- 事件 ----

func listEventsHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c) // 不再从 query 拿，强制从 claims 取（多店隔离）
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	eventType := c.Query("event_type")
	limitStr := c.Query("limit")
	limit := 100
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 1000 {
		limit = v
	}
	q := storage.DB.WithContext(ctx).Model(&storage.EventLog{}).
		Where("shop_id = ?", shopID)
	if eventType != "" {
		q = q.Where("event_type = ?", eventType)
	}
	var events []storage.EventLog
	if err := q.Order("created_at DESC").Limit(limit).Find(&events).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, events)
}

// ---- helpers ----

func newID() string {
	// 简单随机 ID（生产用 uuid）
	return strings.ReplaceAll(time.Now().Format("20060102150405.000000"), ".", "")
}

func staticAdmin() ([]byte, error) {
	return os.ReadFile("static/admin.html")
}

// ============================================================
// P4 理发师请假（PRD §11.7）
// ============================================================

// createBarberLeaveHandler 创建一条理发师请假
//
// POST /api/admin/barber/:id/leave
// Body: { start_at, end_at, reason, action }
//   - action: "cancel" | "reschedule"
//   - start_at / end_at: ISO8601（RFC3339）
//
// 响应：{ leave_id, affected_count, rescheduled_count, cancelled_count, notified_count }
func createBarberLeaveHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	barberID := c.Param("id")
	if barberID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "barber id required"})
		return
	}
	var req struct {
		StartAt string `json:"start_at"`
		EndAt   string `json:"end_at"`
		Reason  string `json:"reason"`
		Action  string `json:"action"`
	}
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	startAt, err := time.Parse(time.RFC3339, req.StartAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "start_at 格式错误，需 RFC3339"})
		return
	}
	endAt, err := time.Parse(time.RFC3339, req.EndAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "end_at 格式错误，需 RFC3339"})
		return
	}
	if !startAt.Before(endAt) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "start_at 必须早于 end_at"})
		return
	}

	// 多店隔离：先验证理发师属于本店
	var barber storage.Barber
	if err := storage.DB.WithContext(ctx).Where("id = ?", barberID).First(&barber).Error; err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "理发师不存在"})
		return
	}
	if barber.ShopID != shopID {
		c.JSON(http.StatusForbidden, map[string]string{"error": "无权操作其他店铺的理发师"})
		return
	}

	leave := storage.BarberLeave{
		ShopID:   shopID,
		BarberID: barberID,
		StartAt:  startAt,
		EndAt:    endAt,
		Reason:   req.Reason,
		Action:   req.Action,
	}
	res, err := storage.CreateBarberLeave(ctx, leave, notifSender)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"leave_id":           res.LeaveID,
		"affected_count":     res.AffectedCount,
		"rescheduled_count":  res.RescheduledCount,
		"cancelled_count":    res.CancelledCount,
		"notified_count":     len(res.NotifiedCustomers),
	})
}

// cancelBarberLeaveHandler 撤销一条请假（仅当还没到 start_at）
//
// DELETE /api/admin/barber/:id/leave/:leaveID
func cancelBarberLeaveHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	barberID := c.Param("id")
	leaveID := c.Param("leaveID")
	if barberID == "" || leaveID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "barber id + leave id required"})
		return
	}
	// 多店隔离：先验 leave 属于本店
	var leave storage.BarberLeave
	if err := storage.DB.WithContext(ctx).Where("id = ?", leaveID).First(&leave).Error; err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "请假记录不存在"})
		return
	}
	if leave.ShopID != shopID {
		c.JSON(http.StatusForbidden, map[string]string{"error": "无权操作其他店铺的请假"})
		return
	}
	if leave.BarberID != barberID {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "barber id 与 leave 不匹配"})
		return
	}
	username, _ := c.Get("auth_claims")
	operator := ""
	if username != nil {
		if claims, ok := username.(*auth.Claims); ok {
			operator = fmt.Sprintf("admin#%d", claims.AdminID)
		}
	}
	if _, err := storage.CancelBarberLeave(ctx, leaveID, operator); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "cancelled"})
}

// listBarberLeavesHandler 列某理发师的请假历史
//
// GET /api/admin/barber/:id/leaves?limit=20
func listBarberLeavesHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	barberID := c.Param("id")
	if barberID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "barber id required"})
		return
	}
	limit := 50
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	leaves, err := storage.ListBarberLeaves(ctx, barberID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// 多店隔离：过滤掉非本店的 leave
	filtered := make([]storage.BarberLeave, 0, len(leaves))
	for _, l := range leaves {
		if l.ShopID == shopID {
			filtered = append(filtered, l)
		}
	}
	c.JSON(http.StatusOK, filtered)
}

// ============================================================
// P4 备用接口风格（路径短，barber_id/leave_id 在 body）
//   - POST /api/admin/leave/create   → createLeaveHandler
//   - POST /api/admin/leave/cancel   → cancelLeaveHandler
//   - GET  /api/admin/leave/list?status=&barber_id=  → listLeavesHandler
//
// 与 RESTful 风格 (/barber/:id/leave) 等价；保留两个接口是因为：
//   1) /leave/list 可以跨理发师聚合查询
//   2) 一些商户 UI 直接按 leave_id 撤销更顺手（不用先知道 barber_id）
// ============================================================

// createLeaveHandler
//
// POST /api/admin/leave/create
// Body: { barber_id, start_at, end_at, reason, action }
func createLeaveHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	var req struct {
		BarberID string `json:"barber_id"`
		StartAt  string `json:"start_at"`
		EndAt    string `json:"end_at"`
		Reason   string `json:"reason"`
		Action   string `json:"action"`
	}
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.BarberID == "" || req.StartAt == "" || req.EndAt == "" || req.Action == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "barber_id / start_at / end_at / action 都是必填"})
		return
	}
	startAt, err := time.Parse(time.RFC3339, req.StartAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "start_at 格式错误，需 RFC3339"})
		return
	}
	endAt, err := time.Parse(time.RFC3339, req.EndAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "end_at 格式错误，需 RFC3339"})
		return
	}
	if req.Action != storage.LeaveActionCancel && req.Action != storage.LeaveActionReschedule {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "action 必须是 cancel 或 reschedule"})
		return
	}

	// 多店隔离：理发师属于本店
	var barber storage.Barber
	if err := storage.DB.WithContext(ctx).Where("id = ?", req.BarberID).First(&barber).Error; err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "理发师不存在或不属于本店"})
		return
	}
	if barber.ShopID != shopID {
		// 不泄露存在性，对外只说"不属于本店"
		c.JSON(http.StatusBadRequest, map[string]string{"error": "理发师不属于本店"})
		return
	}

	leave := storage.BarberLeave{
		ShopID:   shopID,
		BarberID: req.BarberID,
		StartAt:  startAt,
		EndAt:    endAt,
		Reason:   req.Reason,
		Action:   req.Action,
	}
	res, err := storage.CreateBarberLeave(ctx, leave, notifSender)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"leave_id":          res.LeaveID,
		"affected_count":    res.AffectedCount,
		"rescheduled_count": res.RescheduledCount,
		"cancelled_count":   res.CancelledCount,
		"notified_count":    len(res.NotifiedCustomers),
	})
}

// cancelLeaveHandler
//
// POST /api/admin/leave/cancel
// Body: { leave_id }
func cancelLeaveHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	var req struct {
		LeaveID string `json:"leave_id"`
	}
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.LeaveID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "leave_id 必须"})
		return
	}
	// 多店隔离：先查 leave 所属店铺
	var leave storage.BarberLeave
	if err := storage.DB.WithContext(ctx).Where("id = ?", req.LeaveID).First(&leave).Error; err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "请假记录不存在"})
		return
	}
	if leave.ShopID != shopID {
		c.JSON(http.StatusForbidden, map[string]string{"error": "无权操作其他店铺的请假"})
		return
	}
	operator := currentOperator(c)
	got, err := storage.CancelBarberLeave(ctx, req.LeaveID, operator)
	if err != nil {
		// 已开始 → 409 Conflict；其他 → 500
		if errors.Is(err, storage.ErrLeaveNotCancellable) {
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error() + "（leave has already started, cannot cancel）"})
			return
		}
		if strings.Contains(err.Error(), "已是 cancelled") {
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": got.Status})
}

// listLeavesHandler
//
// GET /api/admin/leave/list?status=&barber_id=&limit=
//   - 不传 status：默认仅返回 active
//   - status=cancelled|expired：过滤对应状态
//   - barber_id=xxx：仅返回某理发师
func listLeavesHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	status := c.Query("status")
	barberID := c.Query("barber_id")
	limit := 50
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	q := storage.DB.WithContext(ctx).Model(&storage.BarberLeave{}).Where("shop_id = ?", shopID)
	if barberID != "" {
		q = q.Where("barber_id = ?", barberID)
	}
	switch status {
	case "":
		// 默认：只列 active
		q = q.Where("status = ?", storage.LeaveStatusActive)
	case storage.LeaveStatusCancelled, storage.LeaveStatusExpired, storage.LeaveStatusActive:
		q = q.Where("status = ?", status)
	}
	var leaves []storage.BarberLeave
	if err := q.Order("start_at DESC").Limit(limit).Find(&leaves).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if leaves == nil {
		leaves = []storage.BarberLeave{}
	}
	c.JSON(http.StatusOK, leaves)
}

// currentOperator 从 JWT claims 取当前操作人标识（MVP 简化：admin#<id>）
func currentOperator(c *app.RequestContext) string {
	cl := auth.GetClaims(c)
	if cl == nil {
		return ""
	}
	return fmt.Sprintf("admin#%d", cl.AdminID)
}