package api

// admin_features.go
//
// v4.4 补全商户后台 — 6 个新模块的 handler：
//
//   1) 店铺设置   GET /api/admin/shop          +  PUT /api/admin/shop
//   2) 转人工列表 GET /api/admin/handoffs      （从 event_logs 筛 handoff_to_human）
//   3) 顾客管理   GET /api/admin/customers     +  POST/DELETE 标签
//   4) 续费管理   GET /api/admin/subscription  （当前订阅 + 历史列表）
//                  POST /api/admin/subscription/renew  已有，路径沿用 api.go 的
//   5) 服务目录   GET/POST/PUT/DELETE /api/admin/services
//   6) 周报预览   GET /api/admin/weekly-report +  GET /api/admin/weekly-report/chain
//                  复用 storage.BuildWeeklyUsageReport / BuildChainWeeklyUsageReport
//
// 设计约定：
//   - shopID 一律从 JWT claims 取（多店隔离）
//   - 错误响应统一 map[string]string{"error": "..."}
//   - 入参用 BindAndValidate 自动 JSON 解析
//   - 状态码：400 输入、401 无 session、403 跨店、404 不存在、409 冲突、500 其它

import (
	"context"
	"encoding/json"
	"errors"
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
// 1) 店铺设置
// ============================================================

// ShopUpdateRequest 商户后台更新店铺字段（PRD §5 shops 表）
//
// 注意：wecom_* 字段不在此处暴露（多店版本每个店单独配，env 注入；后台改 wecom 会引入
// 一致性风险）。Plan / ExpiresAt 也不在此处直接改（走 subscription/renew 接口）。
type ShopUpdateRequest struct {
	Name        *string `json:"name,omitempty"`         // 店铺名
	Address     *string `json:"address,omitempty"`      // 店铺地址
	OpenHour    *int    `json:"open_hour,omitempty"`    // 营业开始（0-23）
	CloseHour   *int    `json:"close_hour,omitempty"`   // 营业结束（1-24）
	LunchStart  *int    `json:"lunch_start,omitempty"`  // 午休开始
	LunchEnd    *int    `json:"lunch_end,omitempty"`    // 午休结束
	LunchEndMin *int    `json:"lunch_end_min,omitempty"` // 午休结束分钟
	Timezone    *string `json:"timezone,omitempty"`     // IANA 时区
	Holidays    *string `json:"holidays,omitempty"`     // 逗号分隔 YYYY-MM-DD
}

// getShopHandler GET /api/admin/shop
//
// 返回当前 admin 所属店铺的完整配置
func getShopHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	shop, err := storage.GetShopByID(ctx, shopID)
	if err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "店铺不存在"})
		return
	}
	c.JSON(http.StatusOK, shop)
}

