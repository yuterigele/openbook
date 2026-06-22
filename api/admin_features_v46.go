package api

// admin_features_v46.go
//
// v4.6 增量 —— 补全 3 个长期标 ⏳ 的后台能力：
//
//   8) 顾客详情页      GET /api/admin/customers/:id
//                       返回顾客基础信息 + 标签 + 最近 20 条预约 + 爽约/晚退订趋势
//   9) 转人工已处理    POST /api/admin/handoffs/:id/resolve
//                       写一条 handoff_resolved 埋点，关联回原 handoff event log
//                       返回新 event_log.id（前端可关联 UI）
//  10) 服务批量导入    POST /api/admin/services/import
//                       Body: {"services":[{name, estimated_min, price_range?}, ...]}
//                       批量 upsert，逐行校验，单条失败不影响整体（返回 successes / errors）
//
// 设计原则：沿用 v4.4 的三层结构（admin_features.go），保持风格一致：
//   - shopID 一律从 JWT claims 取（多店隔离）
//   - 错误响应统一 map[string]string{"error": "..."}
//   - 入参用 BindAndValidate 自动 JSON 解析
//   - 状态码：400 输入、401 无 session、403 跨店、404 不存在、409 冲突、500 其它

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

// ============================================================
// 8) 顾客详情（v4.6 PRD §11.14.6 #2）
// ============================================================

// CustomerDetailResponse 顾客详情聚合（v4.6）
//
// 用途：商户后台点击顾客行 → 弹窗展示"该顾客的最近预约 + 标签 + 爽约趋势"
//   - profile   : 基础信息（name / phone / wechat_open_id / tags / 累计统计）
//   - tags      : 拆开的 []string（profile.tags 是逗号分隔，前端拿这个直接渲染 chip）
//   - recent    : 最近 20 条预约，按 date DESC, time DESC
//   - status_counts: 4 种状态在该店的累计（active/completed/cancelled/noshow）
//   - upcoming_count: 未来的 active 预约数（用于"该顾客还有几单没到"展示）
type CustomerDetailResponse struct {
	Profile        CustomerProfile        `json:"profile"`
	Tags           []string               `json:"tags"`
	Recent         []CustomerApptItem     `json:"recent"`
	StatusCounts   CustomerStatusCounts   `json:"status_counts"`
	UpcomingCount  int                    `json:"upcoming_count"`
	ResolvedCount  int                    `json:"resolved_handoff_count"` // 该顾客转人工被商户处理过的次数
	GeneratedAt    time.Time              `json:"generated_at"`
}

// CustomerProfile 顾客档案
type CustomerProfile struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Phone          string     `json:"phone"`
	WechatOpenID   string     `json:"wechat_open_id"`
	ExternalUserID string     `json:"external_user_id"`
	TotalVisits    int        `json:"total_visits"`
	NoShowCount    int        `json:"no_show_count"`
	LateCancelCount int       `json:"late_cancel_count"`
	TagsRaw        string     `json:"tags_raw"` // 逗号分隔的原始字段，便于调试
	LastVisitAt    *time.Time `json:"last_visit_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// CustomerApptItem 顾客的某条预约（精简字段，避免 N+1）
type CustomerApptItem struct {
	ID          string     `json:"id"`
	Date        string     `json:"date"`
	Time        string     `json:"time"`
	BarberName  string     `json:"barber_name"`
	Service     string     `json:"service"`
	Status      string     `json:"status"`
	CancelType  string     `json:"cancel_type,omitempty"`
	CancelReason string    `json:"cancel_reason,omitempty"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// CustomerStatusCounts 4 种预约状态累计
type CustomerStatusCounts struct {
	Active    int `json:"active"`
	Completed int `json:"completed"`
	Cancelled int `json:"cancelled"`
	Noshow    int `json:"noshow"`
}

