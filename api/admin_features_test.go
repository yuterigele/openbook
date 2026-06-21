package api

// admin_features_test.go
//
// v4.4 新增 5 个模块的 handler 测试
//
// 覆盖：
//   - Shop: GET / PUT + 字段校验
//   - Handoffs: GET（从 event_logs 筛 handoff_to_human）
//   - Customers: GET + tag add/remove + 跨店隔离
//   - Subscription: GET（历史 + is_current 计算）
//   - Services: GET/POST/PUT/DELETE/activate + 多店隔离
//
// Pattern 与 api_test.go 一致：setupAPITestDB → newAPIContext → runHandler → 断言

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// ============================================================
// 1) Shop
// ============================================================

func TestGetShop_NoClaims(t *testing.T) {
	ctx := newAPIContext(t, "GET", "/api/admin/shop", nil)
	status, body := runHandler(t, getShopHandler, ctx)
	if status != 401 {
		t.Errorf("无 claims 应返回 401，实际 %d body=%s", status, body)
	}
}

func TestGetShop_NotFound(t *testing.T) {
	setupAPITestDB(t)
	ctx := newAPIContext(t, "GET", "/api/admin/shop", nil, withClaims(adminClaims("no-such-shop")))
	status, body := runHandler(t, getShopHandler, ctx)
	if status != 404 {
		t.Errorf("不存在的 shop 应 404，实际 %d body=%s", status, body)
	}
}

func TestGetShop_HappyPath(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-get", "")
	ctx := newAPIContext(t, "GET", "/api/admin/shop", nil, withClaims(adminClaims(shop.ID)))
	status, body := runHandler(t, getShopHandler, ctx)
	if status != 200 {
		t.Fatalf("应 200，实际 %d body=%s", status, body)
	}
	if !strings.Contains(body, shop.ID) {
		t.Errorf("响应里应包含 shop ID: %s", body)
	}
}

func TestUpdateShop_ValidationErrors(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-upd", "")

	cases := []struct {
		label, body, wantSubstr string
	}{
		{"open_hour 越界", `{"open_hour": 99}`, "open_hour 必须在"},
		{"close_hour 越界", `{"close_hour": 99}`, "close_hour 必须在"},
		{"open >= close", `{"open_hour": 20, "close_hour": 10}`, "open_hour 必须早于"},
		{"timezone 无效", `{"timezone": "Mars/Olympus"}`, "timezone 无效"},
		{"lunch_start 越界", `{"lunch_start": 99}`, "lunch_start 必须在"},
		{"lunch_end 越界", `{"lunch_end": 99}`, "lunch_end 必须在"},
		{"lunch_end_min 越界", `{"lunch_end_min": 99}`, "lunch_end_min 必须在"},
		{"name 空字符串", `{"name": "   "}`, "店铺名不能为空"},
		{"name 超长", `{"name": "` + strings.Repeat("店", 200) + `"}`, "店铺名过长"},
		{"holidays 超长", `{"holidays": "` + strings.Repeat("a", 600) + `"}`, "holidays 过长"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			ctx := newAPIContext(t, "PUT", "/api/admin/shop", []byte(c.body), withClaims(adminClaims(shop.ID)))
			status, body := runHandler(t, updateShopHandler, ctx)
			if status != 400 {
				t.Errorf("应 400，实际 %d body=%s", status, body)
			}
			if !strings.Contains(body, c.wantSubstr) {
				t.Errorf("错误信息缺 %q: %s", c.wantSubstr, body)
			}
		})
	}
}

func TestUpdateShop_HappyPath(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-upd-ok", "")
	body := `{"name": "新店名", "address": "新地址", "open_hour": 10, "close_hour": 22}`
	ctx := newAPIContext(t, "PUT", "/api/admin/shop", []byte(body), withClaims(adminClaims(shop.ID)))
	status, resp := runHandler(t, updateShopHandler, ctx)
	if status != 200 {
		t.Fatalf("应 200，实际 %d body=%s", status, resp)
	}
	if !strings.Contains(resp, "新店名") || !strings.Contains(resp, "新地址") {
		t.Errorf("更新未生效: %s", resp)
	}
}

// ============================================================
// 2) Handoffs
// ============================================================

func TestListHandoffs_NoClaims(t *testing.T) {
	ctx := newAPIContext(t, "GET", "/api/admin/handoffs", nil)
	status, _ := runHandler(t, listHandoffsHandler, ctx)
	if status != 401 {
		t.Errorf("无 claims 应 401，实际 %d", status)
	}
}

