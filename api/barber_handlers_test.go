package api

// barber_handlers_test.go
//
// P5 理发师管理 handler 测试
//
// 覆盖：
//   - listBarbersHandler       GET    /api/admin/barbers
//   - createBarberHandler      POST   /api/admin/barbers
//   - softDeleteBarberHandler  DELETE /api/admin/barbers/:id
//   - activateBarberHandler    POST   /api/admin/barbers/:id/activate
//
// 模式与 api_test.go 一致：setupAPITestDB + MakeShop → plant fixtures → newAPIContext → runHandler → assert。
//
// 约定：
//   - 每个 test 自己调 setupAPITestDB + storage.MakeShop（避免 plantBarber 里重复 MakeShop 撞 unique）
//   - barberID 约定 "barber-<Name>"（跟 storage.MakeAppointment 的 "barber-" + name 配对）
//
// Run:
//   go test ./api/... -v -run "TestListBarbers|TestCreateBarber|TestSoftDeleteBarberHandler|TestActivateBarberHandler"

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// =====================================================================
// listBarbersHandler
// =====================================================================

func TestListBarbers_NoClaims(t *testing.T) {
	setupAPITestDB(t)
	ctx := newAPIContext(t, "GET", "/api/admin/barbers", nil)

	status, body := runHandler(t, listBarbersHandler, ctx)
	if status != statusUnauthorized {
		t.Errorf("expected 401, got %d body=%s", status, body)
	}
}

func TestListBarbers_Empty(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "GET", "/api/admin/barbers", nil, withClaims(adminClaims(shopID)))

	status, body := runHandler(t, listBarbersHandler, ctx)
	if status != statusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}
	// 空集合应返回 [] 而不是 null
	if body != "[]" {
		t.Errorf("expected [], got %q", body)
	}
}

func TestListBarbers_HappyPath(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	plantBarber(t, shopID, "Tony", true)
	plantBarber(t, shopID, "Kevin", true)

	ctx := newAPIContext(t, "GET", "/api/admin/barbers", nil, withClaims(adminClaims(shopID)))
	status, body := runHandler(t, listBarbersHandler, ctx)
	if status != statusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "Tony") || !strings.Contains(body, "Kevin") {
		t.Errorf("expected both names, got %s", body)
	}
}

func TestListBarbers_IncludeInactive(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	plantBarber(t, shopID, "Tony", true)
	plantBarber(t, shopID, "Kevin", false) // inactive

	// 默认：不含 inactive
	ctx := newAPIContext(t, "GET", "/api/admin/barbers", nil, withClaims(adminClaims(shopID)))
	status, body := runHandler(t, listBarbersHandler, ctx)
	if status != statusOK {
		t.Fatalf("default list failed: %d body=%s", status, body)
	}
	if strings.Contains(body, "Kevin") {
		t.Errorf("default should not include inactive Kevin, got %s", body)
	}

	// include_inactive=true：包含
	ctx2 := newAPIContext(t, "GET", "/api/admin/barbers", nil,
		withClaims(adminClaims(shopID)),
		withQuery("include_inactive", "true"))
	status2, body2 := runHandler(t, listBarbersHandler, ctx2)
	if status2 != statusOK {
		t.Fatalf("include_inactive list failed: %d body=%s", status2, body2)
	}
	if !strings.Contains(body2, "Kevin") {
		t.Errorf("include_inactive should include Kevin, got %s", body2)
	}
}

// =====================================================================
// createBarberHandler
// =====================================================================

func TestCreateBarber_NoClaims(t *testing.T) {
	setupAPITestDB(t)
	ctx := newAPIContext(t, "POST", "/api/admin/barbers", jsonRaw(`{"name":"Tony"}`))

	status, body := runHandler(t, createBarberHandler, ctx)
	if status != statusUnauthorized {
		t.Errorf("expected 401, got %d body=%s", status, body)
	}
}

