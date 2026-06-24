package api

// shops.go —— 多店管理（v4.12.1 feature gate 实战 #2）
//
// GET  /api/admin/shops        列出当前 shop group（主店 + 分店）
// POST /api/admin/shops        在主店下建一个分店（需 multi_store feature）
// PUT  /api/admin/shops/:id    改分店信息（v4.12.1 暂不实现——留 v4.13）
// DEL  /api/admin/shops/:id    删分店（v4.12.1 暂不实现——留 v4.13）
//
// 设计：
//   - perm: view:plan（owner-only，staff 不该自己建分店）
//   - plan: active（frozen 店走 middleware 402）
//   - feature: multi_store（basic 403，flagship+ 允许）
//   - plan limit: CheckPlanLimit("shops", currentCount, +1)
//   - cross-shop isolation: shopFromClaims(c) 拿当前店 → 算 group → 操作

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yuterigele/openbook/storage"
)

// ShopListItem 单个 shop 的列表项
type ShopListItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Address      string `json:"address"`
	ParentShopID string `json:"parent_shop_id,omitempty"` // 空 = 主店
	Plan         string `json:"plan"`
	IsCurrent    bool   `json:"is_current"` // 当前 session 在这家
}

// ShopListResponse GET /api/admin/shops
type ShopListResponse struct {
	CurrentPlan  string          `json:"current_plan"`
	MaxShops     int             `json:"max_shops"`     // 当前 plan 限额（-1=不限）
	CurrentCount int             `json:"current_count"` // group 内 shop 数
	Shops        []ShopListItem  `json:"shops"`
	Feature      string          `json:"feature"`       // "multi_store"（plan 信息）
}

// listShopsHandler GET /api/admin/shops
//
// perm: view:plan
func listShopsHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}

	shop, err := storage.GetShopByID(ctx, shopID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "读 shop 失败: " + err.Error()})
		return
	}
	currentPlan := shop.Plan
	if !storage.IsValidPlanID(currentPlan) {
		currentPlan = storage.DefaultPlanID
	}
	maxShops := storage.PlanLimitInt(currentPlan, "shops")

	shops, err := storage.ListShopsInGroup(ctx, shopID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "列分店失败: " + err.Error()})
		return
	}

	items := make([]ShopListItem, 0, len(shops))
	for _, s := range shops {
		items = append(items, ShopListItem{
			ID:           s.ID,
			Name:         s.Name,
			Address:      s.Address,
			ParentShopID: s.ParentShopID,
			Plan:         s.Plan,
			IsCurrent:    s.ID == shopID,
		})
	}

	c.JSON(http.StatusOK, ShopListResponse{
		CurrentPlan:  currentPlan,
		MaxShops:     maxShops,
		CurrentCount: len(items),
		Shops:        items,
		Feature:      storage.FeatureMultiStore,
	})
}

// CreateShopRequest POST /api/admin/shops
type CreateShopRequest struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// createShopHandler POST /api/admin/shops
//
// perm: view:plan
// feature: multi_store
// plan: active
//
// 当前店主必须是主店（不能从分店建分店）
func createShopHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}

	var req CreateShopRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "分店名不能为空"})
		return
	}
	if len(req.Name) > 128 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "分店名最长 128 字"})
		return
	}

	// 1) 当前店必须为主店（不能从分店建分店）
	currentShop, err := storage.GetShopByID(ctx, shopID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "读 shop 失败: " + err.Error()})
		return
	}
	if currentShop.ParentShopID != "" {
		c.JSON(http.StatusForbidden, map[string]string{
			"error": "分店不能创建新的分店（请用主店账号）",
		})
		return
	}

	// 2) feature gate: basic 没 multi_store
	currentPlan := currentShop.Plan
	if !storage.IsValidPlanID(currentPlan) {
		currentPlan = storage.DefaultPlanID
	}
	if !storage.HasFeature(currentPlan, storage.FeatureMultiStore) {
		c.JSON(http.StatusForbidden, map[string]string{
			"error":            "当前 plan 不支持多店，请升级到旗舰或以上版本",
			"feature_required": storage.FeatureMultiStore,
			"current_plan":     currentPlan,
		})
		return
	}

	// 3) plan limit gate: CountShopsInGroup + 1 vs plan limit
	currentCount, err := storage.CountShopsInGroup(ctx, shopID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "数 group 失败: " + err.Error()})
		return
	}
	if err := storage.CheckPlanLimit(ctx, shopID, "shops", currentCount, 1); err != nil {
		if storage.IsPlanLimitExceeded(err) {
			c.JSON(http.StatusPaymentRequired, map[string]string{
				"error":            err.Error(),
				"current_count":    strconv.Itoa(currentCount),
				"plan":             currentPlan,
				"resource":         "shops",
				"limit_hint":       "升级 plan 或删除现有分店",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "plan 检查失败: " + err.Error()})
		return
	}

	// 4) 建分店
	newShop, err := storage.CreateSubsidiaryShop(ctx, shopID, req.Name, req.Address)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "建分店失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"shop":    newShop,
		"message": "分店创建成功，请用「成员管理」为分店建 owner 账号",
	})
}