// getCustomerDetailHandler GET /api/admin/customers/:id
//
// 流程：
//  1. 取 claims.shopID
//  2. 查 customers WHERE id = :id（404 if not found）
//  3. 校验 customerInShop —— 顾客必须在本店有预约（防止泄漏跨店数据）
//  4. 聚合 4 种状态 + upcoming count + 最近 20 条
//  5. 聚合 handoff_resolved 埋点数（按 customer_id 过滤）
func getCustomerDetailHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	customerID := c.Param("id")
	if customerID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}

	// 1) 顾客基础信息
	var cust storage.Customer
	if err := storage.DB.WithContext(ctx).Where("id = ?", customerID).First(&cust).Error; err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "顾客不存在"})
		return
	}

	// 2) 多店隔离：该顾客必须在本店有预约
	if !customerInShop(ctx, shopID, customerID) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "顾客不存在于本店"})
		return
	}

	// 3) 4 种状态累计
	counts := CustomerStatusCounts{}
	type countRow struct {
		Status string
		N      int
	}
	var rows []countRow
	storage.DB.WithContext(ctx).
		Table("appointments").
		Select("status, COUNT(*) as n").
		Where("shop_id = ? AND customer_id = ?", shopID, customerID).
		Group("status").
		Scan(&rows)
	for _, r := range rows {
		switch r.Status {
		case "active":
			counts.Active = r.N
		case "completed":
			counts.Completed = r.N
		case "cancelled":
			counts.Cancelled = r.N
		case "noshow":
			counts.Noshow = r.N
		}
	}

	// 4) upcoming count（未来的 active 预约）
	today := time.Now().Format("2006-01-02")
	var upcomingCount int64
	storage.DB.WithContext(ctx).
		Table("appointments").
		Where("shop_id = ? AND customer_id = ? AND status = ? AND date >= ?",
			shopID, customerID, "active", today).
		Count(&upcomingCount)
	counts.Active = int(upcomingCount) // 覆盖：active = upcoming（语义更准）
	// 注：上面 counts.Active 已经被覆盖为 upcoming 数。原"active 总数"无业务价值，商户只想看未来待到店。

	// 5) 最近 20 条预约
	var appts []storage.Appointment
	if err := storage.DB.WithContext(ctx).
		Where("shop_id = ? AND customer_id = ?", shopID, customerID).
		Order("date DESC, time DESC").
		Limit(20).
		Find(&appts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	recent := make([]CustomerApptItem, 0, len(appts))
	for _, a := range appts {
		recent = append(recent, CustomerApptItem{
			ID:           a.ID,
			Date:         a.Date,
			Time:         a.Time,
			BarberName:   a.BarberName,
			Service:      a.Service,
			Status:       a.Status,
			CancelType:   a.CancelType,
			CancelReason: a.CancelReason,
			CancelledAt:  a.CancelledAt,
			CreatedAt:    a.CreatedAt,
		})
	}

	// 6) 该顾客被处理过的转人工次数
	var resolvedCount int64
	storage.DB.WithContext(ctx).
		Table("event_logs").
		Where("shop_id = ? AND event_type = ? AND customer_id = ?",
			shopID, storage.EventHandoffResolved, customerID).
		Count(&resolvedCount)

	// 7) 组装
	tags := splitTags(cust.Tags)
	resp := CustomerDetailResponse{
		Profile: CustomerProfile{
			ID:              cust.ID,
			Name:            cust.Name,
			Phone:           cust.Phone,
			WechatOpenID:    cust.WechatOpenID,
			ExternalUserID:  cust.ExternalUserID,
			TotalVisits:     cust.TotalVisits,
			NoShowCount:     cust.NoShowCount,
			LateCancelCount: cust.LateCancelCount,
			TagsRaw:         cust.Tags,
			LastVisitAt:     cust.LastVisitAt,
			CreatedAt:       cust.CreatedAt,
		},
		Tags:           tags,
		Recent:         recent,
		StatusCounts:   counts,
		UpcomingCount:  int(upcomingCount),
		ResolvedCount:  int(resolvedCount),
		GeneratedAt:    time.Now(),
	}
	c.JSON(http.StatusOK, resp)
}