func TestListHandoffs_FilterByShopAndType(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-handoff", "")

	// 写 1 条 handoff + 1 条其他 event
	storage.TrackEvent(context.Background(), shop.ID, storage.EventHandoffToHuman, "cust-1", map[string]any{
		"reason":            "无法识别意图",
		"last_user_message": "我想约明天下午三点",
	})
	storage.TrackEvent(context.Background(), shop.ID, storage.EventAppointmentCreated, "appt-1", nil)

	ctx := newAPIContext(t, "GET", "/api/admin/handoffs", nil, withClaims(adminClaims(shop.ID)))
	status, body := runHandler(t, listHandoffsHandler, ctx)
	if status != 200 {
		t.Fatalf("应 200，实际 %d body=%s", status, body)
	}
	if !strings.Contains(body, "无法识别意图") {
		t.Errorf("响应缺 reason: %s", body)
	}
	if !strings.Contains(body, "我想约明天下午三点") {
		t.Errorf("响应缺 last_user_message: %s", body)
	}
	if !strings.Contains(body, "cust-1") {
		t.Errorf("响应缺 customer id: %s", body)
	}
	// 应当只有 1 条 handoff（不是其他事件）
	if strings.Count(body, "appointment_created") > 0 {
		t.Errorf("筛选错误，把其他 event 也带进来了: %s", body)
	}
}

func TestListHandoffs_OtherShopExcluded(t *testing.T) {
	setupAPITestDB(t)
	shopA := storage.MakeShop(t, "shop-hA", "")
	shopB := storage.MakeShop(t, "shop-hB", "")

	storage.TrackEvent(context.Background(), shopA.ID, storage.EventHandoffToHuman, "cust-A", nil)
	storage.TrackEvent(context.Background(), shopB.ID, storage.EventHandoffToHuman, "cust-B", nil)

	ctx := newAPIContext(t, "GET", "/api/admin/handoffs", nil, withClaims(adminClaims(shopA.ID)))
	_, body := runHandler(t, listHandoffsHandler, ctx)
	if !strings.Contains(body, "cust-A") {
		t.Errorf("A 店 handoff 应出现: %s", body)
	}
	if strings.Contains(body, "cust-B") {
		t.Errorf("B 店 handoff 不应出现: %s", body)
	}
}

// ============================================================
// 3) Customers
// ============================================================

func TestListCustomers_NoClaims(t *testing.T) {
	ctx := newAPIContext(t, "GET", "/api/admin/customers", nil)
	status, _ := runHandler(t, listCustomersHandler, ctx)
	if status != 401 {
		t.Errorf("无 claims 应 401，实际 %d", status)
	}
}

func TestListCustomers_FilterByAppointments(t *testing.T) {
	setupAPITestDB(t)
	shopA := storage.MakeShop(t, "shop-cust-A", "")
	shopB := storage.MakeShop(t, "shop-cust-B", "")

	// A 店有 1 个顾客（带 1 条预约）
	cA := storage.MakeCustomer(t, "Alice", 0, 0)
	storage.MakeAppointment(t, shopA.ID, cA.ID, "Alice", "Tony", "2026-07-01", "10:00")

	// B 店有 1 个顾客（带 1 条预约），不应出现在 A 店结果里
	cB := storage.MakeCustomer(t, "Bob", 0, 0)
	storage.MakeAppointment(t, shopB.ID, cB.ID, "Bob", "Tony", "2026-07-01", "10:00")

	// 还有个顾客没有预约，不应出现
	_ = storage.MakeCustomer(t, "Charlie", 0, 0)

	ctx := newAPIContext(t, "GET", "/api/admin/customers?limit=100", nil, withClaims(adminClaims(shopA.ID)))
	status, body := runHandler(t, listCustomersHandler, ctx)
	if status != 200 {
		t.Fatalf("应 200，实际 %d body=%s", status, body)
	}
	if !strings.Contains(body, "Alice") {
		t.Errorf("Alice 应出现: %s", body)
	}
	if strings.Contains(body, "Bob") {
		t.Errorf("Bob 不应出现在 A 店结果: %s", body)
	}
	if strings.Contains(body, "Charlie") {
		t.Errorf("没预约的 Charlie 不应出现: %s", body)
	}
}