// updateShopHandler PUT /api/admin/shop
//
// Body: ShopUpdateRequest（所有字段可选；只更新非 nil 字段）
func updateShopHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	var req ShopUpdateRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// 字段校验
	if req.Name != nil {
		tn := strings.TrimSpace(*req.Name)
		if tn == "" {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "店铺名不能为空"})
			return
		}
		if len(tn) > 128 {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "店铺名过长（最多 128 字）"})
			return
		}
		req.Name = &tn
	}
	if req.Address != nil && len(*req.Address) > 256 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "地址过长（最多 256 字）"})
		return
	}
	if req.OpenHour != nil && (*req.OpenHour < 0 || *req.OpenHour > 23) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "open_hour 必须在 0-23"})
		return
	}
	if req.CloseHour != nil && (*req.CloseHour < 1 || *req.CloseHour > 24) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "close_hour 必须在 1-24"})
		return
	}
	if req.OpenHour != nil && req.CloseHour != nil && *req.OpenHour >= *req.CloseHour {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "open_hour 必须早于 close_hour"})
		return
	}
	if req.LunchStart != nil && (*req.LunchStart < 0 || *req.LunchStart > 23) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "lunch_start 必须在 0-23"})
		return
	}
	if req.LunchEnd != nil && (*req.LunchEnd < 0 || *req.LunchEnd > 24) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "lunch_end 必须在 0-24"})
		return
	}
	if req.LunchEndMin != nil && (*req.LunchEndMin < 0 || *req.LunchEndMin > 59) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "lunch_end_min 必须在 0-59"})
		return
	}
	if req.Timezone != nil && *req.Timezone != "" {
		if _, err := time.LoadLocation(*req.Timezone); err != nil {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "timezone 无效（需 IANA，如 Asia/Shanghai）"})
			return
		}
	}
	if req.Holidays != nil && len(*req.Holidays) > 512 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "holidays 过长（最多 512 字）"})
		return
	}

	updates := map[string]interface{}{"updated_at": time.Now()}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Address != nil {
		updates["address"] = *req.Address
	}
	if req.OpenHour != nil {
		updates["open_hour"] = *req.OpenHour
	}
	if req.CloseHour != nil {
		updates["close_hour"] = *req.CloseHour
	}
	if req.LunchStart != nil {
		updates["lunch_start"] = *req.LunchStart
	}
	if req.LunchEnd != nil {
		updates["lunch_end"] = *req.LunchEnd
	}
	if req.LunchEndMin != nil {
		updates["lunch_end_min"] = *req.LunchEndMin
	}
	if req.Timezone != nil {
		updates["timezone"] = *req.Timezone
	}
	if req.Holidays != nil {
		updates["holidays"] = *req.Holidays
	}

	if err := storage.DB.WithContext(ctx).Model(&storage.Shop{}).
		Where("id = ?", shopID).
		Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	shop, _ := storage.GetShopByID(ctx, shopID)
	c.JSON(http.StatusOK, shop)
}

// ============================================================
// 2) 转人工待处理列表（PRD §9.2 "商户在后台可见待处理列表"）
// ============================================================

