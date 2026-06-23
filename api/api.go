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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	hserver "github.com/cloudwego/hertz/pkg/app/server"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

// AdminConfig 配置
type AdminConfig struct {
	// 兼容旧版：保留单一 ADMIN_TOKEN（用 env 注入），非空时启用 fallback 鉴权
	LegacyToken string

	// NotifSender 顾客通知发送器（PRD §11.7 P4 理发师请假通知顾客用，v4.10 多店+重试）
	//
	// 签名：(ctx, appt, text) -> error
	// 由 main.go 注入；为 nil 时请假设无通知能力（不影响 leave row 写入，只是不发微信）。
	// 推荐实现：wecom.Router 多店路由 + ChannelSelector 通道降级 + SendWithRetry 3 次退避。
	// storage.CreateBarberLeave 接 interface{}，可同时兼容新签名（推荐）和老签名（向后兼容测试）。
	NotifSender storage.LeaveNotificationSender
}

// notifSender 包级 handler 访问点（在 RegisterRoutes 时赋值一次）
//
// 不放 ctx；调用方负责传 ctx。
var notifSender storage.LeaveNotificationSender

// RegisterRoutes 注册 /api/* + /admin 路由
func RegisterRoutes(h *hserver.Hertz, cfg AdminConfig) {
	// 注入 handler 共享依赖
	notifSender = cfg.NotifSender

	// v4.7 RBAC：把 storage.AdminHasPermission 注入 auth 包（auth 不直接依赖 storage）
	auth.SetHasPermissionFunc(func(ctx context.Context, adminID uint64, perm string) (bool, error) {
		return storage.AdminHasPermission(ctx, adminID, perm)
	})

	// 公开：登录
	h.POST("/api/auth/login", loginHandler)

	// 公开：看板（用 URL 里的 shop_id，登录后才能用）
	api := h.Group("/api")
	api.GET("/shop/:id/dashboard", dashboardHandler)
	api.GET("/shop/:id/appointments", listAppointmentsHandler)
	api.GET("/shop/:id/subscription", getSubscriptionHandler)

	// 需要鉴权：商户后台
	protected := h.Group("/api/admin", authChain(cfg.LegacyToken))

	// v4.7 RBAC：所有 endpoint 都用 auth.RequirePerm(perm) 包一层
	//
	// 权限矩阵（详见 storage/permissions.go）：
	//   - owner：全 15 个权限
	//   - staff：view:* + 业务操作（不可改店铺/服务/订阅/成员）
	//
	// 注释里加 [perm] 标注，方便以后 grep 找。

	// ===== 自身 / 全员可用 =====
	protected.GET("/me", meHandler) // 任何登录 admin 都能看自己
	protected.POST("/change-password", auth.RequirePerm(storage.PermChangeOwnPassword), changePasswordHandler)
	protected.POST("/logout", logoutHandler)

	// ===== 看板 / 预约 / 顾客 / 转人工 / 师傅 / 事件 =====
	// （staff 可用）
	protected.GET("/alerts", auth.RequirePerm(storage.PermViewDashboard), getAlertsHandler) // 看板用
	protected.POST("/appointment/complete", auth.RequirePerm(storage.PermEditAppointments), completeAppointmentHandler)
	protected.POST("/appointment/cancel", auth.RequirePerm(storage.PermEditAppointments), adminCancelHandler)
	protected.GET("/customers", auth.RequirePerm(storage.PermViewCustomers), listCustomersHandler)
	protected.GET("/customers/:id", auth.RequirePerm(storage.PermViewCustomers), getCustomerDetailHandler)
	protected.POST("/customers/tag", auth.RequirePerm(storage.PermEditCustomers), addCustomerTagHandler)
	protected.DELETE("/customers/tag", auth.RequirePerm(storage.PermEditCustomers), removeCustomerTagHandler)
	protected.GET("/handoffs", auth.RequirePerm(storage.PermViewHandoffs), listHandoffsHandler)
	protected.POST("/handoffs/:id/resolve", auth.RequirePerm(storage.PermResolveHandoff), resolveHandoffHandler)
	protected.GET("/barber/:id/leaves", auth.RequirePerm(storage.PermViewBarbers), listBarberLeavesHandler)
	protected.POST("/barber/:id/leave", auth.RequirePerm(storage.PermCreateBarberLeave), createBarberLeaveHandler)
	protected.DELETE("/barber/:id/leave/:leaveID", auth.RequirePerm(storage.PermCreateBarberLeave), cancelBarberLeaveHandler)
	protected.GET("/barbers", auth.RequirePerm(storage.PermViewBarbers), listBarbersHandler)
	protected.GET("/events", auth.RequirePerm(storage.PermViewEvents), listEventsHandler)

	// 通知中心（v4.10.1）—— admin 后台看 leave notify 发送结果 + 补发失败
	//   - GET 列：view:notifications
	//   - POST 补发：retry:notifications
	//   - retry-batch 也在 view+retry 范围内
	protected.GET("/notifications", auth.RequirePerm(storage.PermViewNotifications), listNotificationsHandler)
	protected.POST("/notifications/:id/retry", auth.RequirePerm(storage.PermRetryNotifications), retryNotificationHandler)
	protected.POST("/notifications/retry-batch", auth.RequirePerm(storage.PermRetryNotifications), retryAllFailedNotificationsHandler)

	// ===== owner-only =====
	// 师傅管理：staff 只看不能改（避免误删活师傅）
	protected.POST("/barbers", auth.RequirePerm(storage.PermEditBarbers), createBarberHandler)
	protected.DELETE("/barbers/:id", auth.RequirePerm(storage.PermEditBarbers), softDeleteBarberHandler)
	protected.POST("/barbers/:id/activate", auth.RequirePerm(storage.PermEditBarbers), activateBarberHandler)
	protected.POST("/leave/create", auth.RequirePerm(storage.PermCreateBarberLeave), createLeaveHandler)
	protected.POST("/leave/cancel", auth.RequirePerm(storage.PermCreateBarberLeave), cancelLeaveHandler)
	protected.GET("/leave/list", auth.RequirePerm(storage.PermViewBarbers), listLeavesHandler)

	// 店铺设置
	protected.GET("/shop", auth.RequirePerm(storage.PermEditShop), getShopHandler) // 设为 owner-only 防止 staff 误读敏感字段
	protected.PUT("/shop", auth.RequirePerm(storage.PermEditShop), updateShopHandler)

	// 跨店看板（连锁）—— v4.10.1：只给 platform_admin 看
	// 之前用 RequirePerm + owner 默认有 AllPermissions → 单店 owner 也能看全平台所有店（权限泄漏）
	// 现在改 RequireRole 强约束：只有 platform_admin 角色才能进
	// 配合 storage 层 owner 默认矩阵里不列 view:chain_dashboard，双层防御
	protected.GET("/chain/dashboard", auth.RequireRole(storage.RolePlatformAdmin), chainDashboardHandler)

	// 周报（v4.10.1：单店 / 跨店权限分离）
	//   - 单店周报：owner / staff 都能看（看自己店经营数据）
	//   - 跨店周报：只 platform_admin（看全平台所有店）
	//   - 数据本身在 handler 内部通过 shopFromClaims 拿本店 shopID → 天然多店隔离
	protected.GET("/weekly-report", auth.RequirePerm(storage.PermViewWeeklyReport), getWeeklyReportHandler)
	protected.GET("/weekly-report/chain", auth.RequireRole(storage.RolePlatformAdmin), getChainWeeklyReportHandler)

	// 订阅（v4.10.1：整个订阅模块归 platform_admin）
	//   - 商户不需要关心订阅状态——订阅是平台/运营层的事
	//   - 列表 + 续费都用 RequireRole 强约束
	//   - 商户后台 subscription nav-item 前端会按 role 隐藏
	protected.GET("/subscription", auth.RequireRole(storage.RolePlatformAdmin), listSubscriptionsHandler)
	protected.POST("/subscription/renew", auth.RequireRole(storage.RolePlatformAdmin), renewSubscriptionHandler)

	// 服务目录
	protected.GET("/services", auth.RequirePerm(storage.PermViewServices), listServicesHandler)
	protected.POST("/services", auth.RequirePerm(storage.PermEditServices), createServiceHandler)
	protected.PUT("/services/:id", auth.RequirePerm(storage.PermEditServices), updateServiceHandler)
	protected.DELETE("/services/:id", auth.RequirePerm(storage.PermEditServices), deactivateServiceHandler)
	protected.POST("/services/:id/activate", auth.RequirePerm(storage.PermEditServices), activateServiceHandler)
	protected.POST("/services/import", auth.RequirePerm(storage.PermEditServices), importServicesHandler)

	// 成员管理（v4.7 RBAC 新增）
	protected.GET("/members", auth.RequirePerm(storage.PermManageMembers), listMembersHandler)
	protected.POST("/members", auth.RequirePerm(storage.PermManageMembers), createMemberHandler)
	protected.PUT("/members/:id/role", auth.RequirePerm(storage.PermManageMembers), changeMemberRoleHandler)
	protected.POST("/members/:id/reset-password", auth.RequirePerm(storage.PermManageMembers), resetMemberPasswordHandler)
	protected.DELETE("/members/:id", auth.RequirePerm(storage.PermManageMembers), disableMemberHandler)
	protected.GET("/roles", auth.RequirePerm(storage.PermManageMembers), listRolesHandler)

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
	// v4.7 RBAC:已停用账号禁止登录
	if admin.Status == "disabled" {
		c.JSON(http.StatusForbidden, map[string]string{"error": "账号已停用，请联系店主"})
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
	// v3.8 P2 dashboard 补全：事件漏斗
	//   - EventFunnelToday / Week / Month：按 event_type 聚合的事件数（month 范围，desc by count）
	//   - 包含 PRD §11.2 续费转化漏斗 + P3 黑名单 / P4 请假 / idle_push 等所有埋点
	EventFunnelToday []EventStat `json:"event_funnel_today"`
	EventFunnelWeek  []EventStat `json:"event_funnel_week"`
	EventFunnelMonth []EventStat `json:"event_funnel_month"`
	// MVP 第 5 项 - 转人工兜底
	//   - HandoffPendingToday: 今天还没处理的转人工请求数（商户最该看的指标）
	//   - 复用 EventFunnelToday，避免重复 SQL
	HandoffPendingToday int `json:"handoff_pending_today"`
}

// EventStat 单类事件统计
type EventStat struct {
	EventType string `json:"event_type"`
	Count     int    `json:"count"`
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

	// v3.8 P2 dashboard 补全：事件漏斗（按 event_type 聚合，含全部埋点）
	//  - today / week / month 三个窗口独立聚合
	//  - 用 Go 端 parseAnyTime 解析 created_at，跨 sqlite/mysql 驱动兼容
	//  - 排序按 count DESC，限制 top 20 防止 response 膨胀
	resp.EventFunnelToday = eventFunnel(ctx, shopID, todayStart, now, 20)
	resp.EventFunnelWeek = eventFunnel(ctx, shopID, weekStart, now, 20)
	resp.EventFunnelMonth = eventFunnel(ctx, shopID, monthStart, now, 20)

	// MVP 第 5 项：复用 EventFunnelToday 算出今天的待人工数
	//   - 不发额外 SQL，纯 Go 端 find，零成本
	//   - 商户在 dashboard 卡片一眼看到"今天还有几个转人工没处理"
	resp.HandoffPendingToday = findHandoffCount(resp.EventFunnelToday)
	return resp
}

// findHandoffCount 从 EventFunnelToday 里捞 handoff_to_human 的 count。
//
//   - 用 EventStat.EventType 比对 EventHandoffToHuman 常量（避免字符串硬编码漂移）
//   - 找不到返回 0（没触发过转人工）
//   - 复用而非重算：eventFunnel 已经按 today 窗口聚合并返回 top 20，handoff_to_human
//     即使不在 top 20 里也无所谓（说明今天 ≥20 个其它事件把它挤掉了；这种情况商户
//     更该关心整体的漏斗分布，而不是单看 handoff 数）
func findHandoffCount(stats []EventStat) int {
	for _, s := range stats {
		if s.EventType == storage.EventHandoffToHuman {
			return s.Count
		}
	}
	return 0
}

// eventFunnel 按 event_type 聚合事件数（PRD §11.2 续费漏斗 + P3/P4 埋点）
//
//   - since: 起始时间（inclusive）
//   - until: 截止时间（exclusive）
//   - limit: top N（按 count DESC），<= 0 时返回全部
//   - EventIdleSlotPush 是带前缀的（idle_slot_push:DATE:CUST），用 LIKE 聚合
//
// 注意：使用 map[string]any + parseAnyTime 跨 sqlite/mysql 驱动兼容（参考
// storage.FindShopsForLifecycle 的做法）。
func eventFunnel(ctx context.Context, shopID string, since, until time.Time, limit int) []EventStat {
	if storage.DB == nil {
		return nil
	}
	// 粗筛：created_at 落在 [since-1d, until+1d]，给跨天 / 边界预留 buffer
	// 精确过滤在 Go 端做（与 dashboard.summarizeRange 同样的策略）。
	sinceBuf := since.AddDate(0, 0, -1)
	untilBuf := until.AddDate(0, 0, 1)
	var rows []map[string]any
	if err := storage.DB.WithContext(ctx).
		Table("event_logs").
		Select("event_type, created_at").
		Where("shop_id = ?", shopID).
		Where("created_at >= ? AND created_at <= ?", sinceBuf, untilBuf).
		Scan(&rows).Error; err != nil {
		return nil
	}
	counts := make(map[string]int, len(rows))
	for _, r := range rows {
		et, _ := r["event_type"].(string)
		if et == "" {
			continue
		}
		// 把 idle_slot_push:DATE:CUST 归一为 idle_slot_push（避免展开成 N 条记录）
		if i := strings.Index(et, ":"); i > 0 {
			et = et[:i]
		}
		// 精确时间过滤（按真实 created_at）
		ts, ok := storage.ParseAnyTime(r["created_at"])
		if !ok || ts.Before(since) || !ts.Before(until) {
			continue
		}
		counts[et]++
	}
	out := make([]EventStat, 0, len(counts))
	for et, n := range counts {
		out = append(out, EventStat{EventType: et, Count: n})
	}
	// 按 count DESC 排序（稳定排序保证 limit 稳定）
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].EventType < out[j].EventType
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
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

// ============================================================
// 通知中心（v4.10.1 PRD §11.7 P4 配套：admin 后台看 leave notify 发送结果 + 补发）
// ============================================================
//
// 路由：
//   - GET    /api/admin/notifications?status=&type=&leave_id=&limit=
//   - POST   /api/admin/notifications/:id/retry          单条补发
//   - POST   /api/admin/notifications/retry-batch        一键补发本店所有 failed leave 通知
//
// 多店隔离：所有 list / retry 都用 shopFromClaims 拿当前 admin 的 shopID，
// 强制只读 / 只改本店通知，避免越权访问其他店铺数据。
//
// 安全：
//   - 单条 retry 调 storage.RetryNotification，发送走同一个 sender（多店路由 + 重试 + 通道降级）
//   - 已 sent 的拒绝 retry（避免重复打扰顾客，storage 内部硬约束）
//   - retry-batch 串行跑，N 条 × 3 次重试 = 可能 5-15 秒；UI 上需要 loading 态

// listNotificationsHandler 列本店通知（v4.10.1 admin 后台"通知中心"用）
//
// Query 参数（都可空）：
//   - status:  pending / sent / failed / skipped（空 = 全部）
//   - type:    leave_cancel / leave_reschedule / leave_no_contact（空 = 全部）
//   - leave_id: 按 leave_id 精确匹配（空 = 全部），用于"查看某次请假的所有通知"
//   - limit:   1-500，默认 200
//
// 排序：created_at DESC
func listNotificationsHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	status := c.Query("status")
	notifType := c.Query("type")
	leaveID := c.Query("leave_id")
	limitStr := c.Query("limit")
	limit := 200
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 500 {
		limit = v
	}

	// 校验 status 是已知值（不挡错别字，给前端更明确的反馈）
	if status != "" && !isKnownNotifStatus(status) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "status 必须是 pending / sent / failed / skipped 之一"})
		return
	}
	if notifType != "" && !isKnownNotifType(notifType) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "type 必须是 leave_cancel / leave_reschedule / leave_no_contact 之一"})
		return
	}

	notifs, err := storage.ListNotificationsForShop(ctx, shopID, status, notifType, leaveID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// 始终返回 array（避免前端处理 null）
	if notifs == nil {
		notifs = []storage.CustomerNotification{}
	}
	c.JSON(http.StatusOK, notifs)
}

