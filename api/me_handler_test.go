package api

// me_handler_test.go
//
// v4.11.1：meHandler 返 permissions []string，前端用真 perm 矩阵驱动 nav 可见性
// 这是结构性修法，防 v4.7/v4.10.1/v4.11 三次漂过的"前端 ROLES_REQUIRED 字典漂"债务。
//
// 覆盖：
//   - owner 登录 → me.permissions 含 owner 该有的 perm
//   - staff 登录 → me.permissions 不含 staff 故意禁的 perm
//   - platform_admin 登录 → me.permissions 含 AllPermissions
//   - me.permissions 跟 storage.GetRolePermissions 返的一致（真理之源对齐）

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

// runMeHandler 直接调 meHandler 拿响应，绕过 router
func runMeHandler(t *testing.T, claims *auth.Claims) map[string]any {
	t.Helper()
	reqCtx := newAPIContext(t, "GET", "/api/admin/me", nil)
	if claims != nil {
		setClaimsForAdmin(reqCtx, claims.AdminID, claims.ShopID, claims.Role)
	}
	meHandler(context.Background(), reqCtx)
	body := string(reqCtx.Response.Body())
	if reqCtx.Response.StatusCode() != 200 {
		t.Fatalf("meHandler 状态码 %d body=%s", reqCtx.Response.StatusCode(), body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("meHandler 返非 JSON: %v body=%s", err, body)
	}
	return out
}

// TestMeHandler_OwnerHasOwnerPerms 验证 owner 登录后 me 返的 permissions 包含 owner 该有的关键 perm
func TestMeHandler_OwnerHasOwnerPerms(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-meowner", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), storage.RoleOwner)

	claims := &auth.Claims{AdminID: owner.ID, ShopID: owner.ShopID, Role: owner.Role}
	out := runMeHandler(t, claims)

	perms, ok := out["permissions"].([]any)
	if !ok {
		t.Fatalf("me.permissions 应该是数组，实际 %T: %v", out["permissions"], out["permissions"])
	}
	got := make(map[string]bool, len(perms))
	for _, p := range perms {
		if s, ok := p.(string); ok {
			got[s] = true
		}
	}
	// owner 该有的关键 perm
	wantPresent := []string{
		storage.PermViewDashboard,
		storage.PermViewAppointments,
		storage.PermEditAppointments,
		storage.PermViewCustomers,
		storage.PermViewWeeklyReport,
		storage.PermEditShop,
		storage.PermEditServices,
		storage.PermManageMembers,
		storage.PermViewNotifications,
		storage.PermRetryNotifications,
	}
	for _, p := range wantPresent {
		if !got[p] {
			t.Errorf("owner 应有 %s，但 me.permissions 里没", p)
		}
	}
	// owner 故意没有的（v4.10.1 收紧）
	wantAbsent := []string{
		storage.PermViewChainDashboard,
		storage.PermViewSubscription,
		storage.PermManageSubscription,
	}
	for _, p := range wantAbsent {
		if got[p] {
			t.Errorf("owner 不应有 %s（v4.10.1 收紧归 platform_admin），但 me.permissions 里有", p)
		}
	}
	// 数量校验：owner 应该有 len(DefaultRolePermissions[RoleOwner]) 个
	wantCount := len(storage.DefaultRolePermissions[storage.RoleOwner])
	if len(perms) != wantCount {
		t.Errorf("owner permissions 数量 = %d, want %d（跟 storage.DefaultRolePermissions 矩阵对不上）", len(perms), wantCount)
	}
}

// TestMeHandler_StaffLacksOwnerPerms 验证 staff 登录后 me 返的 permissions 不含 staff 故意禁的 7 个
func TestMeHandler_StaffLacksOwnerPerms(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-mestaff", "")
	staff := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "staff"), storage.RoleStaff)

	claims := &auth.Claims{AdminID: staff.ID, ShopID: staff.ShopID, Role: staff.Role}
	out := runMeHandler(t, claims)

	perms, _ := out["permissions"].([]any)
	got := make(map[string]bool, len(perms))
	for _, p := range perms {
		if s, ok := p.(string); ok {
			got[s] = true
		}
	}
	// staff 故意禁的 7 个 perm 必须不出现
	staffForbidden := []string{
		storage.PermViewWeeklyReport,    // v4.10.1 收紧：经营数据敏感
		storage.PermViewChainDashboard,  // v4.10.1 收紧：跨店数据
		storage.PermEditShop,            // v4.7：营业时间误操作影响大
		storage.PermEditServices,        // v4.7：服务目录/价格敏感
		storage.PermViewSubscription,    // v4.10.1 收紧：订阅归 platform_admin
		storage.PermManageSubscription,  // v4.10.1 收紧：续费归 platform_admin
		storage.PermManageMembers,       // v4.7：成员管理必须有 owner 权限
	}
	for _, p := range staffForbidden {
		if got[p] {
			t.Errorf("staff 不应有 %s，但 me.permissions 里有——v4.7/v4.10.1 漂过类似问题", p)
		}
	}
	// staff 该有的关键 perm
	staffHas := []string{
		storage.PermViewDashboard,
		storage.PermViewAppointments,
		storage.PermEditAppointments,
		storage.PermViewCustomers,
		storage.PermCreateBarberLeave,
		storage.PermViewNotifications,
		storage.PermRetryNotifications,
	}
	for _, p := range staffHas {
		if !got[p] {
			t.Errorf("staff 应有 %s，但 me.permissions 里没", p)
		}
	}
}