func TestCustomerTag_AddRemoveAndIsolation(t *testing.T) {
	setupAPITestDB(t)
	shopA := storage.MakeShop(t, "shop-tagA", "")
	shopB := storage.MakeShop(t, "shop-tagB", "")
	cA := storage.MakeCustomer(t, "Alice", 0, 0)
	cB := storage.MakeCustomer(t, "Bob", 0, 0)
	storage.MakeAppointment(t, shopA.ID, cA.ID, "Alice", "Tony", "2026-07-01", "10:00")
	storage.MakeAppointment(t, shopB.ID, cB.ID, "Bob", "Tony", "2026-07-01", "10:00")

	// 1) A 店给 Alice 加 VIP → 200
	body := `{"customer_id":"` + cA.ID + `","tag":"VIP"}`
	ctx := newAPIContext(t, "POST", "/api/admin/customers/tag", []byte(body), withClaims(adminClaims(shopA.ID)))
	status, _ := runHandler(t, addCustomerTagHandler, ctx)
	if status != 200 {
		t.Errorf("A 店给 Alice 加 VIP 应 200，实际 %d", status)
	}

	// 2) A 店给 Bob 加 VIP → 404（不在 A 店）
	body = `{"customer_id":"` + cB.ID + `","tag":"VIP"}`
	ctx = newAPIContext(t, "POST", "/api/admin/customers/tag", []byte(body), withClaims(adminClaims(shopA.ID)))
	status, _ = runHandler(t, addCustomerTagHandler, ctx)
	if status != 404 {
		t.Errorf("A 店给 Bob 加 VIP 应 404，实际 %d", status)
	}

	// 3) 不合法 tag
	body = `{"customer_id":"` + cA.ID + `","tag":"NOT_A_TAG"}`
	ctx = newAPIContext(t, "POST", "/api/admin/customers/tag", []byte(body), withClaims(adminClaims(shopA.ID)))
	status, _ = runHandler(t, addCustomerTagHandler, ctx)
	if status != 400 {
		t.Errorf("非法 tag 应 400，实际 %d", status)
	}

	// 4) 删除
	body = `{"customer_id":"` + cA.ID + `","tag":"VIP"}`
	ctx = newAPIContext(t, "DELETE", "/api/admin/customers/tag", []byte(body), withClaims(adminClaims(shopA.ID)))
	status, _ = runHandler(t, removeCustomerTagHandler, ctx)
	if status != 200 {
		t.Errorf("删除 tag 应 200，实际 %d", status)
	}
}

// ============================================================
// 4) Subscription
// ============================================================

func TestListSubscriptions_NoClaims(t *testing.T) {
	ctx := newAPIContext(t, "GET", "/api/admin/subscription", nil)
	status, _ := runHandler(t, listSubscriptionsHandler, ctx)
	if status != 401 {
		t.Errorf("无 claims 应 401，实际 %d", status)
	}
}

func TestListSubscriptions_IsCurrentFlag(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-sub", "")

	// 写 1 条已过期 + 1 条生效中
	storage.DB.Create(&storage.Subscription{
		ID: "sub-old", ShopID: shop.ID, Plan: "basic",
		StartedAt: nowTime().AddDate(0, -2, 0),
		ExpiresAt: nowTime().AddDate(0, -1, 0),
	})
	storage.DB.Create(&storage.Subscription{
		ID: "sub-cur", ShopID: shop.ID, Plan: "flagship",
		StartedAt: nowTime(),
		ExpiresAt: nowTime().AddDate(0, 1, 0),
	})

	ctx := newAPIContext(t, "GET", "/api/admin/subscription", nil, withClaims(adminClaims(shop.ID)))
	status, body := runHandler(t, listSubscriptionsHandler, ctx)
	if status != 200 {
		t.Fatalf("应 200，实际 %d body=%s", status, body)
	}
	if !strings.Contains(body, `"is_current":true`) {
		t.Errorf("生效中那条 is_current 应为 true: %s", body)
	}
	if !strings.Contains(body, `"is_current":false`) {
		t.Errorf("已过期那条 is_current 应为 false: %s", body)
	}
}

// ============================================================
// 5) Services
// ============================================================