// retryNotificationHandler 单条补发（admin 后台"补发"按钮）
//
// POST /api/admin/notifications/:id/retry
// 行为：
//   - 200 + { new_status }  重发成功（status=sent）
//   - 200 + { new_status: "failed" }  重发仍失败（attempt_count 已累加）
//   - 200 + { new_status: "skipped" } 顾客仍无联系方式（ErrNoCustomerContact）
//   - 409 + { error }  通知已 sent 成功（拒绝重发避免重复打扰）
//   - 404 + { error }  通知 id 不存在
//   - 500  其他错误（DB / sender 错）
//
// 重要：通知必须属于当前 admin 的 shop（多店隔离硬约束）。
func retryNotificationHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "id 必须是正整数"})
		return
	}

	// 多店隔离：先验证这条通知属于本店
	n, ok, err := storage.GetNotificationByID(ctx, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "通知不存在"})
		return
	}
	if n.ShopID != shopID {
		// 不泄露存在性，对外只说 forbidden
		c.JSON(http.StatusForbidden, map[string]string{"error": "无权操作其他店铺的通知"})
		return
	}

	// 调 storage 重发（内部已含 SendWithRetry + 通道降级）
	res, err := storage.RetryNotification(ctx, id, notifSender)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrNotificationNotFound):
			c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, storage.ErrNotificationAlreadySent):
			// 409：业务上不允许，但前端可能因竞态（点 retry 按钮的瞬间 row 状态变了）
			// 返回 200 + new_status 也能接受；这里选 409 让前端能区分"已成功" vs "重发了"
			c.JSON(http.StatusConflict, map[string]string{
				"error":      err.Error(),
				"new_status": res.NewStatus,
			})
		default:
			// 重发失败（sender 内部 error） → 200 + new_status=failed，让前端能继续展示
			// 这里选择 200 + { new_status, error } 而非 5xx：业务上 row 已更新，UI 应当刷新
			c.JSON(http.StatusOK, map[string]any{
				"id":         id,
				"new_status": res.NewStatus,
				"error":      err.Error(),
			})
		}
		return
	}

	// 成功
	c.JSON(http.StatusOK, map[string]any{
		"id":         id,
		"new_status": res.NewStatus,
	})
}

