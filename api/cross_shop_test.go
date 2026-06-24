package api

// cross_shop_test.go
//
// 跨店隔离集成测试（v4.10.1 W1 补测试计划）
//
// 目标：path-param handler + 关键 list handler，验证"店主 A 不能操作店主 B 的资源"。
//
// 防御模式（按 handler 现状）：
//   - 显式 403：members.go 三个 handler (changeRole / resetPassword / disable) + leave 取消
//   - 显式 404：barber_handlers.go (softDelete / activate) → "伪装不存在"，不泄漏存在性
//   - 显式 400：createBarberLeave 跨店 barber → "不属于本店"
//   - 隐式（list）：list handler 用 storage.ListXxxByShop(shopID) 或 in-memory filter
//
// 关键断言：
//   - 操作后，目标资源**未被修改**（密码未改 / status 未变 / leave 未取消）
//   - 错误信息不泄漏存在性（"理发师不存在" or "无权操作"，不能是 "owner 店 B 已存在但你没权限"）

import (
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

// ============================================================
// helpers
// ============================================================

// makeShopWithOwner 建一个店 + 一个 owner，返回 owner
func makeShopWithOwner(t *testing.T, shopID, ownerName string) *storage.ShopAdmin {
	t.Helper()
	shop := storage.MakeShop(t, shopID, "")
	return storage.MakeAdminWithRole(t, shop.ID, ownerName, "owner")
}

// assertAdminUnchanged 验证 admin 字段没被跨店操作改动
func assertAdminUnchanged(t *testing.T, admin *storage.ShopAdmin, expectStatus, expectRole string) {
	t.Helper()
	var got storage.ShopAdmin
	storage.DB.First(&got, admin.ID)
	if got.Status != expectStatus {
		t.Errorf("admin#%d status = %q, want %q (跨店操作不应改 status)", admin.ID, got.Status, expectStatus)
	}
	if got.Role != expectRole {
		t.Errorf("admin#%d role = %q, want %q (跨店操作不应改 role)", admin.ID, got.Role, expectRole)
	}
}

// ============================================================
// members: resetPassword / disable 跨店 → 403
// ============================================================

func TestCrossShop_ResetPassword_Forbidden(t *testing.T) {
	setupAPITestDB(t)
	ownerA := makeShopWithOwner(t, "shop-A-cspw", "ownerA-cspw")
	ownerB := makeShopWithOwner(t, "shop-B-cspw", "ownerB-cspw")

	ctx := newAPIContext(t, "POST", "/api/admin/members/"+itoa(ownerB.ID)+"/reset-password",
		[]byte(`{"new_password":"hacked123"}`),
		withPathParam("id", itoa(ownerB.ID)))
	setClaimsForAdmin(ctx, ownerA.ID, ownerA.ShopID, ownerA.Role)

	status, body := runHandler(t, resetMemberPasswordHandler, ctx)
	if status != 403 {
		t.Errorf("跨店重置密码应 403, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "无权") {
		t.Errorf("错误信息应含 '无权', got: %s", body)
	}
	// 验证 B 密码未变（用原密码还能登入）
	if !storage.VerifyAdminPassword(ownerB, "testpass") {
		t.Error("B 店主密码被改了，跨店保护失败")
	}
}

func TestCrossShop_DisableMember_Forbidden(t *testing.T) {
	setupAPITestDB(t)
	ownerA := makeShopWithOwner(t, "shop-A-csdis", "ownerA-csdis")
	ownerB := makeShopWithOwner(t, "shop-B-csdis", "ownerB-csdis")

	ctx := newAPIContext(t, "DELETE", "/api/admin/members/"+itoa(ownerB.ID), nil,
		withPathParam("id", itoa(ownerB.ID)))
	setClaimsForAdmin(ctx, ownerA.ID, ownerA.ShopID, ownerA.Role)

	status, body := runHandler(t, disableMemberHandler, ctx)
	if status != 403 {
		t.Errorf("跨店停用应 403, got %d body=%s", status, body)
	}
	assertAdminUnchanged(t, ownerB, "active", "owner")
}

// ============================================================
// barber: softDelete / activate 跨店 → 404（伪装不存在）
// ============================================================

func TestCrossShop_SoftDeleteBarber_NotFound(t *testing.T) {
	setupAPITestDB(t)
	ownerA := makeShopWithOwner(t, "shop-A-csbd", "ownerA-csbd")
	ownerB := makeShopWithOwner(t, "shop-B-csbd", "ownerB-csbd")
	// 店 B 有一个 barber
	barberB := storage.MakeBarber(t, "barber-b-csbd", ownerB.ShopID, "Bob")

	ctx := newAPIContext(t, "DELETE", "/api/admin/barbers/"+barberB.ID, nil,
		withPathParam("id", barberB.ID))
	setClaimsForAdmin(ctx, ownerA.ID, ownerA.ShopID, ownerA.Role)

	status, body := runHandler(t, softDeleteBarberHandler, ctx)
	if status != 404 {
		t.Errorf("跨店删 barber 应 404（伪装不存在）, got %d body=%s", status, body)
	}
	// 验证 barber 仍 active
	var got storage.Barber
	storage.DB.First(&got, "id = ?", barberB.ID)
	if !got.Active {
		t.Error("跨店 DELETE 不应把 B 店的 barber 删了")
	}
}

func TestCrossShop_ActivateBarber_NotFound(t *testing.T) {
	setupAPITestDB(t)
	ownerA := makeShopWithOwner(t, "shop-A-csba", "ownerA-csba")
	ownerB := makeShopWithOwner(t, "shop-B-csba", "ownerB-csba")
	barberB := storage.MakeBarber(t, "barber-b-csba", ownerB.ShopID, "Bob")
	// 把 B barber 改成 inactive（软删）
	storage.DB.Model(barberB).Update("active", false)

	ctx := newAPIContext(t, "POST", "/api/admin/barbers/"+barberB.ID+"/activate", nil,
		withPathParam("id", barberB.ID))
	setClaimsForAdmin(ctx, ownerA.ID, ownerA.ShopID, ownerA.Role)

	status, _ := runHandler(t, activateBarberHandler, ctx)
	if status != 404 {
		t.Errorf("跨店激活 barber 应 404, got %d", status)
	}
	// 验证仍 inactive
	var got storage.Barber
	storage.DB.First(&got, "id = ?", barberB.ID)
	if got.Active {
		t.Error("跨店 POST /activate 不应把 B 店的 barber 激活")
	}
}

// ============================================================
// leave: cancel 跨店 → 403
// ============================================================

func TestCrossShop_CancelBarberLeave_Forbidden(t *testing.T) {
	setupAPITestDB(t)
	ownerA := makeShopWithOwner(t, "shop-A-csbl", "ownerA-csbl")
	ownerB := makeShopWithOwner(t, "shop-B-csbl", "ownerB-csbl")
	barberB := storage.MakeBarber(t, "barber-b-csbl", ownerB.ShopID, "Bob")

	// B 店建一条 leave
	future := time.Now().Add(24 * time.Hour)
	leaveB := storage.MakeBarberLeave(t, ownerB.ShopID, barberB.ID,
		future, future.Add(2*time.Hour), storage.LeaveActionCancel)

	ctx := newAPIContext(t, "POST",
		"/api/admin/barber/"+barberB.ID+"/leaves/"+leaveB.ID+"/cancel", nil,
		withPathParam("id", barberB.ID),
		withPathParam("leaveID", leaveB.ID))
	setClaimsForAdmin(ctx, ownerA.ID, ownerA.ShopID, ownerA.Role)

	status, body := runHandler(t, cancelBarberLeaveHandler, ctx)
	if status != 403 {
		t.Errorf("跨店 cancel barber leave 应 403, got %d body=%s", status, body)
	}
	// 验证 leave 未取消
	var got storage.BarberLeave
	storage.DB.First(&got, "id = ?", leaveB.ID)
	if got.Status == storage.LeaveStatusCancelled {
		t.Error("跨店 cancel 不应把 B 店的 leave 取消了")
	}
}

func TestCrossShop_CancelLeave_Forbidden(t *testing.T) {
	setupAPITestDB(t)
	ownerA := makeShopWithOwner(t, "shop-A-cslv", "ownerA-cslv")
	ownerB := makeShopWithOwner(t, "shop-B-cslv", "ownerB-cslv")
	barberB := storage.MakeBarber(t, "barber-b-cslv", ownerB.ShopID, "Bob")
	future := time.Now().Add(24 * time.Hour)
	leaveB := storage.MakeBarberLeave(t, ownerB.ShopID, barberB.ID,
		future, future.Add(2*time.Hour), storage.LeaveActionCancel)

	body := `{"leave_id":"` + leaveB.ID + `"}`
	ctx := newAPIContext(t, "POST", "/api/admin/leave/cancel", []byte(body))
	setClaimsForAdmin(ctx, ownerA.ID, ownerA.ShopID, ownerA.Role)

	status, _ := runHandler(t, cancelLeaveHandler, ctx)
	if status != 403 {
		t.Errorf("跨店 POST /leave/cancel 应 403, got %d", status)
	}
	var got storage.BarberLeave
	storage.DB.First(&got, "id = ?", leaveB.ID)
	if got.Status == storage.LeaveStatusCancelled {
		t.Error("跨店 cancel 不应把 B 店的 leave 取消了")
	}
}

// ============================================================
// create leave: 跨店 barber → 400 "不属于本店"
// ============================================================

func TestCrossShop_CreateLeave_BarberFromOtherShop_BadRequest(t *testing.T) {
	setupAPITestDB(t)
	ownerA := makeShopWithOwner(t, "shop-A-cscl", "ownerA-cscl")
	barberB := storage.MakeBarber(t, "barber-b-cscl", "shop-B-cscl", "Bob")

	future := time.Now().Add(24 * time.Hour)
	futureEnd := future.Add(2 * time.Hour)

	body := `{"barber_id":"` + barberB.ID + `","start_at":"` + future.UTC().Format(time.RFC3339) +
		`","end_at":"` + futureEnd.UTC().Format(time.RFC3339) + `","reason":"x","action":"cancel"}`

	ctx := newAPIContext(t, "POST", "/api/admin/leave/create", []byte(body))
	setClaimsForAdmin(ctx, ownerA.ID, ownerA.ShopID, ownerA.Role)

	status, respBody := runHandler(t, createLeaveHandler, ctx)
	if status != 400 {
		t.Errorf("跨店 barber 建 leave 应 400, got %d body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "不属于本店") {
		t.Errorf("错误信息应含 '不属于本店', got: %s", respBody)
	}
	// 验证 A 店没建出 leave（A.shopID == ownerA.ShopID）
	var n int64
	storage.DB.Model(&storage.BarberLeave{}).Where("shop_id = ?", ownerA.ShopID).Count(&n)
	if n != 0 {
		t.Errorf("跨店 create leave 不应在 A 店落库, count = %d", n)
	}
}

// ============================================================
// list: listMembers 跨店 → 看不到对方成员
// ============================================================

func TestCrossShop_ListMembers_OnlySeesOwnShop(t *testing.T) {
	setupAPITestDB(t)
	ownerA := makeShopWithOwner(t, "shop-A-cslm", "ownerA-cslm")
	// ownerB 不需要引用——只需要它的 username 字符串出现在 B 店的 row
	// （用于断言"A 的 list 不应含 B 店主名"）
	_ = makeShopWithOwner(t, "shop-B-cslm", "ownerB-cslm")

	ctx := newAPIContext(t, "GET", "/api/admin/members", nil)
	setClaimsForAdmin(ctx, ownerA.ID, ownerA.ShopID, ownerA.Role)

	status, body := runWithPerm(t, storage.PermManageMembers, listMembersHandler, ctx)
	if status != 200 {
		t.Fatalf("A 店主 list 自己店成员应 200, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "ownerA-cslm") {
		t.Errorf("A 应看到自己, body=%s", body)
	}
	if strings.Contains(body, "ownerB-cslm") {
		t.Errorf("A 不应看到 B 店主, body=%s", body)
	}
}

// ============================================================
// list: listBarbers 跨店 → 看不到对方 barber
// ============================================================

func TestCrossShop_ListBarbers_OnlySeesOwnShop(t *testing.T) {
	setupAPITestDB(t)
	ownerA := makeShopWithOwner(t, "shop-A-cslb", "ownerA-cslb")
	ownerB := makeShopWithOwner(t, "shop-B-cslb", "ownerB-cslb")
	_ = storage.MakeBarber(t, "barber-a-cslb", ownerA.ShopID, "Alice")
	_ = storage.MakeBarber(t, "barber-b-cslb", ownerB.ShopID, "Bob")

	ctx := newAPIContext(t, "GET", "/api/admin/barbers?include_inactive=true", nil)
	setClaimsForAdmin(ctx, ownerA.ID, ownerA.ShopID, ownerA.Role)

	status, body := runHandler(t, listBarbersHandler, ctx)
	if status != 200 {
		t.Fatalf("A 店主 list 自己店 barber 应 200, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "Alice") {
		t.Errorf("A 应看到自己的 Alice, body=%s", body)
	}
	if strings.Contains(body, "Bob") {
		t.Errorf("A 不应看到 B 店的 Bob, body=%s", body)
	}
}