// HandoffItem 转人工列表的一项（含解析后的 reason/last_user_message）
type HandoffItem struct {
	ID            uint64    `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	CustomerID    string    `json:"customer_id"`     // ref_id（顾客标识）
	CustomerName  string    `json:"customer_name"`   // 关联 customers.name（可能为空）
	Reason        string    `json:"reason"`          // 解析自 meta.reason
	LastUserMsg   string    `json:"last_user_message"`
	ShopID        string    `json:"shop_id"`
}

// listHandoffsHandler GET /api/admin/handoffs?limit=50
//
// 逻辑：
//   - 筛 event_logs WHERE event_type = 'handoff_to_human' AND shop_id = ?
//   - 解析 meta 里的 reason / last_user_message
//   - 关联 customers 表取顾客名（如果 ref_id 是 customer ID）
//   - 按 created_at DESC
func listHandoffsHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	limit := 50
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	var events []storage.EventLog
	if err := storage.DB.WithContext(ctx).
		Where("shop_id = ? AND event_type = ?", shopID, storage.EventHandoffToHuman).
		Order("created_at DESC").
		Limit(limit).
		Find(&events).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// 收集 ref_id 去查 customer 表
	refIDs := make([]string, 0, len(events))
	seen := make(map[string]bool, len(events))
	for _, e := range events {
		if e.RefID == "" || seen[e.RefID] {
			continue
		}
		seen[e.RefID] = true
		refIDs = append(refIDs, e.RefID)
	}
	custNameByRef := make(map[string]string, len(refIDs))
	if len(refIDs) > 0 {
		// ref_id 可能是 customer.id / wechat_open_id / 顾客名
		// 优先按 ID 查，再按 wechat_open_id 查
		var custs []storage.Customer
		storage.DB.WithContext(ctx).
			Where("id IN ? OR wechat_open_id IN ?", refIDs, refIDs).
			Find(&custs)
		for _, cu := range custs {
			custNameByRef[cu.ID] = cu.Name
			if cu.WechatOpenID != "" {
				custNameByRef[cu.WechatOpenID] = cu.Name
			}
		}
	}

	out := make([]HandoffItem, 0, len(events))
	for _, e := range events {
		item := HandoffItem{
			ID:         e.ID,
			CreatedAt:  e.CreatedAt,
			CustomerID: e.RefID,
			ShopID:     e.ShopID,
		}
		if name, ok := custNameByRef[e.RefID]; ok {
			item.CustomerName = name
		}
		// 解析 meta
		if e.Meta != "" {
			var m map[string]any
			if err := json.Unmarshal([]byte(e.Meta), &m); err == nil {
				if v, ok := m["reason"].(string); ok {
					item.Reason = v
				}
				if v, ok := m["last_user_message"].(string); ok {
					item.LastUserMsg = v
				}
			}
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, out)
}

// ============================================================
// 3) 顾客管理
// ============================================================

// listCustomersHandler GET /api/admin/customers?query=&tag=&limit=200
//
// 过滤：
//   - query: 模糊匹配 name / phone / wechat_open_id
//   - tag:   精确匹配 tag（包含即可；tags 是逗号分隔，用 LIKE 即可）
//   - 默认 limit=200，max=500
//
// 注意：Customer 模型无 shop_id 字段（跨店黑名单场景），但 UI 只看本店需要——
// 这里用"曾在本店有 appointment 的顾客"过滤，避免列出无关顾客。
// 简化策略：先返回最近 200 条 appointment 涉及的 customer_id，再展开 customer 详情。
func listCustomersHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	limit := 200
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	query := strings.TrimSpace(c.Query("query"))
	tag := strings.TrimSpace(c.Query("tag"))
	hasCard := strings.TrimSpace(c.Query("has_card")) // v4.16 P1.2: "yes" = 仅持卡顾客

	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}

	// 先拿到"本店有 appointment 的 customer_id"（最近 1000 条 active 预约里的 distinct 顾客）
	var apptCustIDs []string
	storage.DB.WithContext(ctx).
		Table("appointments").
		Where("shop_id = ? AND customer_id <> ''", shopID).
		Distinct("customer_id").
		Limit(1000).
		Pluck("customer_id", &apptCustIDs)

	if len(apptCustIDs) == 0 {
		c.JSON(http.StatusOK, []storage.Customer{})
		return
	}

	// v4.16 P1.2: has_card=yes 时只返回在本店持 active 卡的顾客
	if hasCard == "yes" {
		var cardCustIDs []string
		storage.DB.WithContext(ctx).
			Table("customer_cards").
			Where("shop_id = ? AND status = ?", shopID, storage.CustomerCardStatusActive).
			Distinct("customer_id").
			Pluck("customer_id", &cardCustIDs)
		if len(cardCustIDs) == 0 {
			c.JSON(http.StatusOK, []storage.Customer{})
			return
		}
		// 求交集（顾客既在本店有预约，又在本店持卡）
		cardSet := make(map[string]bool, len(cardCustIDs))
		for _, id := range cardCustIDs {
			cardSet[id] = true
		}
		filtered := make([]string, 0, len(apptCustIDs))
		for _, id := range apptCustIDs {
			if cardSet[id] {
				filtered = append(filtered, id)
			}
		}
		if len(filtered) == 0 {
			c.JSON(http.StatusOK, []storage.Customer{})
			return
		}
		apptCustIDs = filtered
	}

	q := storage.DB.WithContext(ctx).Model(&storage.Customer{}).
		Where("id IN ?", apptCustIDs)
	if query != "" {
		// 手机号后 4 位也能搜（v4.16 P1.2）：额外 OR 一下 phone 的后 4 位
		like := "%" + query + "%"
		likeTail := query // 已经是模糊匹配，LIKE '%1234%' 能匹到手机尾号 1234
		q = q.Where("name LIKE ? OR phone LIKE ? OR wechat_open_id LIKE ? OR phone LIKE ?",
			like, like, like, "%"+likeTail)
	}
	if tag != "" {
		q = q.Where("tags LIKE ?", "%"+tag+"%")
	}
	var custs []storage.Customer
	// MySQL 不支持 "NULLS LAST" 关键字（v4.9.2 修复：先试错的 fallback 已删，避免日志刷 1064）
	// 用 COALESCE 把 NULL 映射成 '1970-01-01' → DESC 时自然排到最后，等价 NULLS LAST 语义
	if err := q.Order("COALESCE(last_visit_at, '1970-01-01') DESC, total_visits DESC, id ASC").
		Limit(limit).Find(&custs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if custs == nil {
		custs = []storage.Customer{}
	}
	c.JSON(http.StatusOK, custs)
}

// CustomerTagRequest 加/减标签的 body
type CustomerTagRequest struct {
	CustomerID string `json:"customer_id"`
	Tag        string `json:"tag"`
}

// addCustomerTagHandler POST /api/admin/customers/tag
func addCustomerTagHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	var req CustomerTagRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.CustomerID == "" || req.Tag == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "customer_id 和 tag 必填"})
		return
	}
	tag := strings.ToUpper(strings.TrimSpace(req.Tag))
	if !isAllowedTag(tag) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "tag 必须是 VIP / FREQUENT / BLACKLIST / NEW 之一"})
		return
	}
	// 多店隔离：顾客必须在本店有预约
	if !customerInShop(ctx, shopID, req.CustomerID) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "顾客不存在于本店"})
		return
	}
	if err := storage.AddCustomerTag(ctx, req.CustomerID, tag); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// removeCustomerTagHandler DELETE /api/admin/customers/tag
func removeCustomerTagHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	var req CustomerTagRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.CustomerID == "" || req.Tag == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "customer_id 和 tag 必填"})
		return
	}
	tag := strings.ToUpper(strings.TrimSpace(req.Tag))
	if !customerInShop(ctx, shopID, req.CustomerID) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "顾客不存在于本店"})
		return
	}
	if err := storage.RemoveCustomerTag(ctx, req.CustomerID, tag); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// isAllowedTag 校验是否为已知标签枚举
func isAllowedTag(tag string) bool {
	switch tag {
	case storage.TagVIP, storage.TagFrequent, storage.TagBlacklist, storage.TagNew:
		return true
	}
	return false
}

// customerInShop 检查 customer 是否在本店有预约
func customerInShop(ctx context.Context, shopID, customerID string) bool {
	if storage.DB == nil {
		return false
	}
	var n int64
	storage.DB.WithContext(ctx).
		Table("appointments").
		Where("shop_id = ? AND customer_id = ?", shopID, customerID).
		Count(&n)
	return n > 0
}

// ============================================================
// 4) 续费管理（订阅历史）
// ============================================================

// SubscriptionHistoryItem 订阅历史的一项
type SubscriptionHistoryItem struct {
	ID         string     `json:"id"`
	Plan       string     `json:"plan"`
	StartedAt  time.Time  `json:"started_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AutoRenew  bool       `json:"auto_renew"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	IsCurrent  bool       `json:"is_current"` // 是否当前生效
}

