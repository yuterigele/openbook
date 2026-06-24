package api

// change_password_test.go —— POST /api/admin/change-password 测试（v4.12.1）
//
// v4.12.1 安全 fix：之前 handler 不校验旧密码（任何人有 JWT 都能改）。
// 现强制要求 old_password（bcrypt compare），新密码至少 6 位。
//
// 覆盖：
//   - 缺旧密码 → 400
//   - 旧密码错 → 401
//   - 新密码 < 6 位 → 400
//   - 新旧密码相同 → 400
//   - 正常改 → 200（再 verify 新密码应能登录）
//   - 无 perm → 403

import (
	"strings"
	"testing"

	"github.com/yuterigele/openbook/storage"
)

func TestChangePassword_MissingOld_400(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-cp-missing-old", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")

	req := newAPIContext(t, "POST", "/api/admin/change-password", []byte(`{"new_password": "newpass123"}`))
	setClaimsForAdmin(req, owner.ID, owner.ShopID, owner.Role)

	status, respBody := runWithPerm(t, storage.PermChangeOwnPassword, changePasswordHandler, req)

	if status != 400 {
		t.Fatalf("缺旧密码应 400, got %d body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "旧密码") {
		t.Errorf("应提示旧密码, 实际: %s", respBody)
	}
}

func TestChangePassword_WrongOld_401(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-cp-wrong-old", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")

	req := newAPIContext(t, "POST", "/api/admin/change-password",
		[]byte(`{"old_password": "wrong-old", "new_password": "newpass123"}`))
	setClaimsForAdmin(req, owner.ID, owner.ShopID, owner.Role)

	status, respBody := runWithPerm(t, storage.PermChangeOwnPassword, changePasswordHandler, req)

	if status != 401 {
		t.Fatalf("旧密码错应 401, got %d body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "旧密码错误") {
		t.Errorf("应提示旧密码错误, 实际: %s", respBody)
	}
}

func TestChangePassword_TooShort_400(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-cp-short", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")

	req := newAPIContext(t, "POST", "/api/admin/change-password",
		[]byte(`{"old_password": "testpass", "new_password": "123"}`))
	setClaimsForAdmin(req, owner.ID, owner.ShopID, owner.Role)

	status, respBody := runWithPerm(t, storage.PermChangeOwnPassword, changePasswordHandler, req)

	if status != 400 {
		t.Fatalf("新密码 < 6 位应 400, got %d body=%s", status, respBody)
	}
}

func TestChangePassword_SameAsOld_400(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-cp-same", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")

	req := newAPIContext(t, "POST", "/api/admin/change-password",
		[]byte(`{"old_password": "testpass", "new_password": "testpass"}`))
	setClaimsForAdmin(req, owner.ID, owner.ShopID, owner.Role)

	status, respBody := runWithPerm(t, storage.PermChangeOwnPassword, changePasswordHandler, req)

	if status != 400 {
		t.Fatalf("新旧密码相同应 400, got %d body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "相同") {
		t.Errorf("应提示新旧密码相同, 实际: %s", respBody)
	}
}

func TestChangePassword_OK_VerifyNew(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-cp-ok", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")

	// MakeAdminWithRole 默认 password = "testpass"（看 storage/testhelpers.go:229）
	req := newAPIContext(t, "POST", "/api/admin/change-password",
		[]byte(`{"old_password": "testpass", "new_password": "newpass456"}`))
	setClaimsForAdmin(req, owner.ID, owner.ShopID, owner.Role)

	status, respBody := runWithPerm(t, storage.PermChangeOwnPassword, changePasswordHandler, req)

	if status != 200 {
		t.Fatalf("正常改应 200, got %d body=%s", status, respBody)
	}

	// verify 新密码生效 + 旧密码失效
	admin, err := storage.FindShopAdminByID(t.Context(), owner.ID)
	if err != nil {
		t.Fatalf("查 admin: %v", err)
	}
	if !storage.VerifyAdminPassword(admin, "newpass456") {
		t.Error("新密码应能 verify 成功")
	}
	if storage.VerifyAdminPassword(admin, "testpass") {
		t.Error("旧密码应已失效")
	}
}

func TestChangePassword_NoPerm_403(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-cp-noperm", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")

	req := newAPIContext(t, "POST", "/api/admin/change-password",
		[]byte(`{"old_password": "testpass", "new_password": "newpass123"}`))
	setClaimsForAdmin(req, owner.ID, owner.ShopID, owner.Role)

	// 传空 perm → RequirePerm 返 403
	status, _ := runWithPerm(t, "", changePasswordHandler, req)

	if status != 403 {
		t.Fatalf("无 perm 应 403, got %d", status)
	}
}