// splitTags 把 "VIP,FREQUENT" → ["VIP", "FREQUENT"]（trim + 去空 + 去重保序）
func splitTags(s string) []string {
	seen := make(map[string]bool, 4)
	out := make([]string, 0, 4)
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// ============================================================
// 9) 转人工「已处理」（v4.6 PRD §11.14.6 #5）
// ============================================================

// ResolveHandoffRequest POST body
type ResolveHandoffRequest struct {
	Note string `json:"note"`         // 可选备注
	CustomerID string `json:"customer_id"` // 顾客 ID（冗余；原 handoff event 的 ref_id 解析不出来时便于反查）
}

// ResolveHandoffResponse 响应
type ResolveHandoffResponse struct {
	ResolvedEventID uint64    `json:"resolved_event_id"` // 新写的 handoff_resolved 埋点 ID
	ResolvedAt      time.Time `json:"resolved_at"`
	ResolvedBy      string    `json:"resolved_by"` // 商户后台 admin ID（JWT claims.AdminID 字符串化）
}

// resolveHandoffHandler POST /api/admin/handoffs/:id/resolve
//
// :id = 原始 handoff event_log.id
//
// 流程：
//  1. 校验 claims.shopID
//  2. 查 event_log WHERE id = :id AND shop_id = ? AND event_type = 'handoff_to_human'
//     404 if not found
//  3. 写一条新 event_log：event_type = handoff_resolved，ref_id = 商户用户名，
//     meta.resolved_from = 原 handoff event id, meta.customer_id = 原 ref_id, meta.note = 备注
//  4. 返回新 event id
//
// 注：resolved 与原 handoff 是 N:1 关系（同一 handoff 可被处理多次，便于审计）。
// 业务上一般只处理一次，前端按"最新一条 resolved"判定"已处理"。
func resolveHandoffHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	handoffIDStr := c.Param("id")
	if handoffIDStr == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "handoff id required"})
		return
	}
	handoffID, err := strconv.ParseUint(handoffIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "handoff id 格式错误"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}

	// 1) 查原 handoff event
	var origin storage.EventLog
	if err := storage.DB.WithContext(ctx).Where("id = ?", handoffID).First(&origin).Error; err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "转人工记录不存在"})
		return
	}
	if origin.ShopID != shopID {
		c.JSON(http.StatusForbidden, map[string]string{"error": "无权操作其他店铺的转人工"})
		return
	}
	if origin.EventType != storage.EventHandoffToHuman {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "该 event 不是 handoff_to_human 类型"})
		return
	}

	// 2) 解析请求 body（可选；空 body 也接受）
	var req ResolveHandoffRequest
	_ = c.BindAndValidate(&req) // 空 body 不报错；忽略解析错误（保持 resolve 操作幂等）
	note := strings.TrimSpace(req.Note)
	if len(note) > 256 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "note 长度上限 256"})
		return
	}

	// 3) 取商户 admin ID
	cl := auth.GetClaims(c)
	resolvedBy := ""
	if cl != nil && cl.AdminID > 0 {
		resolvedBy = strconv.FormatUint(cl.AdminID, 10)
	}

	// 4) 写 resolved 埋点
	customerID := strings.TrimSpace(req.CustomerID)
	if customerID == "" {
		customerID = origin.RefID // 兜底用原 ref_id
	}
	meta := map[string]any{
		"resolved_from": origin.ID,
		"customer_id":   customerID,
		"resolved_by":   resolvedBy,
		"note":          note,
	}
	var metaStr string
	if b, mErr := json.Marshal(meta); mErr == nil {
		metaStr = string(b)
	}
	now := time.Now()
	resolved := storage.EventLog{
		ShopID:     shopID,
		CustomerID: customerID,
		EventType:  storage.EventHandoffResolved,
		RefID:      resolvedBy,
		Meta:       metaStr,
		CreatedAt:  now,
	}
	if err := storage.DB.WithContext(ctx).Create(&resolved).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, ResolveHandoffResponse{
		ResolvedEventID: resolved.ID,
		ResolvedAt:      now,
		ResolvedBy:      resolvedBy,
	})
}

// ============================================================
// 10) 服务批量导入（v4.6 PRD §11.14.6 #1）
// ============================================================

// ImportServiceItem 单行服务
type ImportServiceItem struct {
	Name         string `json:"name"`
	EstimatedMin int    `json:"estimated_min"`
	PriceRange   string `json:"price_range,omitempty"`
}

// ImportServicesRequest 批量导入请求
type ImportServicesRequest struct {
	Services []ImportServiceItem `json:"services"`
	// Replace: true = 清空现有 active 服务再导入（适用于"按目录完全重置"）
	//          false（默认）= 增量，跳过同名
	Replace bool `json:"replace,omitempty"`
}