// listSubscriptionsHandler GET /api/admin/subscription
//
// 返回：当前订阅 + 历史订阅列表（按 started_at DESC）
func listSubscriptionsHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	var subs []storage.Subscription
	if err := storage.DB.WithContext(ctx).
		Where("shop_id = ?", shopID).
		Order("started_at DESC").
		Find(&subs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	now := time.Now()
	out := make([]SubscriptionHistoryItem, 0, len(subs))
	for _, s := range subs {
		item := SubscriptionHistoryItem{
			ID:          s.ID,
			Plan:        s.Plan,
			StartedAt:   s.StartedAt,
			ExpiresAt:   s.ExpiresAt,
			AutoRenew:   s.AutoRenew,
			CancelledAt: s.CancelledAt,
			CreatedAt:   s.CreatedAt,
			IsCurrent:   s.CancelledAt == nil && s.ExpiresAt.After(now),
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, out)
}

// ============================================================
// 5) 服务目录
// ============================================================

// serviceRequest 服务的统一请求体（create / update 共用）
type serviceRequest struct {
	Name         string `json:"name"`
	EstimatedMin int    `json:"estimated_min"`
	PriceRange   string `json:"price_range"`
	SortOrder    int    `json:"sort_order"`
}

// listServicesHandler GET /api/admin/services?include_inactive=true
//
// v4.9:返回带 shop_name 的服务列表
//   - platform_admin:跨店看所有店的服务（前端按商户分组渲染）
//   - owner / staff:只看本店服务（带本店 shop_name 用于显示）
func listServicesHandler(ctx context.Context, c *app.RequestContext) {
	cl := auth.GetClaims(c)
	if cl == nil {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "未登录"})
		return
	}
	includeInactive := false
	if v, err := strconv.ParseBool(c.Query("include_inactive")); err == nil {
		includeInactive = v
	}

	var (
		svcs []storage.ServiceWithShop
		err  error
	)
	if cl.Role == "platform_admin" {
		// 超管：跨店看所有店
		svcs, err = storage.ListAllServicesWithShopName(ctx, includeInactive)
	} else {
		// 普通 owner/staff：限本店（必须有 shop_id）
		if cl.ShopID == "" {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
			return
		}
		svcs, err = storage.ListServicesByShopWithShopName(ctx, cl.ShopID, includeInactive)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if svcs == nil {
		svcs = []storage.ServiceWithShop{}
	}
	c.JSON(http.StatusOK, svcs)
}