// TestMeHandler_PlatformAdminHasAll 验证 platform_admin 登录后 me 返的 permissions = AllPermissions
func TestMeHandler_PlatformAdminHasAll(t *testing.T) {
	setupAPITestDB(t)
	// platform_admin 不需要 shop，但 meHandler 拿 shop 会失败，注入空字符串兜底
	claims := &auth.Claims{AdminID: 999, ShopID: "", Role: storage.RolePlatformAdmin}
	out := runMeHandler(t, claims)

	perms, _ := out["permissions"].([]any)
	got := make(map[string]bool, len(perms))
	for _, p := range perms {
		if s, ok := p.(string); ok {
			got[s] = true
		}
	}
	// platform_admin 应该含 AllPermissions 全部
	for _, p := range storage.AllPermissions {
		if !got[p] {
			t.Errorf("platform_admin 应有 %s，但 me.permissions 里没", p)
		}
	}
	if len(perms) != len(storage.AllPermissions) {
		t.Errorf("platform_admin permissions 数量 = %d, want %d（= len AllPermissions）", len(perms), len(storage.AllPermissions))
	}
}

// TestMeHandler_PermsMatchStorageMatrix 交叉验证：me 返的 perm 数 == storage.GetRolePermissions 返的
//
// 真理之源：storage.DefaultRolePermissions
// me.permissions 必须跟它 1:1 对齐，否则前端 nav 跟后端 endpoint 中间件不一致
func TestMeHandler_PermsMatchStorageMatrix(t *testing.T) {
	setupAPITestDB(t)
	cases := []struct {
		role      string
		wantCount int
	}{
		{storage.RoleOwner, len(storage.DefaultRolePermissions[storage.RoleOwner])},
		{storage.RoleStaff, len(storage.DefaultRolePermissions[storage.RoleStaff])},
		{storage.RolePlatformAdmin, len(storage.AllPermissions)},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			claims := &auth.Claims{AdminID: 1, ShopID: "shop-x", Role: tc.role}
			out := runMeHandler(t, claims)
			perms, _ := out["permissions"].([]any)
			if len(perms) != tc.wantCount {
				t.Errorf("%s me.permissions 数量 = %d, want %d（跟 storage.DefaultRolePermissions 不一致 = 漂）",
					tc.role, len(perms), tc.wantCount)
			}
			// 进一步：每个 perm 都应该是 storage 认识的
			for _, p := range perms {
				ps, _ := p.(string)
				found := false
				for _, known := range storage.AllPermissions {
					if known == ps {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("%s me.permissions 含未知 perm: %s（可能 storage 已删）", tc.role, ps)
				}
			}
		})
	}
}

// TestMeHandler_NoClaims 验证无 session 时返 401（v4.11.1 不该破这行为）
func TestMeHandler_NoClaims(t *testing.T) {
	setupAPITestDB(t)
	reqCtx := newAPIContext(t, "GET", "/api/admin/me", nil)
	// 不 setClaims
	meHandler(context.Background(), reqCtx)
	if reqCtx.Response.StatusCode() != 401 {
		t.Errorf("无 claims 应 401, got %d", reqCtx.Response.StatusCode())
	}
	if !strings.Contains(string(reqCtx.Response.Body()), "unauthorized") {
		t.Errorf("错误信息应含 'unauthorized', got: %s", string(reqCtx.Response.Body()))
	}
}
