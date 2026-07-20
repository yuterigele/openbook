package api

// admin_features_v46_test.go
//
// v4.6 增量 3 个 handler 的单测：
//
//   1) getCustomerDetailHandler  GET /api/admin/customers/:id
//   2) resolveHandoffHandler     POST /api/admin/handoffs/:id/resolve
//   3) importServicesHandler    POST /api/admin/services/import
//
// 覆盖：
//   - 401 无 claims
//   - 404 不存在 / 跨店
//   - 200 正常路径
//   - 400 输入校验
//   - 特殊路径：replace=true 软下架旧服务
//   - 特殊路径：resolved 写埋点 + 关联回原 handoff

import (
	"strings"
	"testing"

	"github.com/yuterigele/openbook/storage"
)

// ============================================================
// 1) Customer detail
// ============================================================

func TestGetCustomerDetail_NoClaims(t *testing.T) {
	ctx := newAPIContext(t, "GET", "/api/admin/customers/cust-1", nil)
	status, _ := runHandler(t, getCustomerDetailHandler, ctx)
	if status != 401 {
		t.Errorf("无 claims 应返回 401，实际 %d", status)
	}
}

func TestGetCustomerDetail_NotFound(t *testing.T) {
	setupAPITestDB(t)
	ctx := newAPIContext(t, "GET", "/api/admin/customers/no-such-cust", nil,
		withClaims(adminClaims("shop-1")),
		withPathParam("id", "no-such-cust"))
	status, _ := runHandler(t, getCustomerDetailHandler, ctx)
	if status != 404 {
		t.Errorf("不存在的顾客应 404，实际 %d", status)
	}
}

func TestGetCustomerDetail_CrossShopForbidden(t *testing.T) {
	setupAPITestDB(t)
	shopA := storage.MakeShop(t, "shop-cust-a", "")
	shopB := storage.MakeShop(t, "shop-cust-b", "")

	// 顾客在 shopA 有过预约
	cust := storage.MakeCustomer(t, "Alice", 0, 0)
	storage.MakeAppointment(t, shopA.ID, cust.ID, "Alice", "Tony", "2026-06-20", "10:00")

	// 用 shopB 的 claims 查 → 应 404
	ctx := newAPIContext(t, "GET", "/api/admin/customers/"+cust.ID, nil,
		withClaims(adminClaims(shopB.ID)),
		withPathParam("id", cust.ID))
	status, body := runHandler(t, getCustomerDetailHandler, ctx)
	if status != 404 {
		t.Errorf("跨店访问应 404（隐藏存在性），实际 %d body=%s", status, body)
	}
}