// createServiceHandler POST /api/admin/services
func createServiceHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	var req serviceRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s, err := storage.CreateService(ctx, shopID, req.Name, req.EstimatedMin, req.PriceRange)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrServiceNameTaken):
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		case isServiceValidationErr(err):
			c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	// 如果请求里指定了 sort_order，再覆盖一次（CreateService 自动算的）
	if req.SortOrder != 0 && req.SortOrder != s.SortOrder {
		if err := storage.DB.WithContext(ctx).Model(s).
			Update("sort_order", req.SortOrder).Error; err == nil {
			s.SortOrder = req.SortOrder
		}
	}
	c.JSON(http.StatusOK, s)
}

// updateServiceHandler PUT /api/admin/services/:id
func updateServiceHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	serviceID := c.Param("id")
	if serviceID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "service id required"})
		return
	}
	var req serviceRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s, err := storage.UpdateService(ctx, shopID, serviceID, req.Name, req.EstimatedMin, req.PriceRange, req.SortOrder)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrServiceNotFoundInShop):
			c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, storage.ErrServiceNameTaken):
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		case isServiceValidationErr(err):
			c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, s)
}

// deactivateServiceHandler DELETE /api/admin/services/:id（软下架）
func deactivateServiceHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	serviceID := c.Param("id")
	if serviceID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "service id required"})
		return
	}
	if err := storage.DeactivateService(ctx, shopID, serviceID); err != nil {
		if errors.Is(err, storage.ErrServiceNotFoundInShop) {
			c.JSON(http.StatusNotFound, map[string]string{"error": "服务不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "deactivated"})
}

// activateServiceHandler POST /api/admin/services/:id/activate
func activateServiceHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	serviceID := c.Param("id")
	if serviceID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "service id required"})
		return
	}
	if err := storage.ActivateService(ctx, shopID, serviceID); err != nil {
		if errors.Is(err, storage.ErrServiceNotFoundInShop) {
			c.JSON(http.StatusNotFound, map[string]string{"error": "服务不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "activated"})
}

// isServiceValidationErr 判断 service 输入校验类错误
func isServiceValidationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "不能为空") ||
		strings.Contains(msg, "过长") ||
		strings.Contains(msg, "必须在") ||
		strings.Contains(msg, "无效")
}

// ============================================================
// 6) 周报预览（v4.3 PRD §11.12 — 数据已在 storage，UI 一直缺）
// ============================================================

// getWeeklyReportHandler GET /api/admin/weekly-report?as_of=YYYY-MM-DD
//
// 返回当前 admin 所属店铺的最近一周（[as_of-7d, as_of)）经营报告。
//   - as_of 缺省 = now；非法格式 400
//   - 用 storage.BuildWeeklyUsageReport 复用 D+15 同一套数据组装逻辑
//   - 返回结构与 email 模板渲染的 source 字段一致；前端用同一份数据画卡片/排行/趋势
func getWeeklyReportHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	asOf, err := parseAsOf(c.Query("as_of"))
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	rep, err := storage.BuildWeeklyUsageReport(ctx, shopID, asOf)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rep)
}

