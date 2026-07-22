package api

// admin_platform.go —— 平台超管（platform_admin）专属管理面板（v4.13.0）
//
// 背景：
//   - v4.10.1 把 /subscription /subscription/renew 锁给 platform_admin
//     但 handler 内部仍用 shopFromClaims → 只能管"超管自己 shop_id"（无意义）
//   - v4.13.0 给 platform_admin 补齐真正的"跨店"管理能力：
//     列全平台店铺、改任意店铺套餐、看 audit log
//
// 路由（全部 RequireRole(RolePlatformAdmin)）：
//   GET  /api/admin/platform/stats                       平台总览 KPI
//   GET  /api/admin/platform/shops                       全平台店铺列表（含套餐 / 到期 / 状态）
//   GET  /api/admin/platform/shops/:id                   单店详情 + 订阅历史 + 成员
//   PUT  /api/admin/platform/shops/:id/plan              给某店开/改套餐（写 subscription + shop.plan + event_log 审计）
//   GET  /api/admin/platform/audit?limit=100             套餐变更审计日志（近 N 条）
//
// 设计：
//   - shopID 从 :id 路径参数取（不读 claims.shopID）—— platform_admin 跨店
//   - plan 校验复用 storage.IsValidPlanID / GetPlan
//   - 审计：复用 storage.TrackEvent 写 EventLog（event_type=plan_changed_by_admin）
//   - 改完 plan 调 auth.InvalidatePlanActiveCache 让中间件立即感知

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/server"
	"github.com/yuterigele/openbook/storage"
)

// planNameByID 取 plan 中文名（id 不存在时回退到 id 本身）
func planNameByID(id string) string {
	if m, ok := storage.GetPlan(id); ok && m.Name != "" {
		return m.Name
	}
	return id
}

// PlatformAgentObservability is intentionally platform-wide and process-local.
// It must only be returned through the platform_admin route because token usage
// and cross-tenant Agent health are operational data, not shop data.
type PlatformAgentObservability struct {
	Agent       server.AgentMetricsSnapshot `json:"agent"`
	LLM         chatmodel.UsageSnapshot     `json:"llm"`
	GeneratedAt time.Time                   `json:"generated_at"`
}

// platformAgentObservabilityHandler GET /api/admin/platform/agent-observability
// Route-level RequireRole(RolePlatformAdmin) is the access-control boundary.
func platformAgentObservabilityHandler(_ context.Context, c *app.RequestContext) {
	c.JSON(http.StatusOK, PlatformAgentObservability{
		Agent:       server.DefaultAgentMetrics.Snapshot(),
		LLM:         chatmodel.DefaultUsageTracker.Snapshot(),
		GeneratedAt: time.Now(),
	})
}

// ============================================================
// 1) 平台总览 KPI
// ============================================================

// PlatformStats 平台总览
type PlatformStats struct {
	TotalShops         int          `json:"total_shops"`
	TotalMembers       int          `json:"total_members"`
	TotalAppointments  int          `json:"total_appointments"`
	PlanDistribution   []PlanBucket `json:"plan_distribution"`
	ExpiringSoon       int          `json:"expiring_soon"`        // 7 天内到期
	Frozen             int          `json:"frozen"`               // 已冻结
	MonthlyRevenueYuan int          `json:"monthly_revenue_yuan"` // 估算（按 plan 默认月费）
	GeneratedAt        time.Time    `json:"generated_at"`
}

// PlanBucket 某档 plan 的汇总
type PlanBucket struct {
	Plan         string `json:"plan"`
	PlanName     string `json:"plan_name"`
	ShopCount    int    `json:"shop_count"`
	MonthlyCents int    `json:"monthly_cents"`
	SubtotalYuan int    `json:"subtotal_yuan"`
}