func TestGetCustomerDetail_HappyPath(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-cust-detail", "")
	cust := storage.MakeCustomer(t, "Bob", 1, 2) // late=1, noshow=2
	storage.AddCustomerTag(storage.WithCtx(), cust.ID, "VIP")

	// 建 5 条预约（3 completed + 1 cancelled + 1 active future）
	for _, timeStr := range []string{"10:00", "10:30", "11:00"} {
		storage.MakeAppointment(t, shop.ID, cust.ID, "Bob", "Tony", "2026-06-15", timeStr)
	}
	storage.MakeAppointment(t, shop.ID, cust.ID, "Bob", "Tony", "2026-06-16", "11:00")
	// 未来 active
	storage.MakeAppointment(t, shop.ID, cust.ID, "Bob", "Tony", "2099-12-31", "14:00")

	// mark 3 completed
	var appts []storage.Appointment
	storage.DB.Where("shop_id = ? AND customer_id = ? AND date = ?", shop.ID, cust.ID, "2026-06-15").
		Find(&appts)
	for _, a := range appts {
		storage.DB.Model(&a).Updates(map[string]any{"status": "completed", "active_slot_key": nil})
	}
	// mark 1 cancelled
	storage.DB.Model(&storage.Appointment{}).
		Where("shop_id = ? AND customer_id = ? AND date = ?", shop.ID, cust.ID, "2026-06-16").
		Update("status", "cancelled")

	ctx := newAPIContext(t, "GET", "/api/admin/customers/"+cust.ID, nil,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", cust.ID))
	status, body := runHandler(t, getCustomerDetailHandler, ctx)
	if status != 200 {
		t.Fatalf("应 200，实际 %d body=%s", status, body)
	}
	mustContain(t, body, `"name":"Bob"`)
	mustContain(t, body, `"VIP"`)
	mustContain(t, body, `"upcoming_count":1`)
	mustContain(t, body, `"completed":3`)
	mustContain(t, body, `"cancelled":1`)
	mustContain(t, body, `"no_show_count":2`)
}

// ============================================================
// 2) Resolve handoff
// ============================================================

func TestResolveHandoff_NoClaims(t *testing.T) {
	ctx := newAPIContext(t, "POST", "/api/admin/handoffs/1/resolve", []byte(`{}`))
	status, _ := runHandler(t, resolveHandoffHandler, ctx)
	if status != 401 {
		t.Errorf("无 claims 应 401，实际 %d", status)
	}
}

func TestResolveHandoff_BadID(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-resolve-bad", "")
	ctx := newAPIContext(t, "POST", "/api/admin/handoffs/abc/resolve", []byte(`{}`),
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", "abc"))
	status, _ := runHandler(t, resolveHandoffHandler, ctx)
	if status != 400 {
		t.Errorf("非数字 id 应 400，实际 %d", status)
	}
}

func TestResolveHandoff_NotFound(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-resolve-404", "")
	ctx := newAPIContext(t, "POST", "/api/admin/handoffs/99999/resolve", []byte(`{}`),
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", "99999"))
	status, _ := runHandler(t, resolveHandoffHandler, ctx)
	if status != 404 {
		t.Errorf("不存在应 404，实际 %d", status)
	}
}

func TestResolveHandoff_CrossShop(t *testing.T) {
	setupAPITestDB(t)
	shopA := storage.MakeShop(t, "shop-resolve-a", "")
	shopB := storage.MakeShop(t, "shop-resolve-b", "")
	// 在 shopA 写一条 handoff
	storage.TrackEvent(storage.WithCtx(), shopA.ID, storage.EventHandoffToHuman, "cust-x", nil)
	var ev storage.EventLog
	storage.DB.Where("shop_id = ?", shopA.ID).First(&ev)

	ctx := newAPIContext(t, "POST", "/api/admin/handoffs/"+itoa(ev.ID)+"/resolve", []byte(`{}`),
		withClaims(adminClaims(shopB.ID)),
		withPathParam("id", itoa(ev.ID)))
	status, _ := runHandler(t, resolveHandoffHandler, ctx)
	if status != 403 {
		t.Errorf("跨店应 403，实际 %d", status)
	}
}

func TestResolveHandoff_WrongEventType(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-resolve-wrongtype", "")
	// 写一条非 handoff_to_human 的 event
	storage.TrackEvent(storage.WithCtx(), shop.ID, "first_appointment", "cust-x", nil)
	var ev storage.EventLog
	storage.DB.Where("shop_id = ?", shop.ID).First(&ev)

	ctx := newAPIContext(t, "POST", "/api/admin/handoffs/"+itoa(ev.ID)+"/resolve", []byte(`{}`),
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", itoa(ev.ID)))
	status, _ := runHandler(t, resolveHandoffHandler, ctx)
	if status != 400 {
		t.Errorf("非 handoff 事件应 400，实际 %d", status)
	}
}

func TestResolveHandoff_HappyPath(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-resolve-ok", "")
	storage.TrackEvent(storage.WithCtx(), shop.ID, storage.EventHandoffToHuman, "cust-y",
		map[string]any{"reason": "test"})
	var origin storage.EventLog
	storage.DB.Where("shop_id = ? AND event_type = ?", shop.ID, storage.EventHandoffToHuman).First(&origin)

	ctx := newAPIContext(t, "POST", "/api/admin/handoffs/"+itoa(origin.ID)+"/resolve",
		[]byte(`{"note":"已联系顾客","customer_id":"cust-y"}`),
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", itoa(origin.ID)))
	status, body := runHandler(t, resolveHandoffHandler, ctx)
	if status != 200 {
		t.Fatalf("应 200，实际 %d body=%s", status, body)
	}
	mustContain(t, body, `"resolved_event_id":`)
	mustContain(t, body, `"resolved_by":"1"`)

	// 校验埋点确实写了
	var resolved storage.EventLog
	storage.DB.Where("shop_id = ? AND event_type = ?", shop.ID, storage.EventHandoffResolved).First(&resolved)
	if resolved.ID == 0 {
		t.Fatal("resolved 埋点没写")
	}
	if !strings.Contains(resolved.Meta, "已联系顾客") {
		t.Errorf("meta 缺备注: %s", resolved.Meta)
	}
	if !strings.Contains(resolved.Meta, itoa(origin.ID)) {
		t.Errorf("meta 没关联原 handoff id: %s", resolved.Meta)
	}
}

func TestResolveHandoff_EmptyBodyOK(t *testing.T) {
	// resolve 应允许空 body
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-resolve-empty", "")
	storage.TrackEvent(storage.WithCtx(), shop.ID, storage.EventHandoffToHuman, "cust-z", nil)
	var origin storage.EventLog
	storage.DB.Where("shop_id = ?", shop.ID).First(&origin)

	ctx := newAPIContext(t, "POST", "/api/admin/handoffs/"+itoa(origin.ID)+"/resolve", nil,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", itoa(origin.ID)))
	status, _ := runHandler(t, resolveHandoffHandler, ctx)
	if status != 200 {
		t.Errorf("空 body 应 200，实际 %d", status)
	}
}

// ============================================================
// 3) Import services
// ============================================================

func TestImportServices_NoClaims(t *testing.T) {
	ctx := newAPIContext(t, "POST", "/api/admin/services/import", []byte(`{"services":[]}`))
	status, _ := runHandler(t, importServicesHandler, ctx)
	if status != 401 {
		t.Errorf("无 claims 应 401，实际 %d", status)
	}
}

func TestImportServices_EmptyArray(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-import-empty", "")
	ctx := newAPIContext(t, "POST", "/api/admin/services/import", []byte(`{"services":[]}`),
		withClaims(adminClaims(shop.ID)))
	status, body := runHandler(t, importServicesHandler, ctx)
	if status != 400 {
		t.Errorf("空数组应 400，实际 %d body=%s", status, body)
	}
	mustContain(t, body, "不能为空")
}

func TestImportServices_TooMany(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-import-many", "")
	// 构造 101 条
	var sb strings.Builder
	sb.WriteString(`{"services":[`)
	for i := 0; i < 101; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"name":"s","estimated_min":30}`)
	}
	sb.WriteString(`]}`)

	ctx := newAPIContext(t, "POST", "/api/admin/services/import", []byte(sb.String()),
		withClaims(adminClaims(shop.ID)))
	status, body := runHandler(t, importServicesHandler, ctx)
	if status != 400 {
		t.Errorf("超 100 条应 400，实际 %d body=%s", status, body)
	}
	mustContain(t, body, "最多")
}

func TestImportServices_ValidationFailures(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-import-val", "")
	body := `{"services":[
		{"name":"","estimated_min":30},
		{"name":"ok-1","estimated_min":30},
		{"name":"bad-min","estimated_min":0},
		{"name":"bad-max","estimated_min":999},
		{"name":"` + strings.Repeat("长", 33) + `","estimated_min":30}
	]}`
	ctx := newAPIContext(t, "POST", "/api/admin/services/import", []byte(body),
		withClaims(adminClaims(shop.ID)))
	status, resp := runHandler(t, importServicesHandler, ctx)
	if status != 200 {
		t.Fatalf("部分失败应仍 200，实际 %d resp=%s", status, resp)
	}
	mustContain(t, resp, `"success_count":1`)
	mustContain(t, resp, `"failed_count":4`)
	mustContain(t, resp, `"ok-1"`)
}

func TestImportServices_DuplicateNameSkipped(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-import-dup", "")
	// 预存一个 "剪发"
	storage.CreateService(storage.WithCtx(), shop.ID, "剪发", 30, "30-50")

	body := `{"services":[{"name":"剪发","estimated_min":30},{"name":"烫发","estimated_min":90}]}`
	ctx := newAPIContext(t, "POST", "/api/admin/services/import", []byte(body),
		withClaims(adminClaims(shop.ID)))
	status, resp := runHandler(t, importServicesHandler, ctx)
	if status != 200 {
		t.Fatalf("应 200，实际 %d resp=%s", status, resp)
	}
	mustContain(t, resp, `"success_count":1`)
	mustContain(t, resp, `"skipped_count":1`)
	mustContain(t, resp, `"skipped"`)
}

func TestImportServices_ReplaceTrueDeactivatesExisting(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-import-replace", "")
	oldSvc, _ := storage.CreateService(storage.WithCtx(), shop.ID, "old-svc", 30, "10")
	oldSvc2, _ := storage.CreateService(storage.WithCtx(), shop.ID, "old-svc-2", 30, "20")

	body := `{"replace":true,"services":[{"name":"new-svc","estimated_min":45}]}`
	ctx := newAPIContext(t, "POST", "/api/admin/services/import", []byte(body),
		withClaims(adminClaims(shop.ID)))
	status, resp := runHandler(t, importServicesHandler, ctx)
	if status != 200 {
		t.Fatalf("应 200，实际 %d resp=%s", status, resp)
	}
	mustContain(t, resp, `"replaced_count":2`)
	mustContain(t, resp, `"success_count":1`)

	// 校验旧服务 is_active = false
	var old1, old2 storage.Service
	storage.DB.Where("id = ?", oldSvc.ID).First(&old1)
	storage.DB.Where("id = ?", oldSvc2.ID).First(&old2)
	if old1.IsActive || old2.IsActive {
		t.Errorf("replace=true 应下架旧服务：old1.IsActive=%v old2.IsActive=%v", old1.IsActive, old2.IsActive)
	}
}

func TestImportServices_AllNewSucceed(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-import-allnew", "")
	body := `{"services":[
		{"name":"美甲","estimated_min":60,"price_range":"80-150"},
		{"name":"手部护理","estimated_min":45}
	]}`
	ctx := newAPIContext(t, "POST", "/api/admin/services/import", []byte(body),
		withClaims(adminClaims(shop.ID)))
	status, resp := runHandler(t, importServicesHandler, ctx)
	if status != 200 {
		t.Fatalf("应 200，实际 %d resp=%s", status, resp)
	}
	mustContain(t, resp, `"success_count":2`)
	mustContain(t, resp, `"skipped_count":0`)
	mustContain(t, resp, `"failed_count":0`)
	mustContain(t, resp, "美甲")
	mustContain(t, resp, "手部护理")
}

// ============================================================
// helpers
// ============================================================

func mustContain(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected to contain %q, got: %s", substr, s)
	}
}

// itoa 把 uint64 转 string（避免依赖 strconv 在 import 段出现）
func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