// getChainWeeklyReportHandler GET /api/admin/weekly-report/chain?as_of=YYYY-MM-DD
//
// 跨店周报（连锁/平台用）。权限：
//   - 路由层用 auth.RequireRole(RolePlatformAdmin) 强约束
//   - 之前 v4.0 MVP 留的"已登录即可"是权限泄漏——单店 owner 能看全平台周报
//   - 修复后：单店 owner 路由层就 403
func getChainWeeklyReportHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	asOf, err := parseAsOf(c.Query("as_of"))
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	rep, err := storage.BuildChainWeeklyUsageReport(ctx, asOf)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rep)
}

// parseAsOf 解析 as_of query（YYYY-MM-DD），空时 = now，非法时返回 error
func parseAsOf(raw string) (time.Time, error) {
	if raw == "" {
		return time.Now(), nil
	}
	t, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return time.Time{}, errors.New("as_of 格式错误，需 YYYY-MM-DD")
	}
	return t, nil
}

// ============================================================
// 7) Dashboard 告警 + drill-down（v4.5 B3 主动告警 + B1 操作闭环）
// ============================================================

// AlertSeverity 告警级别（前端按颜色渲染）
type AlertSeverity string

const (
	AlertSeverityInfo     AlertSeverity = "info"     // 灰，提示
	AlertSeverityWarning  AlertSeverity = "warning"  // 黄，建议关注
	AlertSeverityCritical AlertSeverity = "critical" // 红，需立即处理
)

// Alert 单条告警
type Alert struct {
	Severity    AlertSeverity `json:"severity"`
	Code        string        `json:"code"`         // 稳定 code，前端可作 key 渲染
	Title       string        `json:"title"`        // 一句话标题
	Description string        `json:"description"`  // 详细说明
	Action      *AlertAction  `json:"action,omitempty"` // 跳转目标（dashboard 卡片 → 详情页）
}

// AlertAction 跳转目标（drill-down）
type AlertAction struct {
	View  string `json:"view"`            // 前端要切换的 view
	Query string `json:"query,omitempty"` // query 字符串
}

// AlertResponse 告警响应
type AlertResponse struct {
	Alerts       []Alert `json:"alerts"`
	GeneratedAt  time.Time `json:"generated_at"`
	AlertCount   int    `json:"alert_count"`
	HasCritical  bool   `json:"has_critical"`
	HasWarning   bool   `json:"has_warning"`
}

// getAlertsHandler GET /api/admin/alerts
//
// 返回当前店铺需要关注的告警列表，按 severity 排序（critical > warning > info）。
// 计算规则：
//   - subscription_expiring_7d  订阅 7 天内到期
//   - noshow_rate_high_weekly    本周爽约率 > 15%
//   - completion_rate_low_weekly 本周完成率 < 50%（预约数 ≥ 10）
//   - leave_heavy                某理发师本周 ≥ 3 次请假
//   - holiday_upcoming            3 天内有节假日
//   - handoff_pending             今天有 ≥ 1 个转人工未处理
func getAlertsHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	alerts := buildAlerts(ctx, shopID)

	// 按严重度排序：critical > warning > info
	sort.SliceStable(alerts, func(i, j int) bool {
		return severityRank(alerts[i].Severity) > severityRank(alerts[j].Severity)
	})

	resp := AlertResponse{
		Alerts:      alerts,
		GeneratedAt: time.Now(),
		AlertCount:  len(alerts),
	}
	for _, a := range alerts {
		if a.Severity == AlertSeverityCritical {
			resp.HasCritical = true
		}
		if a.Severity == AlertSeverityWarning {
			resp.HasWarning = true
		}
	}
	c.JSON(http.StatusOK, resp)
}