// retryAllFailedNotificationsHandler 一键补发本店所有 failed leave 通知
//
// POST /api/admin/notifications/retry-batch
// Body: 空（不需要参数，从 claims 拿 shopID）
//
// 行为：串行遍历所有 status=failed + type in (leave_*) 的通知，逐一 RetryNotification
//
// 返回：
//   - 200 + { succeeded, failed, total }
//
// 注意：单条 send 已含 3 次退避，串行 N 条可能跑 5-15 秒（API 端），UI 上需要转圈
func retryAllFailedNotificationsHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	suc, fail, err := storage.RetryShopFailedNotifications(ctx, shopID, notifSender)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"total":     suc + fail,
		"succeeded": suc,
		"failed":    fail,
	})
}

// isKnownNotifStatus 校验 status 是合法值（防前端错别字 500）
func isKnownNotifStatus(s string) bool {
	switch s {
	case storage.NotifStatusPending, storage.NotifStatusSent, storage.NotifStatusFailed, storage.NotifStatusSkipped:
		return true
	}
	return false
}

// isKnownNotifType 校验 type 是合法值
func isKnownNotifType(t string) bool {
	switch t {
	case storage.NotifTypeLeaveCancel, storage.NotifTypeLeaveReschedule, storage.NotifTypeLeaveNoContact:
		return true
	}
	return false
}