func TestCreateBarber_EmptyName(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "POST", "/api/admin/barbers",
		jsonRaw(`{"name":""}`),
		withClaims(adminClaims(shopID)))

	status, body := runHandler(t, createBarberHandler, ctx)
	if status != statusBadRequest {
		t.Errorf("expected 400, got %d body=%s", status, body)
	}
}

func TestCreateBarber_HappyPath(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "POST", "/api/admin/barbers",
		jsonRaw(`{"name":"Tony","skills":"剪发,染发"}`),
		withClaims(adminClaims(shopID)))

	status, body := runHandler(t, createBarberHandler, ctx)
	if status != statusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}
	resp := decodeJSON(t, body)
	if resp["name"] != "Tony" {
		t.Errorf("name: got %v want Tony", resp["name"])
	}
	if resp["active"] != true {
		t.Errorf("active should be true, got %v", resp["active"])
	}
	if resp["shop_id"] != shopID {
		t.Errorf("shop_id: got %v want %v", resp["shop_id"], shopID)
	}
}

func TestCreateBarber_DuplicateName(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	plantBarber(t, shopID, "Tony", true)

	ctx := newAPIContext(t, "POST", "/api/admin/barbers",
		jsonRaw(`{"name":"Tony"}`),
		withClaims(adminClaims(shopID)))

	status, body := runHandler(t, createBarberHandler, ctx)
	if status != statusConflict {
		t.Errorf("expected 409, got %d body=%s", status, body)
	}
}

// =====================================================================
// softDeleteBarberHandler
// =====================================================================

func TestSoftDeleteBarberHandler_NoClaims(t *testing.T) {
	setupAPITestDB(t)
	ctx := newAPIContext(t, "DELETE", "/api/admin/barbers/any", nil)

	status, _ := runHandler(t, softDeleteBarberHandler, ctx)
	if status != statusUnauthorized {
		t.Errorf("expected 401, got %d", status)
	}
}

func TestSoftDeleteBarberHandler_NotFound(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "DELETE", "/api/admin/barbers/no-such-id", nil,
		withClaims(adminClaims(shopID)),
		withPathParam("id", "no-such-id"))

	status, _ := runHandler(t, softDeleteBarberHandler, ctx)
	if status != statusNotFound {
		t.Errorf("expected 404, got %d", status)
	}
}

func TestSoftDeleteBarberHandler_HappyPath(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	b := plantBarber(t, shopID, "Tony", true)

	ctx := newAPIContext(t, "DELETE", "/api/admin/barbers/"+b.ID, nil,
		withClaims(adminClaims(shopID)),
		withPathParam("id", b.ID))

	status, body := runHandler(t, softDeleteBarberHandler, ctx)
	if status != statusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}
	resp := decodeJSON(t, body)
	if resp["status"] != "deleted" {
		t.Errorf("status: got %v want deleted", resp["status"])
	}

	// 复查：active=false
	got, _ := storage.GetBarberInShop(contextBackground(), shopID, b.ID)
	if got.Active {
		t.Errorf("barber should be inactive after delete")
	}
}

func TestSoftDeleteBarberHandler_WithFutureAppt_409(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	b := plantBarber(t, shopID, "Tony", true)
	cust := plantCustomer(t, "Alice")
	// plantAppointment 用 b.Name（"Tony"），MakeAppointment 内部会拼成 "barber-Tony"
	plantAppointment(t, shopID, cust.ID, cust.Name, b.Name, futureDate(t, 1), "14:00")

	ctx := newAPIContext(t, "DELETE", "/api/admin/barbers/"+b.ID, nil,
		withClaims(adminClaims(shopID)),
		withPathParam("id", b.ID))

	status, body := runHandler(t, softDeleteBarberHandler, ctx)
	if status != statusConflict {
		t.Errorf("expected 409 (有未来预约), got %d body=%s", status, body)
	}
}