func severityRank(s AlertSeverity) int {
	switch s {
	case AlertSeverityCritical:
		return 3
	case AlertSeverityWarning:
		return 2
	default:
		return 1
	}
}

// buildAlerts 计算所有告警
func buildAlerts(ctx context.Context, shopID string) []Alert {
	var alerts []Alert

	// 1) 订阅快到期
	if a := checkSubscriptionExpiring(ctx, shopID); a != nil {
		alerts = append(alerts, *a)
	}

	// 2) 本周爽约率
	if a := checkNoShowRateWeekly(ctx, shopID); a != nil {
		alerts = append(alerts, *a)
	}

	// 3) 本周完成率低
	if a := checkCompletionRateWeekly(ctx, shopID); a != nil {
		alerts = append(alerts, *a)
	}

	// 4) 请假密集
	if a := checkLeaveHeavy(ctx, shopID); a != nil {
		alerts = append(alerts, *a)
	}

	// 5) 节假日临近
	if a := checkHolidayUpcoming(ctx, shopID); a != nil {
		alerts = append(alerts, *a)
	}

	// 6) 转人工待处理
	if a := checkHandoffPending(ctx, shopID); a != nil {
		alerts = append(alerts, *a)
	}

	return alerts
}

// checkSubscriptionExpiring 订阅 7 天内到期
func checkSubscriptionExpiring(ctx context.Context, shopID string) *Alert {
	var sub storage.Subscription
	if err := storage.DB.WithContext(ctx).
		Where("shop_id = ? AND cancelled_at IS NULL", shopID).
		Order("expires_at DESC").
		First(&sub).Error; err != nil {
		return nil // 没有有效订阅，不告警
	}
	daysLeft := int(time.Until(sub.ExpiresAt).Hours() / 24)
	if daysLeft >= 0 && daysLeft <= 7 {
		severity := AlertSeverityWarning
		if daysLeft <= 3 {
			severity = AlertSeverityCritical
		}
		return &Alert{
			Severity: severity,
			Code:     "subscription_expiring_7d",
			Title:    fmt.Sprintf("订阅还有 %d 天到期", daysLeft),
			Description: fmt.Sprintf("当前 %s 套餐到期日 %s，过期后将无法使用 AI 预约助手。",
				sub.Plan, sub.ExpiresAt.Format("2006-01-02")),
			Action: &AlertAction{View: "subscription"},
		}
	}
	return nil
}

// checkNoShowRateWeekly 本周爽约率 > 15%
func checkNoShowRateWeekly(ctx context.Context, shopID string) *Alert {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	weekStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -7)
	summary := summarizeRange(ctx, shopID, weekStart, now.Add(24*time.Hour))
	closed := summary.NoShow + summary.Completed
	if closed < 5 {
		return nil // 样本太少，不告警
	}
	if summary.NoShowRate > 0.15 {
		return &Alert{
			Severity:    AlertSeverityWarning,
			Code:        "noshow_rate_high_weekly",
			Title:       fmt.Sprintf("本周爽约率 %.1f%%（%d/%d）", summary.NoShowRate*100, summary.NoShow, closed),
			Description: "高于 15% 警戒线。建议：复盘爽约顾客 + D-1 微信提醒强化。",
			Action:      &AlertAction{View: "customers", Query: "tag=BLACKLIST"},
		}
	}
	return nil
}

