package api

// admin_features.go
//
// v4.4 补全商户后台 — 5 个新模块的 handler：
//
//   1) 店铺设置   GET /api/admin/shop          +  PUT /api/admin/shop
//   2) 转人工列表 GET /api/admin/handoffs      （从 event_logs 筛 handoff_to_human）
//   3) 顾客管理   GET /api/admin/customers     +  POST/DELETE 标签
//   4) 续费管理   GET /api/admin/subscription  （当前订阅 + 历史列表）
//                  POST /api/admin/subscription/renew  已有，路径沿用 api.go 的
//   5) 服务目录   GET/POST/PUT/DELETE /api/admin/services
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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
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

	q := storage.DB.WithContext(ctx).Model(&storage.Customer{}).
		Where("id IN ?", apptCustIDs)
	if query != "" {
		like := "%" + query + "%"
		q = q.Where("name LIKE ? OR phone LIKE ? OR wechat_open_id LIKE ?", like, like, like)
	}
	if tag != "" {
		q = q.Where("tags LIKE ?", "%"+tag+"%")
	}
	var custs []storage.Customer
	if err := q.Order("last_visit_at DESC NULLS LAST, total_visits DESC, id ASC").
		Limit(limit).Find(&custs).Error; err != nil {
		// 兼容 MySQL（无 NULLS LAST 关键字）：重试不写 NULLS LAST
		q2 := storage.DB.WithContext(ctx).Model(&storage.Customer{}).
			Where("id IN ?", apptCustIDs)
		if query != "" {
			like := "%" + query + "%"
			q2 = q2.Where("name LIKE ? OR phone LIKE ? OR wechat_open_id LIKE ?", like, like, like)
		}
		if tag != "" {
			q2 = q2.Where("tags LIKE ?", "%"+tag+"%")
		}
		if err2 := q2.Order("total_visits DESC, id ASC").Limit(limit).Find(&custs).Error; err2 != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": err2.Error()})
			return
		}
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
func listServicesHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	includeInactive := false
	if v, err := strconv.ParseBool(c.Query("include_inactive")); err == nil {
		includeInactive = v
	}
	svcs, err := storage.ListServicesByShop(ctx, shopID, includeInactive)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if svcs == nil {
		svcs = []storage.Service{}
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