func TestSoftDeleteBarberHandler_Idempotent(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	b := plantBarber(t, shopID, "Tony", false) // 已经 inactive

	ctx := newAPIContext(t, "DELETE", "/api/admin/barbers/"+b.ID, nil,
		withClaims(adminClaims(shopID)),
		withPathParam("id", b.ID))

	status, _ := runHandler(t, softDeleteBarberHandler, ctx)
	if status != statusOK {
		t.Errorf("idempotent delete should be OK, got %d", status)
	}
}

// =====================================================================
// activateBarberHandler
// =====================================================================

func TestActivateBarberHandler_NoClaims(t *testing.T) {
	setupAPITestDB(t)
	ctx := newAPIContext(t, "POST", "/api/admin/barbers/any/activate", nil)

	status, _ := runHandler(t, activateBarberHandler, ctx)
	if status != statusUnauthorized {
		t.Errorf("expected 401, got %d", status)
	}
}

func TestActivateBarberHandler_NotFound(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "POST", "/api/admin/barbers/no-such-id/activate", nil,
		withClaims(adminClaims(shopID)),
		withPathParam("id", "no-such-id"))

	status, _ := runHandler(t, activateBarberHandler, ctx)
	if status != statusNotFound {
		t.Errorf("expected 404, got %d", status)
	}
}

func TestActivateBarberHandler_HappyPath(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	b := plantBarber(t, shopID, "Tony", false) // inactive

	ctx := newAPIContext(t, "POST", "/api/admin/barbers/"+b.ID+"/activate", nil,
		withClaims(adminClaims(shopID)),
		withPathParam("id", b.ID))

	status, body := runHandler(t, activateBarberHandler, ctx)
	if status != statusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}
	resp := decodeJSON(t, body)
	if resp["status"] != "activated" {
		t.Errorf("status: got %v want activated", resp["status"])
	}

	got, _ := storage.GetBarberInShop(contextBackground(), shopID, b.ID)
	if !got.Active {
		t.Errorf("barber should be active after activate")
	}
}

// =====================================================================
// Fixtures & helpers
// =====================================================================

// plantBarber 创建一个测试 barber（约定 ID = "barber-<Name>" 跟 MakeAppointment 配对）
//
// 注意：调用方需先 setupAPITestDB + storage.MakeShop（避免 MakeShop 撞 unique）。
// active=false 时直接 DB 更新，绕过 SoftDelete 的未来预约检查（测试 fixture 专用）。
func plantBarber(t *testing.T, shopID, name string, active bool) storage.Barber {
	t.Helper()
	b := storage.MakeBarber(t, "barber-"+name, shopID, name)
	if !active {
		if err := storage.DB.Model(&storage.Barber{}).Where("id = ?", b.ID).
			Update("active", false).Error; err != nil {
			t.Fatalf("deactivate: %v", err)
		}
	}
	return *b
}

// plantCustomer 用 storage.MakeCustomer（返回带 ID 的 customer）
func plantCustomer(t *testing.T, name string) storage.Customer {
	t.Helper()
	return *storage.MakeCustomer(t, name, 0, 0)
}

// plantAppointment 创建一个 future active 预约
//
// 注意：MakeAppointment 内部会把传入的 barberName 前缀 "barber-" 再拼一次，
// 所以这里传 "Tony"（不带前缀）；barberID 自动变成 "barber-Tony"，
// 跟 plantBarber 创建的 ID 一致。
func plantAppointment(t *testing.T, shopID, customerID, customerName, barberName, date, time string) {
	t.Helper()
	storage.MakeAppointment(t, shopID, customerID, customerName, barberName, date, time)
}

// newShopID 随机生成 shopID，调用方需自己 MakeShop
func newShopID() string {
	return "shop-" + uuid.NewString()
}

// contextBackground 避免每个 test 都写 context.Background() 啰嗦
func contextBackground() context.Context {
	return context.Background()
}
