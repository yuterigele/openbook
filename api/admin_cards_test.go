package api

// admin_cards_test.go —— 卡管理 endpoint 测试（v4.15）
//
// 覆盖重点：
//   - feature gate：basic 计划 403，pro 计划 200
//   - 售卡 → 扣减 → 调账全链路
//   - 调账 reason 必填
//   - 跨店隔离

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

// makeShopWithPlan 建店 + 设 plan + 建 owner admin（owner 默认有 view:cards / manage:cards）
func makeShopWithPlan(t *testing.T, id, plan string) {
	t.Helper()
	shop := &storage.Shop{
		ID: id, Name: "shop-" + id, Plan: plan,
		ExpiresAt: time.Now().AddDate(1, 0, 0),
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := storage.DB.Create(shop).Error; err != nil {
		t.Fatalf("create shop: %v", err)
	}
	storage.MakeAdminWithRole(t, id, "owner-"+id, storage.RoleOwner)
}

// ---- feature gate ----

func TestListCards_BasicPlan_403(t *testing.T) {
	setupAPITestDB(t)
	makeShopWithPlan(t, "shop-basic", storage.PlanBasic)
	ctx := newAPIContext(t, "GET", "/api/admin/cards", nil,
		withClaims(&auth.Claims{AdminID: 1, ShopID: "shop-basic", Role: storage.RoleOwner}))
	status, body := runHandler(t, listCardsHandler, ctx)
	if status != http.StatusForbidden {
		t.Errorf("basic plan 应 403，得到 %d", status)
	}
	if !strings.Contains(body, storage.FeatureCardManagement) {
		t.Errorf("应含 feature_required=%s，实际：%s", storage.FeatureCardManagement, body)
	}
}

func TestListCards_ProPlan_200(t *testing.T) {
	setupAPITestDB(t)
	makeShopWithPlan(t, "shop-pro", storage.PlanPro)
	ctx := newAPIContext(t, "GET", "/api/admin/cards", nil,
		withClaims(&auth.Claims{AdminID: 1, ShopID: "shop-pro", Role: storage.RoleOwner}))
	status, _ := runHandler(t, listCardsHandler, ctx)
	if status != http.StatusOK {
		t.Errorf("pro plan 应 200，得到 %d", status)
	}
}

// ---- 售卡 → 扣减 → 调账全链路 ----

func TestCardEndToEnd_SellConsumeAdjust(t *testing.T) {
	setupAPITestDB(t)
	makeShopWithPlan(t, "shop-e2e", storage.PlanPro)
	cust := storage.MakeCustomer(t, "张三", 0, 0)
	claims := &auth.Claims{AdminID: 1, ShopID: "shop-e2e", Role: storage.RoleOwner}

	// 1) 建卡（2000送200）
	createBody := []byte(`{"name":"2000送200","type":"stored_value","price_cents":200000,"face_value_cents":200000,"bonus_cents":20000,"valid_days":365}`)
	ctx := newAPIContext(t, "POST", "/api/admin/cards", createBody, withClaims(claims))
	status, body := runHandler(t, createCardHandler, ctx)
	if status != http.StatusOK {
		t.Fatalf("create card: %d %s", status, body)
	}
	var card storage.Card
	if err := json.Unmarshal([]byte(body), &card); err != nil {
		t.Fatalf("parse: %v", err)
	}

	// 2) 售卡
	sellBody := []byte(`{"card_id":"` + card.ID + `","note":"测试售卡"}`)
	ctx = newAPIContext(t, "POST", "/api/admin/customers/"+cust.ID+"/cards/sell", sellBody,
		withClaims(claims), withPathParam("id", cust.ID))
	status, body = runHandler(t, sellCardHandler, ctx)
	if status != http.StatusOK {
		t.Fatalf("sell: %d %s", status, body)
	}
	var cc storage.CustomerCard
	if err := json.Unmarshal([]byte(body), &cc); err != nil {
		t.Fatalf("parse cc: %v body=%s", err, body)
	}
	if cc.BalanceCents != 220000 {
		t.Errorf("初始余额应为 220000（2000+200），得到 %d", cc.BalanceCents)
	}

	// 3) 扣减 50000（500 元）
	consumeBody := []byte(`{"amount_cents":50000,"reason":"剪发测试"}`)
	ctx = newAPIContext(t, "POST", "/api/admin/customer-cards/"+cc.ID+"/consume", consumeBody,
		withClaims(claims), withPathParam("id", cc.ID))
	status, body = runHandler(t, consumeCardHandler, ctx)
	if status != http.StatusOK {
		t.Fatalf("consume: %d %s", status, body)
	}
	var cc2 storage.CustomerCard
	if err := json.Unmarshal([]byte(body), &cc2); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cc2.BalanceCents != 170000 {
		t.Errorf("扣后余额应为 170000，得到 %d", cc2.BalanceCents)
	}

	// 4) 调账 — 空 reason 应被拒
	adjustBody := []byte(`{"direction":"up","amount_cents":10000,"reason":""}`)
	ctx = newAPIContext(t, "POST", "/api/admin/customer-cards/"+cc.ID+"/adjust", adjustBody,
		withClaims(claims), withPathParam("id", cc.ID))
	status, body = runHandler(t, adjustCardHandler, ctx)
	if status != http.StatusBadRequest {
		t.Errorf("空 reason 应 400，得到 %d body=%s", status, body)
	}

	// 5) 调账 — 填了 reason 应成功
	adjustBody = []byte(`{"direction":"up","amount_cents":10000,"reason":"补偿上次多扣"}`)
	ctx = newAPIContext(t, "POST", "/api/admin/customer-cards/"+cc.ID+"/adjust", adjustBody,
		withClaims(claims), withPathParam("id", cc.ID))
	status, body = runHandler(t, adjustCardHandler, ctx)
	if status != http.StatusOK {
		t.Fatalf("adjust: %d %s", status, body)
	}
	var cc3 storage.CustomerCard
	if err := json.Unmarshal([]byte(body), &cc3); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cc3.BalanceCents != 180000 {
		t.Errorf("调增后余额应为 180000，得到 %d", cc3.BalanceCents)
	}

	// 6) 查流水 —— 应有 3 条（recharge + consume + adjust_up）
	ctx = newAPIContext(t, "GET", "/api/admin/customer-cards/"+cc.ID+"/transactions", nil,
		withClaims(claims), withPathParam("id", cc.ID))
	status, body = runHandler(t, listCardTransactionsHandler, ctx)
	if status != http.StatusOK {
		t.Fatalf("list tx: %d %s", status, body)
	}
	var txs []storage.CardTransaction
	if err := json.Unmarshal([]byte(body), &txs); err != nil {
		t.Fatalf("parse txs: %v", err)
	}
	if len(txs) != 3 {
		t.Errorf("应有 3 条流水，得到 %d", len(txs))
	}
}

// ---- 跨店隔离 ----

func TestCard_CrossShopIsolation(t *testing.T) {
	setupAPITestDB(t)
	makeShopWithPlan(t, "shop-1", storage.PlanPro)
	makeShopWithPlan(t, "shop-2", storage.PlanPro)
	cust := storage.MakeCustomer(t, "张三", 0, 0)
	claims1 := &auth.Claims{AdminID: 1, ShopID: "shop-1", Role: storage.RoleOwner}
	claims2 := &auth.Claims{AdminID: 2, ShopID: "shop-2", Role: storage.RoleOwner}

	// shop1 建卡 + 售卡
	createBody := []byte(`{"name":"shop1卡","type":"stored_value","price_cents":100000,"face_value_cents":100000,"bonus_cents":0}`)
	ctx := newAPIContext(t, "POST", "/api/admin/cards", createBody, withClaims(claims1))
	status, body := runHandler(t, createCardHandler, ctx)
	if status != http.StatusOK {
		t.Fatalf("create: %d %s", status, body)
	}
	var card storage.Card
	json.Unmarshal([]byte(body), &card)

	sellBody := []byte(`{"card_id":"` + card.ID + `"}`)
	ctx = newAPIContext(t, "POST", "/api/admin/customers/"+cust.ID+"/cards/sell", sellBody,
		withClaims(claims1), withPathParam("id", cust.ID))
	status, body = runHandler(t, sellCardHandler, ctx)
	if status != http.StatusOK {
		t.Fatalf("sell: %s", body)
	}
	var cc storage.CustomerCard
	json.Unmarshal([]byte(body), &cc)

	// shop2 用 cc.ID 调扣减 → 应 404
	consumeBody := []byte(`{"amount_cents":1000,"reason":"跨店"}`)
	ctx = newAPIContext(t, "POST", "/api/admin/customer-cards/"+cc.ID+"/consume", consumeBody,
		withClaims(claims2), withPathParam("id", cc.ID))
	status, body = runHandler(t, consumeCardHandler, ctx)
	if status != http.StatusNotFound {
		t.Errorf("跨店应 404，得到 %d body=%s", status, body)
	}

	// shop2 查流水 → 应 404
	ctx = newAPIContext(t, "GET", "/api/admin/customer-cards/"+cc.ID+"/transactions", nil,
		withClaims(claims2), withPathParam("id", cc.ID))
	status, body = runHandler(t, listCardTransactionsHandler, ctx)
	if status != http.StatusNotFound {
		t.Errorf("跨店查流水应 404，得到 %d", status)
	}
}

// ---- 顾客详情带 cards ----

func TestGetCustomerDetail_IncludesCards(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-det", "")
	// 把 plan 改成 pro 让卡管理可用
	storage.DB.Model(shop).Update("plan", storage.PlanPro)
	cust := storage.MakeCustomer(t, "Alice", 0, 0)
	// 先让顾客在本店有预约
	storage.MakeAppointment(t, shop.ID, cust.ID, "Alice", "Tony", "2099-12-31", "10:00")
	// 建卡 + 售卡
	c, _ := storage.CreateCard(nil, storage.CreateCardParams{
		ShopID: shop.ID, Name: "测试卡", Type: storage.CardTypeStoredValue,
		PriceCents: 100000, FaceValueCents: 100000, BonusCents: 0,
	})
	storage.SellCardToCustomer(nil, storage.SellCardToCustomerParams{
		ShopID: shop.ID, CustomerID: cust.ID, CardID: c.ID,
		OperatorID: 1, OperatorName: "test",
	})

	ctx := newAPIContext(t, "GET", "/api/admin/customers/"+cust.ID, nil,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", cust.ID))
	status, body := runHandler(t, getCustomerDetailHandler, ctx)
	if status != http.StatusOK {
		t.Fatalf("应 200，得到 %d body=%s", status, body)
	}
	if !strings.Contains(body, `"cards":`) {
		t.Errorf("响应应含 cards 字段，实际：%s", body)
	}
	if !strings.Contains(body, `"card_name":"测试卡"`) {
		t.Errorf("响应应含卡名，实际：%s", body)
	}
}

// ---- v4.16.4: GET /api/admin/customer-cards/:id 单卡详情 ----

// TestGetCustomerCard_OK 验证 happy path：返回 cc 完整字段
func TestGetCustomerCard_OK(t *testing.T) {
	setupAPITestDB(t)
	makeShopWithPlan(t, "shop-gcc", storage.PlanPro)
	cust := storage.MakeCustomer(t, "李四", 0, 0)
	claims := &auth.Claims{AdminID: 1, ShopID: "shop-gcc", Role: storage.RoleOwner}

	// 建卡 + 售卡拿 cc
	createBody := []byte(`{"name":"v4.16.4测试卡","type":"stored_value","price_cents":100000,"face_value_cents":100000,"bonus_cents":0,"valid_days":365}`)
	ctx := newAPIContext(t, "POST", "/api/admin/cards", createBody, withClaims(claims))
	_, body := runHandler(t, createCardHandler, ctx)
	var card storage.Card
	json.Unmarshal([]byte(body), &card)

	sellBody := []byte(`{"card_id":"` + card.ID + `"}`)
	ctx = newAPIContext(t, "POST", "/api/admin/customers/"+cust.ID+"/cards/sell", sellBody,
		withClaims(claims), withPathParam("id", cust.ID))
	_, body = runHandler(t, sellCardHandler, ctx)
	var cc storage.CustomerCard
	json.Unmarshal([]byte(body), &cc)

	// 调新 endpoint
	ctx = newAPIContext(t, "GET", "/api/admin/customer-cards/"+cc.ID, nil,
		withClaims(claims), withPathParam("id", cc.ID))
	status, body := runHandler(t, getCustomerCardHandler, ctx)
	if status != http.StatusOK {
		t.Fatalf("应 200，得到 %d body=%s", status, body)
	}
	var got storage.CustomerCard
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("parse: %v body=%s", err, body)
	}
	if got.ID != cc.ID {
		t.Errorf("id 应回 %s，得到 %s", cc.ID, got.ID)
	}
	if got.BalanceCents != 100000 {
		t.Errorf("balance 应回 100000，得到 %d", got.BalanceCents)
	}
	if got.Status != "active" {
		t.Errorf("status 应为 active，得到 %s", got.Status)
	}
}

// TestGetCustomerCard_NotFound 验证 cc 不存在时 404
func TestGetCustomerCard_NotFound(t *testing.T) {
	setupAPITestDB(t)
	makeShopWithPlan(t, "shop-nf", storage.PlanPro)
	claims := &auth.Claims{AdminID: 1, ShopID: "shop-nf", Role: storage.RoleOwner}

	ctx := newAPIContext(t, "GET", "/api/admin/customer-cards/nonexistent-id", nil,
		withClaims(claims), withPathParam("id", "nonexistent-id"))
	status, _ := runHandler(t, getCustomerCardHandler, ctx)
	if status != http.StatusNotFound {
		t.Errorf("不存在 cc 应 404，得到 %d", status)
	}
}

// TestGetCustomerCard_CrossShopIsolation 验证跨店访问 → 404（防泄漏）
//
// v4.16.4 真实事故：商户在「顾客详情」点卡进详情，缓存可能没这张卡。
// 如果跨店能访问，会泄漏其他店的卡信息。
func TestGetCustomerCard_CrossShopIsolation(t *testing.T) {
	setupAPITestDB(t)
	makeShopWithPlan(t, "shop-a", storage.PlanPro)
	makeShopWithPlan(t, "shop-b", storage.PlanPro)
	cust := storage.MakeCustomer(t, "跨店测试", 0, 0)
	claimsA := &auth.Claims{AdminID: 1, ShopID: "shop-a", Role: storage.RoleOwner}
	claimsB := &auth.Claims{AdminID: 2, ShopID: "shop-b", Role: storage.RoleOwner}

	// shop-a 建卡 + 售卡
	createBody := []byte(`{"name":"shop-a卡","type":"stored_value","price_cents":100000,"face_value_cents":100000,"bonus_cents":0}`)
	ctx := newAPIContext(t, "POST", "/api/admin/cards", createBody, withClaims(claimsA))
	_, body := runHandler(t, createCardHandler, ctx)
	var card storage.Card
	json.Unmarshal([]byte(body), &card)

	sellBody := []byte(`{"card_id":"` + card.ID + `"}`)
	ctx = newAPIContext(t, "POST", "/api/admin/customers/"+cust.ID+"/cards/sell", sellBody,
		withClaims(claimsA), withPathParam("id", cust.ID))
	_, body = runHandler(t, sellCardHandler, ctx)
	var cc storage.CustomerCard
	json.Unmarshal([]byte(body), &cc)

	// shop-b 用 cc.ID 查 → 应 404（不泄漏）
	ctx = newAPIContext(t, "GET", "/api/admin/customer-cards/"+cc.ID, nil,
		withClaims(claimsB), withPathParam("id", cc.ID))
	status, _ := runHandler(t, getCustomerCardHandler, ctx)
	if status != http.StatusNotFound {
		t.Errorf("跨店查 cc 应 404，得到 %d", status)
	}
}