// checkCompletionRateWeekly 本周完成率 < 50%（样本 ≥ 10）
func checkCompletionRateWeekly(ctx context.Context, shopID string) *Alert {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	weekStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -7)
	summary := summarizeRange(ctx, shopID, weekStart, now.Add(24*time.Hour))
	closed := summary.NoShow + summary.Completed
	if closed < 10 {
		return nil
	}
	if summary.CompleteRate < 0.5 {
		return &Alert{
			Severity:    AlertSeverityWarning,
			Code:        "completion_rate_low_weekly",
			Title:       fmt.Sprintf("本周完成率仅 %.1f%%（%d/%d）", summary.CompleteRate*100, summary.Completed, closed),
			Description: "完成率偏低，可能与爽约 / 取消过多有关。",
			Action:      &AlertAction{View: "appointments"},
		}
	}
	return nil
}

// checkLeaveHeavy 某理发师本周 ≥ 3 次请假（不重叠合并）
func checkLeaveHeavy(ctx context.Context, shopID string) *Alert {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	weekStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -7)

	type row struct {
		BarberID   string
		BarberName string
		Count      int
	}
	var rows []row
	if err := storage.DB.WithContext(ctx).
		Table("barber_leaves").
		Select("barber_id, barber_name, COUNT(*) as count").
		Where("shop_id = ? AND status = ? AND start_at >= ?", shopID, storage.LeaveStatusActive, weekStart).
		Group("barber_id, barber_name").
		Having("count >= ?", 3).
		Order("count DESC").
		Limit(1).
		Scan(&rows).Error; err != nil || len(rows) == 0 {
		return nil
	}
	r := rows[0]
	return &Alert{
		Severity:    AlertSeverityInfo,
		Code:        "leave_heavy",
		Title:       fmt.Sprintf("%s 师傅本周已请假 %d 次", r.BarberName, r.Count),
		Description: "频繁请假可能影响顾客体验，建议确认下师傅状态或考虑改派。",
		Action:      &AlertAction{View: "leaves"},
	}
}

// checkHolidayUpcoming 3 天内有节假日
func checkHolidayUpcoming(ctx context.Context, shopID string) *Alert {
	shop, err := storage.GetShopByID(ctx, shopID)
	if err != nil || shop.Holidays == "" {
		return nil
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	today := time.Now().In(loc)
	todayStart := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)

	for _, d := range strings.Split(shop.Holidays, ",") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		hDate, err := time.ParseInLocation("2006-01-02", d, loc)
		if err != nil {
			continue
		}
		daysAway := int(hDate.Sub(todayStart).Hours() / 24)
		if daysAway >= 0 && daysAway <= 3 {
			label := "今天"
			if daysAway == 1 {
				label = "明天"
			} else if daysAway == 2 {
				label = "后天"
			} else if daysAway == 3 {
				label = "大后天"
			}
			return &Alert{
				Severity:    AlertSeverityInfo,
				Code:        "holiday_upcoming",
				Title:       fmt.Sprintf("%s是休息日（%s）", label, d),
				Description: "记得提前告知顾客、暂停提醒推送。",
				Action:      &AlertAction{View: "shop"},
			}
		}
	}
	return nil
}

// checkHandoffPending 今天有 ≥ 1 个转人工未处理
func checkHandoffPending(ctx context.Context, shopID string) *Alert {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	var n int64
	storage.DB.WithContext(ctx).
		Table("event_logs").
		Where("shop_id = ? AND event_type = ? AND created_at >= ?", shopID, storage.EventHandoffToHuman, todayStart).
		Count(&n)
	if n == 0 {
		return nil
	}
	return &Alert{
		Severity:    AlertSeverityWarning,
		Code:        "handoff_pending",
		Title:       fmt.Sprintf("今天有 %d 个转人工待处理", n),
		Description: "Agent 把顾客转给了你，记得尽快联系避免顾客流失。",
		Action:      &AlertAction{View: "handoffs"},
	}
}
