package api

// admin_cards.go —— 储值 / 次卡模块的 admin 后台 endpoint（v4.15）
//
// Endpoints：
//   GET    /api/admin/cards                     列本店卡产品
//   POST   /api/admin/cards                     建卡
//   PUT    /api/admin/cards/:id                 改卡
//   POST   /api/admin/cards/:id/archive         下架
//   POST   /api/admin/cards/:id/activate        上架
//   GET    /api/admin/cards/sold                列本店所有顾客卡（管理视图）
//   GET    /api/admin/customers/:id/cards       列某顾客的所有卡
//   POST   /api/admin/customers/:id/cards/sell  售卡给顾客
//   POST   /api/admin/customer-cards/:id/consume     扣减
//   POST   /api/admin/customer-cards/:id/adjust      手动调账（reason 必填）
//   GET    /api/admin/customer-cards/:id/transactions 流水
//
// 鉴权：
//   - perm：view:cards / manage:cards
//   - feature gate：FeatureCardManagement（basic 403，pro+ 允许）
//
// 设计要点：
//   - 所有 handler 先 RequirePerm 再 RequireCardFeature（避免 basic 店主拿到 perm 但被 feature 拦）
//   - 调账 reason 必填在 storage 层校验，handler 只是透传；返回 400 + 中文提示

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

// ---- helpers ----

// requireCardFeature feature gate: 当前 shop 必须在 pro 及以上
//
// 失败返 false 并直接写 403 + feature_required / current_plan（前端用来提示升级）
func requireCardFeature(ctx context.Context, c *app.RequestContext, shopID string) bool {
	shop, err := storage.GetShopByID(ctx, shopID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "读 shop 失败: " + err.Error()})
		return false
	}
	currentPlan := shop.Plan
	if !storage.IsValidPlanID(currentPlan) {
		currentPlan = storage.DefaultPlanID
	}
	if !storage.HasFeature(currentPlan, storage.FeatureCardManagement) {
		c.JSON(http.StatusForbidden, map[string]string{
			"error":            "当前 plan 不支持储值/次卡功能，请升级到专业版或以上",
			"feature_required": storage.FeatureCardManagement,
			"current_plan":     currentPlan,
		})
		return false
	}
	return true
}

// operatorFromClaims 取当前操作人 (admin_id + username)
//
// admin 找不到 username 时 fallback 为 "admin#<id>"（不影响功能）
func operatorFromClaims(ctx context.Context, c *app.RequestContext) (uint64, string) {
	cl := auth.GetClaims(c)
	if cl == nil || cl.AdminID == 0 {
		return 0, "unknown"
	}
	if storage.DB == nil {
		return cl.AdminID, ""
	}
	if a, err := storage.FindShopAdminByID(ctx, cl.AdminID); err == nil && a != nil {
		return a.ID, a.Username
	}
	return cl.AdminID, ""
}

// ---- request bodies ----

// CreateCardRequest POST /api/admin/cards
type CreateCardRequest struct {
	Name           string `json:"name"`
	Type           string `json:"type"` // stored_value / count
	Note           string `json:"note"`
	PriceCents     int    `json:"price_cents"`
	FaceValueCents int    `json:"face_value_cents"` // stored_value
	BonusCents     int    `json:"bonus_cents"`      // stored_value
	ServiceID      string `json:"service_id"`       // count
	ServiceName    string `json:"service_name"`     // count（可选，没传时 storage 从 service 读）
	TotalCount     int    `json:"total_count"`      // count
	ValidDays      int    `json:"valid_days"`       // 0 = 永久
}

// UpdateCardRequest PUT /api/admin/cards/:id
//
// 指针字段：nil = 不改
type UpdateCardRequest struct {
	Name           *string `json:"name,omitempty"`
	Note           *string `json:"note,omitempty"`
	PriceCents     *int    `json:"price_cents,omitempty"`
	FaceValueCents *int    `json:"face_value_cents,omitempty"`
	BonusCents     *int    `json:"bonus_cents,omitempty"`
	TotalCount     *int    `json:"total_count,omitempty"`
	ValidDays      *int    `json:"valid_days,omitempty"`
}

// SellCardRequest POST /api/admin/customers/:id/cards/sell
type SellCardRequest struct {
	CardID string `json:"card_id"`
	Note   string `json:"note"`
}

// ConsumeCardRequest POST /api/admin/customer-cards/:id/consume
type ConsumeCardRequest struct {
	// 储值必填；次卡忽略（永远扣 1 次）
	AmountCents   int    `json:"amount_cents"`
	Reason        string `json:"reason"`
	AppointmentID string `json:"appointment_id"`
}