func TestServices_CRUD(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-svc", "")

	// 1) Create
	body := `{"name":"剪发","estimated_min":30,"price_range":"30-50","sort_order":10}`
	ctx := newAPIContext(t, "POST", "/api/admin/services", []byte(body), withClaims(adminClaims(shop.ID)))
	status, resp := runHandler(t, createServiceHandler, ctx)
	if status != 200 {
		t.Fatalf("Create 应 200，实际 %d body=%s", status, resp)
	}
	if !strings.Contains(resp, `"name":"剪发"`) {
		t.Errorf("响应缺 name: %s", resp)
	}
	// 提取 ID（粗略）
	id := extractField(resp, "id")
	if id == "" {
		t.Fatalf("响应里没 id: %s", resp)
	}

	// 2) List
	ctx = newAPIContext(t, "GET", "/api/admin/services", nil, withClaims(adminClaims(shop.ID)))
	status, resp = runHandler(t, listServicesHandler, ctx)
	if status != 200 {
		t.Fatalf("List 应 200，实际 %d body=%s", status, resp)
	}
	if !strings.Contains(resp, "剪发") {
		t.Errorf("List 缺剪发: %s", resp)
	}

	// 3) Update
	upd := `{"name":"精剪","estimated_min":45,"price_range":"50-80","sort_order":20}`
	ctx = newAPIContext(t, "PUT", "/api/admin/services/"+id, []byte(upd), withClaims(adminClaims(shop.ID)), withPathParam("id", id))
	status, resp = runHandler(t, updateServiceHandler, ctx)
	if status != 200 {
		t.Fatalf("Update 应 200，实际 %d body=%s", status, resp)
	}
	if !strings.Contains(resp, "精剪") {
		t.Errorf("Update 后缺新名: %s", resp)
	}

	// 4) Deactivate
	ctx = newAPIContext(t, "DELETE", "/api/admin/services/"+id, nil, withClaims(adminClaims(shop.ID)), withPathParam("id", id))
	status, _ = runHandler(t, deactivateServiceHandler, ctx)
	if status != 200 {
		t.Errorf("Deactivate 应 200，实际 %d", status)
	}

	// 5) 重新 list include_inactive=true 应当看到
	ctx = newAPIContext(t, "GET", "/api/admin/services?include_inactive=true", nil, withClaims(adminClaims(shop.ID)))
	_, resp = runHandler(t, listServicesHandler, ctx)
	if !strings.Contains(resp, "精剪") {
		t.Errorf("Deactivate 后 include_inactive=true 应仍能看到: %s", resp)
	}

	// 6) Activate
	ctx = newAPIContext(t, "POST", "/api/admin/services/"+id+"/activate", nil, withClaims(adminClaims(shop.ID)), withPathParam("id", id))
	status, _ = runHandler(t, activateServiceHandler, ctx)
	if status != 200 {
		t.Errorf("Activate 应 200，实际 %d", status)
	}
}

func TestServices_Validation(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-svc-v", "")

	cases := []struct {
		label, body, wantSubstr string
	}{
		{"name 空", `{"name":"","estimated_min":30,"price_range":""}`, "不能为空"},
		{"min 越界", `{"name":"a","estimated_min":0,"price_range":""}`, "1-480"},
		{"min 过大", `{"name":"a","estimated_min":999,"price_range":""}`, "1-480"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			ctx := newAPIContext(t, "POST", "/api/admin/services", []byte(c.body), withClaims(adminClaims(shop.ID)))
			status, body := runHandler(t, createServiceHandler, ctx)
			if status != 400 {
				t.Errorf("应 400，实际 %d body=%s", status, body)
			}
			if !strings.Contains(body, c.wantSubstr) {
				t.Errorf("错误信息缺 %q: %s", c.wantSubstr, body)
			}
		})
	}
}

func TestServices_CrossShopForbidden(t *testing.T) {
	setupAPITestDB(t)
	shopA := storage.MakeShop(t, "shop-svc-CA", "")
	shopB := storage.MakeShop(t, "shop-svc-CB", "")

	// A 店建一个 service
	ctx := newAPIContext(t, "POST", "/api/admin/services",
		[]byte(`{"name":"X","estimated_min":30,"price_range":""}`),
		withClaims(adminClaims(shopA.ID)))
	_, resp := runHandler(t, createServiceHandler, ctx)
	id := extractField(resp, "id")

	// B 店去 update 这个 ID
	ctx = newAPIContext(t, "PUT", "/api/admin/services/"+id,
		[]byte(`{"name":"Y","estimated_min":30,"price_range":""}`),
		withClaims(adminClaims(shopB.ID)), withPathParam("id", id))
	status, _ := runHandler(t, updateServiceHandler, ctx)
	if status != 404 {
		t.Errorf("B 店 update A 店 service 应 404，实际 %d", status)
	}
}

// ============================================================
// helpers
// ============================================================

// nowTime 返回当前时间（仅测试用）
func nowTime() time.Time {
	return time.Now()
}

// extractField 从 JSON 响应里抠出指定字段的字符串值（粗略版，仅测试用）
//
// 例：extractField(`{"id":"abc-123","name":"X"}`, "id") → "abc-123"
func extractField(body, key string) string {
	needle := `"` + key + `":"`
	i := strings.Index(body, needle)
	if i < 0 {
		return ""
	}
	rest := body[i+len(needle):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