// platformStatsHandler GET /api/admin/platform/stats
func platformStatsHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	stats := PlatformStats{
		GeneratedAt:      time.Now(),
		PlanDistribution: make([]PlanBucket, 0, len(storage.AllPlanIDs)),
	}

	shops := storage.ListAllShops(ctx)
	stats.TotalShops = len(shops)

	var totalMembers int64
	storage.DB.WithContext(ctx).Model(&storage.ShopAdmin{}).Count(&totalMembers)
	stats.TotalMembers = int(totalMembers)

	var totalAppts int64
	storage.DB.WithContext(ctx).Model(&storage.Appointment{}).Count(&totalAppts)
	stats.TotalAppointments = int(totalAppts)

	// 按 plan 分布 + frozen/expiring 检查
	planCounts := make(map[string]int, len(storage.AllPlanIDs))
	for _, s := range shops {
		p := s.Plan
		if !storage.IsValidPlanID(p) {
			p = storage.DefaultPlanID
		}
		planCounts[p]++

		frozen, _ := storage.IsPlanExpired(ctx, s.ID)
		if frozen {
			stats.Frozen++
		} else {
			var sub storage.Subscription
			if err := storage.DB.WithContext(ctx).
				Where("shop_id = ? AND cancelled_at IS NULL", s.ID).
				Order("expires_at DESC").
				First(&sub).Error; err == nil {
				daysLeft := int(time.Until(sub.ExpiresAt).Hours() / 24)
				if daysLeft >= 0 && daysLeft <= 7 {
					stats.ExpiringSoon++
				}
			}
		}
	}

	monthlyCentsTotal := 0
	for _, pid := range storage.AllPlanIDs {
		meta, ok := storage.GetPlan(pid)
		if !ok {
			continue
		}
		n := planCounts[pid]
		subtotalYuan := n * meta.PriceCents / 100
		monthlyCentsTotal += n * meta.PriceCents
		stats.PlanDistribution = append(stats.PlanDistribution, PlanBucket{
			Plan:         pid,
			PlanName:     meta.Name,
			ShopCount:    n,
			MonthlyCents: meta.PriceCents,
			SubtotalYuan: subtotalYuan,
		})
	}
	stats.MonthlyRevenueYuan = monthlyCentsTotal / 100

	c.JSON(http.StatusOK, stats)
}

// ============================================================
// 2) 全平台店铺列表
// ============================================================

// PlatformShopItem 全平台店铺列表的一项
type PlatformShopItem struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Address      string     `json:"address"`
	ParentShopID string     `json:"parent_shop_id,omitempty"`
	Plan         string     `json:"plan"`
	PlanName     string     `json:"plan_name"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	DaysLeft     int        `json:"days_left"`
	Frozen       bool       `json:"frozen"`
	MemberCount  int        `json:"member_count"`
	ApptCount    int        `json:"appt_count"`     // 累计
	ApptCount30d int        `json:"appt_count_30d"` // 近 30 天
	CreatedAt    time.Time  `json:"created_at"`
}

// PlatformShopsResponse 列表响应
type PlatformShopsResponse struct {
	Total int                `json:"total"`
	Shops []PlatformShopItem `json:"shops"`
}

// platformShopsHandler GET /api/admin/platform/shops
func platformShopsHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	shops := storage.ListAllShops(ctx)
	shopIDs := make([]string, 0, len(shops))
	for _, s := range shops {
		shopIDs = append(shopIDs, s.ID)
	}

	// 批量算 member_count / appt_count（避免 N+1）
	memberByShop := batchCountByShopID(ctx, "admins", "shop_id", shopIDs)
	apptByShop := batchCountByShopID(ctx, "appointments", "shop_id", shopIDs)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	thirtyDaysAgo := time.Now().In(loc).AddDate(0, 0, -30).Format("2006-01-02")
	appt30ByShop := batchCountByShopIDWithExtra(ctx, "appointments", "shop_id", shopIDs, "date", thirtyDaysAgo)

	items := make([]PlatformShopItem, 0, len(shops))
	for _, s := range shops {
		plan := s.Plan
		if !storage.IsValidPlanID(plan) {
			plan = storage.DefaultPlanID
		}
		var sub storage.Subscription
		var expiresAt *time.Time
		var daysLeft int
		if err := storage.DB.WithContext(ctx).
			Where("shop_id = ? AND cancelled_at IS NULL", s.ID).
			Order("expires_at DESC").
			First(&sub).Error; err == nil {
			t := sub.ExpiresAt
			expiresAt = &t
			diff := time.Until(t)
			daysLeft = int(diff.Hours() / 24)
			if diff.Hours() < 0 {
				daysLeft = 0
			}
		}
		frozen, _ := storage.IsPlanExpired(ctx, s.ID)

		items = append(items, PlatformShopItem{
			ID:           s.ID,
			Name:         s.Name,
			Address:      s.Address,
			ParentShopID: s.ParentShopID,
			Plan:         plan,
			PlanName:     planNameByID(plan),
			ExpiresAt:    expiresAt,
			DaysLeft:     daysLeft,
			Frozen:       frozen,
			MemberCount:  memberByShop[s.ID],
			ApptCount:    apptByShop[s.ID],
			ApptCount30d: appt30ByShop[s.ID],
			CreatedAt:    s.CreatedAt,
		})
	}

	c.JSON(http.StatusOK, PlatformShopsResponse{
		Total: len(items),
		Shops: items,
	})
}

// batchCountByShopID 按 shop_id 批量 group count（避免 N+1）
func batchCountByShopID(ctx context.Context, table, shopCol string, shopIDs []string) map[string]int {
	out := make(map[string]int, len(shopIDs))
	if len(shopIDs) == 0 || storage.DB == nil {
		return out
	}
	type row struct {
		ShopID string
		N      int64
	}
	var rows []row
	storage.DB.WithContext(ctx).
		Table(table).
		Select(shopCol+" as shop_id, count(*) as n").
		Where(shopCol+" IN ?", shopIDs).
		Group(shopCol).
		Scan(&rows)
	for _, r := range rows {
		out[r.ShopID] = int(r.N)
	}
	return out
}

// batchCountByShopIDWithExtra 多一个 where 条件（如 date >= ?）
func batchCountByShopIDWithExtra(ctx context.Context, table, shopCol string, shopIDs []string, extraCol, extraVal string) map[string]int {
	out := make(map[string]int, len(shopIDs))
	if len(shopIDs) == 0 || storage.DB == nil {
		return out
	}
	type row struct {
		ShopID string
		N      int64
	}
	var rows []row
	storage.DB.WithContext(ctx).
		Table(table).
		Select(shopCol+" as shop_id, count(*) as n").
		Where(shopCol+" IN ? AND "+extraCol+" >= ?", shopIDs, extraVal).
		Group(shopCol).
		Scan(&rows)
	for _, r := range rows {
		out[r.ShopID] = int(r.N)
	}
	return out
}

// ============================================================
// 3) 单店详情 + 订阅历史 + 成员
// ============================================================

// PlatformShopDetail 单店详情
type PlatformShopDetail struct {
	Shop          PlatformShopItem     `json:"shop"`
	Subscriptions []PlatformSubItem    `json:"subscriptions"`
	Members       []PlatformMemberItem `json:"members"`
}

// PlatformSubItem 订阅历史一项
type PlatformSubItem struct {
	ID          string     `json:"id"`
	Plan        string     `json:"plan"`
	PlanName    string     `json:"plan_name"`
	StartedAt   time.Time  `json:"started_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
	IsCurrent   bool       `json:"is_current"`
	AutoRenew   bool       `json:"auto_renew"`
	CreatedAt   time.Time  `json:"created_at"`
}