// ImportServicesResponse 响应
type ImportServicesResponse struct {
	SuccessCount int                `json:"success_count"`
	SkippedCount int                `json:"skipped_count"`     // 同名跳过
	FailedCount  int                `json:"failed_count"`
	Imported     []ImportedService  `json:"imported,omitempty"`
	Skipped      []SkippedService   `json:"skipped,omitempty"`
	Failed       []FailedService    `json:"failed,omitempty"`
	Replaced     int                `json:"replaced_count,omitempty"` // Replace=true 时清掉的旧服务数
}

// ImportedService 成功导入的
type ImportedService struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

// SkippedService 跳过的（同名）
type SkippedService struct {
	Name string `json:"name"`
	Reason string `json:"reason"`
}

// FailedService 失败的（验证错误）
type FailedService struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// importServicesHandler POST /api/admin/services/import
//
// Body: {"services":[{name, estimated_min, price_range?}], "replace": false}
//
// 行为：
//   - replace = false（默认）：增量。同名跳过（记 skipped）。其它校验失败记 failed。
//   - replace = true：先软下架现有所有 active 服务（保留历史），再按 list 顺序导入。
//
// 行数上限：100（防止误传大文件把服务目录撑爆）。
func importServicesHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	var req ImportServicesRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Services) == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "services 数组不能为空"})
		return
	}
	if len(req.Services) > 100 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("单次最多导入 100 条，实际 %d", len(req.Services))})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}

	resp := ImportServicesResponse{}

	// 1) replace 模式：先软下架现有所有 active 服务
	if req.Replace {
		var existing []storage.Service
		storage.DB.WithContext(ctx).
			Where("shop_id = ? AND is_active = ?", shopID, true).
			Find(&existing)
		for _, e := range existing {
			if err := storage.DeactivateService(ctx, shopID, e.ID); err == nil {
				resp.Replaced++
			}
		}
	}

	// 2) 逐行处理
	for _, item := range req.Services {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			resp.Failed = append(resp.Failed, FailedService{
				Name:   name,
				Reason: "服务名不能为空",
			})
			resp.FailedCount++
			continue
		}
		if len(name) > 32 {
			resp.Failed = append(resp.Failed, FailedService{
				Name:   name,
				Reason: "服务名过长（最多 32 字）",
			})
			resp.FailedCount++
			continue
		}
		if item.EstimatedMin <= 0 || item.EstimatedMin > 480 {
			resp.Failed = append(resp.Failed, FailedService{
				Name:   name,
				Reason: fmt.Sprintf("预估时长必须在 1-480 分钟，得到 %d", item.EstimatedMin),
			})
			resp.FailedCount++
			continue
		}
		if len(item.PriceRange) > 64 {
			resp.Failed = append(resp.Failed, FailedService{
				Name:   name,
				Reason: "价格区间过长（最多 64 字）",
			})
			resp.FailedCount++
			continue
		}

		// 同名预检（避免依赖底层唯一约束返回 500）
		var dup storage.Service
		dupErr := storage.DB.WithContext(ctx).
			Where("shop_id = ? AND name = ?", shopID, name).
			First(&dup).Error
		if dupErr == nil {
			resp.Skipped = append(resp.Skipped, SkippedService{
				Name:   name,
				Reason: "已存在同名服务（保留原条目）",
			})
			resp.SkippedCount++
			continue
		}

		// 创建
		svc, createErr := storage.CreateService(ctx, shopID, name, item.EstimatedMin, item.PriceRange)
		if createErr != nil {
			resp.Failed = append(resp.Failed, FailedService{
				Name:   name,
				Reason: createErr.Error(),
			})
			resp.FailedCount++
			continue
		}
		resp.Imported = append(resp.Imported, ImportedService{
			Name: svc.Name,
			ID:   svc.ID,
		})
		resp.SuccessCount++
	}

	// 排序：便于前端 diff / UI 稳定展示
	sortImportedReport(&resp)

	c.JSON(http.StatusOK, resp)
}

// ============================================================
// 11) 工具方法
// ============================================================

// sortImportedReport 按 name 字典序排序（便于前端 diff / UI 稳定展示）
func sortImportedReport(r *ImportServicesResponse) {
	sort.Slice(r.Imported, func(i, j int) bool { return r.Imported[i].Name < r.Imported[j].Name })
	sort.Slice(r.Skipped, func(i, j int) bool { return r.Skipped[i].Name < r.Skipped[j].Name })
	sort.Slice(r.Failed, func(i, j int) bool { return r.Failed[i].Name < r.Failed[j].Name })
}