// AdjustCardRequest POST /api/admin/customer-cards/:id/adjust
type AdjustCardRequest struct {
	// "up" 调增 / "down" 调减
	Direction   string `json:"direction"`
	AmountCents int    `json:"amount_cents"`
	Reason      string `json:"reason"`
}

// ---- Card 产品 endpoints ----

// listCardsHandler GET /api/admin/cards
//
// perm: view:cards
// feature: card_management
// query: include_archived=true/false（默认 false）
func listCardsHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if !requireCardFeature(ctx, c, shopID) {
		return
	}
	includeArchived := c.Query("include_archived") == "true"
	cards, err := storage.ListCardsByShop(ctx, shopID, includeArchived)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if cards == nil {
		cards = []storage.Card{}
	}
	c.JSON(http.StatusOK, cards)
}

// createCardHandler POST /api/admin/cards
//
// perm: manage:cards
// feature: card_management
func createCardHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if !requireCardFeature(ctx, c, shopID) {
		return
	}
	var req CreateCardRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	card, err := storage.CreateCard(ctx, storage.CreateCardParams{
		ShopID:         shopID,
		Name:           req.Name,
		Type:           req.Type,
		Note:           req.Note,
		PriceCents:     req.PriceCents,
		FaceValueCents: req.FaceValueCents,
		BonusCents:     req.BonusCents,
		ServiceID:      req.ServiceID,
		ServiceName:    req.ServiceName,
		TotalCount:     req.TotalCount,
		ValidDays:      req.ValidDays,
	})
	if err != nil {
		if errors.Is(err, storage.ErrCardNameTaken) {
			c.JSON(http.StatusConflict, map[string]string{"error": "同名卡已存在：" + req.Name})
			return
		}
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, card)
}

// updateCardHandler PUT /api/admin/cards/:id
//
// perm: manage:cards
// feature: card_management
func updateCardHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if !requireCardFeature(ctx, c, shopID) {
		return
	}
	cardID := c.Param("id")
	if cardID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "card id 必填"})
		return
	}
	var req UpdateCardRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	card, err := storage.UpdateCard(ctx, shopID, cardID, storage.UpdateCardParams{
		Name:           req.Name,
		Note:           req.Note,
		PriceCents:     req.PriceCents,
		FaceValueCents: req.FaceValueCents,
		BonusCents:     req.BonusCents,
		TotalCount:     req.TotalCount,
		ValidDays:      req.ValidDays,
	})
	if err != nil {
		if errors.Is(err, storage.ErrCardNotFoundInShop) {
			c.JSON(http.StatusNotFound, map[string]string{"error": "卡产品不存在"})
			return
		}
		if errors.Is(err, storage.ErrCardNameTaken) {
			c.JSON(http.StatusConflict, map[string]string{"error": "同名卡已存在"})
			return
		}
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, card)
}

// archiveCardHandler POST /api/admin/cards/:id/archive
func archiveCardHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if !requireCardFeature(ctx, c, shopID) {
		return
	}
	cardID := c.Param("id")
	if cardID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "card id 必填"})
		return
	}
	if err := storage.ArchiveCard(ctx, shopID, cardID); err != nil {
		if errors.Is(err, storage.ErrCardNotFoundInShop) {
			c.JSON(http.StatusNotFound, map[string]string{"error": "卡产品不存在"})
			return
		}
		if errors.Is(err, storage.ErrCardHasActiveInstances) {
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// activateCardHandler POST /api/admin/cards/:id/activate
func activateCardHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if !requireCardFeature(ctx, c, shopID) {
		return
	}
	cardID := c.Param("id")
	if cardID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "card id 必填"})
		return
	}
	if err := storage.ActivateCard(ctx, shopID, cardID); err != nil {
		if errors.Is(err, storage.ErrCardNotFoundInShop) {
			c.JSON(http.StatusNotFound, map[string]string{"error": "卡产品不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// ---- Customer card endpoints ----

// listSoldCardsHandler GET /api/admin/cards/sold
//
// 列本店所有顾客卡（管理视图：含顾客姓名/手机号）
// query: limit (default 200, max 500)
func listSoldCardsHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if !requireCardFeature(ctx, c, shopID) {
		return
	}
	limit := 200
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	cards, err := storage.ListShopCustomerCards(ctx, shopID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if cards == nil {
		cards = []storage.CustomerCard{}
	}
	c.JSON(http.StatusOK, cards)
}

// listCustomerCardsHandler GET /api/admin/customers/:id/cards
//
// 列某顾客在本店的所有卡
// query: status=active|depleted|archived|expired（空 = 全部）
func listCustomerCardsHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if !requireCardFeature(ctx, c, shopID) {
		return
	}
	customerID := c.Param("id")
	if customerID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "customer id 必填"})
		return
	}
	status := c.Query("status")
	cards, err := storage.ListCustomerCards(ctx, shopID, customerID, status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if cards == nil {
		cards = []storage.CustomerCard{}
	}
	c.JSON(http.StatusOK, cards)
}

// sellCardHandler POST /api/admin/customers/:id/cards/sell
//
// 售卡给顾客；写一条 recharge 流水
func sellCardHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if !requireCardFeature(ctx, c, shopID) {
		return
	}
	customerID := c.Param("id")
	if customerID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "customer id 必填"})
		return
	}
	var req SellCardRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.CardID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "card_id 必填"})
		return
	}
	opID, opName := operatorFromClaims(ctx, c)
	cc, err := storage.SellCardToCustomer(ctx, storage.SellCardToCustomerParams{
		ShopID:       shopID,
		CustomerID:   customerID,
		CardID:       req.CardID,
		OperatorID:   opID,
		OperatorName: opName,
		Note:         req.Note,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cc)
}