// PlatformMemberItem 成员概览
type PlatformMemberItem struct {
	ID        uint64    `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// platformShopDetailHandler GET /api/admin/platform/shops/:id
func platformShopDetailHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	shopID := c.Param("id")
	if shopID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "shop id required"})
		return
	}
	shop, err := storage.GetShopByID(ctx, shopID)
	if err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "店铺不存在"})
		return
	}

	plan := shop.Plan
	if !storage.IsValidPlanID(plan) {
		plan = storage.DefaultPlanID
	}
	var sub storage.Subscription
	var expiresAt *time.Time
	var daysLeft int
	if err := storage.DB.WithContext(ctx).
		Where("shop_id = ? AND cancelled_at IS NULL", shopID).
		Order("expires_at DESC").
		First(&sub).Error; err == nil {
		t := sub.ExpiresAt
		expiresAt = &t
		diff := time.Until(t)
		daysLeft = int(diff.Hours() / 24)
		if diff.Hours() < 0 {
			daysLeft = 0
		}
	}
	frozen, _ := storage.IsPlanExpired(ctx, shopID)

	var memberCount, apptCount, appt30d int64
	storage.DB.WithContext(ctx).Model(&storage.ShopAdmin{}).Where("shop_id = ?", shopID).Count(&memberCount)
	storage.DB.WithContext(ctx).Model(&storage.Appointment{}).Where("shop_id = ?", shopID).Count(&apptCount)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	thirtyDaysAgo := time.Now().In(loc).AddDate(0, 0, -30).Format("2006-01-02")
	storage.DB.WithContext(ctx).Model(&storage.Appointment{}).Where("shop_id = ? AND date >= ?", shopID, thirtyDaysAgo).Count(&appt30d)

	shopItem := PlatformShopItem{
		ID:           shop.ID,
		Name:         shop.Name,
		Address:      shop.Address,
		ParentShopID: shop.ParentShopID,
		Plan:         plan,
		PlanName:     planNameByID(plan),
		ExpiresAt:    expiresAt,
		DaysLeft:     daysLeft,
		Frozen:       frozen,
		MemberCount:  int(memberCount),
		ApptCount:    int(apptCount),
		ApptCount30d: int(appt30d),
		CreatedAt:    shop.CreatedAt,
	}

	// 订阅历史
	var subs []storage.Subscription
	if err := storage.DB.WithContext(ctx).
		Where("shop_id = ?", shopID).
		Order("started_at DESC").
		Find(&subs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	now := time.Now()
	subItems := make([]PlatformSubItem, 0, len(subs))
	for _, s := range subs {
		subItems = append(subItems, PlatformSubItem{
			ID:          s.ID,
			Plan:        s.Plan,
			PlanName:    planNameByID(s.Plan),
			StartedAt:   s.StartedAt,
			ExpiresAt:   s.ExpiresAt,
			CancelledAt: s.CancelledAt,
			IsCurrent:   s.CancelledAt == nil && s.ExpiresAt.After(now),
			AutoRenew:   s.AutoRenew,
			CreatedAt:   s.CreatedAt,
		})
	}

	// 成员
	var admins []storage.ShopAdmin
	if err := storage.DB.WithContext(ctx).
		Where("shop_id = ?", shopID).
		Order("id ASC").
		Find(&admins).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	memberItems := make([]PlatformMemberItem, 0, len(admins))
	for _, a := range admins {
		memberItems = append(memberItems, PlatformMemberItem{
			ID:        a.ID,
			Username:  a.Username,
			Role:      a.Role,
			Status:    a.Status,
			CreatedAt: a.CreatedAt,
		})
	}

	c.JSON(http.StatusOK, PlatformShopDetail{
		Shop:          shopItem,
		Subscriptions: subItems,
		Members:       memberItems,
	})
}

// ============================================================
// 4) 给某店开/改套餐
// ============================================================

// SetShopPlanRequest 改套餐的 body
type SetShopPlanRequest struct {
	Plan   string `json:"plan"`   // basic / pro / flagship / enterprise
	Months int    `json:"months"` // 续费月数
	Note   string `json:"note"`   // 备注（写入 audit log）
}

// SetShopPlanResponse 响应
type SetShopPlanResponse struct {
	Status         string    `json:"status"`
	ShopID         string    `json:"shop_id"`
	ShopName       string    `json:"shop_name"`
	OldPlan        string    `json:"old_plan"`
	NewPlan        string    `json:"new_plan"`
	ExpiresAt      time.Time `json:"expires_at"`
	SubscriptionID string    `json:"subscription_id"`
}

// platformSetShopPlanHandler PUT /api/admin/platform/shops/:id/plan
//
// 流程：取消旧 sub → 建新 sub → 更新 shop.plan/expires_at → 清 cache → 写 audit
func platformSetShopPlanHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	shopID := c.Param("id")
	if shopID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "shop id required"})
		return
	}
	var req SetShopPlanRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req.Plan = strings.TrimSpace(req.Plan)
	if !storage.IsValidPlanID(req.Plan) {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "未知 plan: " + req.Plan + "（支持: " + strings.Join(storage.AllPlanIDs, ", ") + "）",
		})
		return
	}
	if req.Months <= 0 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "months 必须 > 0"})
		return
	}
	if req.Months > 60 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "months 最多 60（5 年）"})
		return
	}

	oldShop, err := storage.GetShopByID(ctx, shopID)
	if err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "店铺不存在"})
		return
	}
	oldPlan := oldShop.Plan
	if !storage.IsValidPlanID(oldPlan) {
		oldPlan = ""
	}

	now := time.Now()
	expiresAt := now.AddDate(0, req.Months, 0)

	// 1) 取消所有 active sub
	if err := storage.DB.WithContext(ctx).Model(&storage.Subscription{}).
		Where("shop_id = ? AND cancelled_at IS NULL", shopID).
		Update("cancelled_at", now).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "取消旧订阅失败: " + err.Error()})
		return
	}
	// 2) 新建 subscription
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
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "建新订阅失败: " + err.Error()})
		return
	}
	// 3) 更新 shop.plan / shop.expires_at
	if err := storage.DB.WithContext(ctx).Model(&storage.Shop{}).
		Where("id = ?", shopID).
		Updates(map[string]interface{}{
			"plan":       req.Plan,
			"expires_at": expiresAt,
		}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "更新 shop 失败: " + err.Error()})
		return
	}
	// 4) 清 plan active cache
	auth.InvalidatePlanActiveCache(shopID)

	// 5) 写 audit log
	adminID, adminUsername := auditAdminInfo(ctx, c)
	storage.TrackEvent(ctx, shopID, "plan_changed_by_admin", sub.ID, map[string]any{
		"old_plan":       oldPlan,
		"new_plan":       req.Plan,
		"months":         req.Months,
		"expires_at":     expiresAt,
		"admin_id":       adminID,
		"admin_username": adminUsername,
		"note":           req.Note,
	})

	c.JSON(http.StatusOK, SetShopPlanResponse{
		Status:         "ok",
		ShopID:         shopID,
		ShopName:       oldShop.Name,
		OldPlan:        oldPlan,
		NewPlan:        req.Plan,
		ExpiresAt:      expiresAt,
		SubscriptionID: sub.ID,
	})
}

// auditAdminInfo 从 JWT 抽 admin_id + 从 admins 表补 username（供 audit log）
//   - 失败时返 (0, "")，调用方自己决定兜底
func auditAdminInfo(ctx context.Context, c *app.RequestContext) (uint64, string) {
	cl := auth.GetClaims(c)
	if cl == nil {
		return 0, ""
	}
	if storage.DB == nil {
		return cl.AdminID, cl.Role
	}
	var a storage.ShopAdmin
	if err := storage.DB.WithContext(ctx).First(&a, cl.AdminID).Error; err == nil {
		return a.ID, a.Username
	}
	return cl.AdminID, cl.Role
}

// ============================================================
// 5) Audit log
// ============================================================

// PlatformAuditItem 一条 audit
type PlatformAuditItem struct {
	ID        uint64    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ShopID    string    `json:"shop_id"`
	ShopName  string    `json:"shop_name"`
	OldPlan   string    `json:"old_plan"`
	NewPlan   string    `json:"new_plan"`
	Months    int       `json:"months"`
	AdminID   uint64    `json:"admin_id"`
	AdminUser string    `json:"admin_user"`
	Note      string    `json:"note"`
	ExpiresAt string    `json:"expires_at"`
}

// planChangeMeta event_log meta 的 schema（写 audit 时用）
type planChangeMeta struct {
	OldPlan       string    `json:"old_plan"`
	NewPlan       string    `json:"new_plan"`
	Months        int       `json:"months"`
	ExpiresAt     time.Time `json:"expires_at"`
	AdminID       uint64    `json:"admin_id"`
	AdminUsername string    `json:"admin_username"`
	Note          string    `json:"note"`
}

// platformAuditHandler GET /api/admin/platform/audit?limit=100
func platformAuditHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	limit := 100
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	var rows []storage.EventLog
	if err := storage.DB.WithContext(ctx).
		Where("event_type = ?", "plan_changed_by_admin").
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// 收集 shop_id 一次性查 shop name（避免 N+1）
	shopIDs := make([]string, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		if r.ShopID != "" && !seen[r.ShopID] {
			seen[r.ShopID] = true
			shopIDs = append(shopIDs, r.ShopID)
		}
	}
	shopNameByID := make(map[string]string, len(shopIDs))
	if len(shopIDs) > 0 {
		var shops []storage.Shop
		storage.DB.WithContext(ctx).Where("id IN ?", shopIDs).Find(&shops)
		for _, s := range shops {
			shopNameByID[s.ID] = s.Name
		}
	}

	items := make([]PlatformAuditItem, 0, len(rows))
	for _, r := range rows {
		item := PlatformAuditItem{
			ID:        r.ID,
			CreatedAt: r.CreatedAt,
			ShopID:    r.ShopID,
			ShopName:  shopNameByID[r.ShopID],
		}
		var m planChangeMeta
		if err := json.Unmarshal([]byte(r.Meta), &m); err == nil {
			item.OldPlan = m.OldPlan
			item.NewPlan = m.NewPlan
			item.Months = m.Months
			item.AdminID = m.AdminID
			item.AdminUser = m.AdminUsername
			item.Note = m.Note
			item.ExpiresAt = m.ExpiresAt.Format("2006-01-02")
		}
		items = append(items, item)
	}
	c.JSON(http.StatusOK, map[string]any{
		"total": len(items),
		"items": items,
	})
}