// consumeCardHandler POST /api/admin/customer-cards/:id/consume
//
// 扣减。储值卡 amount_cents 必填且 <= 当前余额；次卡忽略（永远扣 1 次）
func consumeCardHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if !requireCardFeature(ctx, c, shopID) {
		return
	}
	ccID := c.Param("id")
	if ccID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "customer_card id 必填"})
		return
	}
	var req ConsumeCardRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	opID, opName := operatorFromClaims(ctx, c)
	cc, err := storage.ConsumeCustomerCard(ctx, storage.ConsumeCustomerCardParams{
		ShopID:         shopID,
		CustomerCardID: ccID,
		AmountCents:    req.AmountCents,
		Reason:         req.Reason,
		AppointmentID:  req.AppointmentID,
		OperatorID:     opID,
		OperatorName:   opName,
	})
	if err != nil {
		if errors.Is(err, storage.ErrCustomerCardNotFoundInShop) {
			c.JSON(http.StatusNotFound, map[string]string{"error": "顾客卡不存在"})
			return
		}
		if errors.Is(err, storage.ErrCustomerCardNotActive) ||
			errors.Is(err, storage.ErrCustomerCardExpired) {
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		if errors.Is(err, storage.ErrInsufficientBalance) {
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cc)
}

// adjustCardHandler POST /api/admin/customer-cards/:id/adjust
//
// 手动调账。reason 必填（v4.15 追溯要求）
func adjustCardHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if !requireCardFeature(ctx, c, shopID) {
		return
	}
	ccID := c.Param("id")
	if ccID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "customer_card id 必填"})
		return
	}
	var req AdjustCardRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	opID, opName := operatorFromClaims(ctx, c)
	cc, err := storage.AdjustCustomerCard(ctx, storage.AdjustCustomerCardParams{
		ShopID:         shopID,
		CustomerCardID: ccID,
		Direction:      storage.AdjustDirection(req.Direction),
		AmountCents:    req.AmountCents,
		Reason:         req.Reason,
		OperatorID:     opID,
		OperatorName:   opName,
	})
	if err != nil {
		if errors.Is(err, storage.ErrCustomerCardNotFoundInShop) {
			c.JSON(http.StatusNotFound, map[string]string{"error": "顾客卡不存在"})
			return
		}
		if errors.Is(err, storage.ErrReasonRequired) ||
			errors.Is(err, storage.ErrInsufficientBalance) ||
			errors.Is(err, storage.ErrCustomerCardExpired) {
			c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cc)
}

// listCardTransactionsHandler GET /api/admin/customer-cards/:id/transactions
//
// 列某张顾客卡的流水（按时间倒序）
// query: limit (default 200, max 500)
func listCardTransactionsHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if !requireCardFeature(ctx, c, shopID) {
		return
	}
	ccID := c.Param("id")
	if ccID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "customer_card id 必填"})
		return
	}
	limit := 200
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	// 先校验这张卡属于本店（防泄漏）
	if _, err := storage.GetCustomerCardInShop(ctx, shopID, ccID); err != nil {
		if errors.Is(err, storage.ErrCustomerCardNotFoundInShop) {
			c.JSON(http.StatusNotFound, map[string]string{"error": "顾客卡不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	txs, err := storage.ListCardTransactions(ctx, shopID, ccID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if txs == nil {
		txs = []storage.CardTransaction{}
	}
	c.JSON(http.StatusOK, txs)
}