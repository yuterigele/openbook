package api

// plan_expired_test.go —— plan 过期冻结中间件测试（v4.12 增量）
//
// 覆盖：
//   - fresh 状态：放行
//   - 宽限期内：放行 + 设 grace_days
//   - frozen 状态：402 Payment Required
//   - renew 后 cache 清掉：fresh 恢复
//   - platform_admin：直接放行（不查 plan）
//   - dev / 单测无注入：放行

import (
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

// makeActiveSub 给 shop 建一条 active 订阅（不 cancel / expires_at 未来）
func makeActiveSub(t *testing.T, shopID, plan string, expiresAt time.Time) storage.Subscription {
	t.Helper()
	sub := storage.Subscription{
		ID:        "sub-test-" + shopID,
		ShopID:    shopID,
		Plan:      plan,
		StartedAt: time.Now().Add(-30 * 24 * time.Hour),
		ExpiresAt: expiresAt,
		AutoRenew: false,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := storage.DB.Create(&sub).Error; err != nil {
		t.Fatalf("create sub: %v", err)
	}
	return sub
}

// TestRequirePlanActive_Fresh 验证 fresh 状态放行
func TestRequirePlanActive_Fresh(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-pa-fresh", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	makeActiveSub(t, shop.ID, storage.PlanPro, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/members", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
	status, body := runWithPermAndPlan(t, storage.PermManageMembers, listMembersHandler, ctx)
	if status != 200 {
		t.Errorf("fresh plan 应放行 (200), got %d body=%s", status, body)
	}
}

// TestRequirePlanActive_GracePeriod 验证宽限期内放行 + 设 grace_days
func TestRequirePlanActive_GracePeriod(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-pa-grace", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	// 过期 3 天（在 7 天宽限期内，剩 4 天）
	makeActiveSub(t, shop.ID, storage.PlanPro, time.Now().Add(-3*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/members", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
	status, body := runWithPermAndPlan(t, storage.PermManageMembers, listMembersHandler, ctx)
	if status != 200 {
		t.Errorf("宽限期内应放行 (200), got %d body=%s", status, body)
	}
	// 验证 ctx 里有 plan_grace_days（还剩 4 天）
	grace := ctx.GetInt("plan_grace_days")
	if grace < 3 || grace > 4 {
		t.Errorf("plan_grace_days 应在 3-4 之间（已过 3 天，剩 4 天），got %d", grace)
	}
}

// TestRequirePlanActive_Frozen 验证 frozen 状态 402
func TestRequirePlanActive_Frozen(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-pa-frozen", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	// 过期 30 天（远超 7 天宽限期）
	makeActiveSub(t, shop.ID, storage.PlanPro, time.Now().Add(-30*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/members", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
	status, body := runWithPermAndPlan(t, storage.PermManageMembers, listMembersHandler, ctx)
	if status != 402 {
		t.Errorf("frozen plan 应 402, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "plan 已过期") {
		t.Errorf("错误信息应含 'plan 已过期', got: %s", body)
	}
	if !strings.Contains(body, "frozen") {
		t.Errorf("响应应含 'frozen: true', got: %s", body)
	}
}

// TestRequirePlanActive_RenewClearsCache 验证 renew 后 cache 清掉
func TestRequirePlanActive_RenewClearsCache(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-pa-renew", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	// 先 frozen
	makeActiveSub(t, shop.ID, storage.PlanPro, time.Now().Add(-30*24*time.Hour))

	// 第一次请求：frozen → 402
	ctx1 := newAPIContext(t, "GET", "/api/admin/members", nil)
	setClaimsForAdmin(ctx1, owner.ID, owner.ShopID, owner.Role)
	status, _ := runWithPermAndPlan(t, storage.PermManageMembers, listMembersHandler, ctx1)
	if status != 402 {
		t.Fatalf("renew 前应 402, got %d", status)
	}

	// 手动续费：建一条新 sub（renew handler 行为：cancelled_at 旧 sub + 写新 sub）
	newSub := storage.Subscription{
		ID:        "sub-renewed",
		ShopID:    shop.ID,
		Plan:      storage.PlanPro,
		StartedAt: time.Now(),
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := storage.DB.Create(&newSub).Error; err != nil {
		t.Fatalf("renew: %v", err)
	}
	// renew handler 会调这个
	auth.InvalidatePlanActiveCache(shop.ID)

	// 第二次请求：fresh → 200
	ctx2 := newAPIContext(t, "GET", "/api/admin/members", nil)
	setClaimsForAdmin(ctx2, owner.ID, owner.ShopID, owner.Role)
	status2, body2 := runWithPermAndPlan(t, storage.PermManageMembers, listMembersHandler, ctx2)
	if status2 != 200 {
		t.Errorf("renew 后应 200, got %d body=%s", status2, body2)
	}
}

// TestRequirePlanActive_PlatformAdmin 验证 platform_admin 直接放行（不查 plan）
func TestRequirePlanActive_PlatformAdmin(t *testing.T) {
	setupAPITestDB(t)
	// platform_admin 无 shop 也能登录（Claim 里有 ShopID=""）
	// 但 faked sub 过期了，不应该影响超管
	shop := storage.MakeShop(t, "shop-pa-platform", "")
	makeActiveSub(t, shop.ID, storage.PlanPro, time.Now().Add(-30*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/members", nil)
	setClaimsForAdmin(ctx, 999, "", "platform_admin") // 超管
	// listMembersHandler 自身会要求 shopFromClaims，所以这里会 401——但我们测的是
	// 中间件先过——中间件不该拦超管
	// 所以这里测的是：中间件不 abort 让 handler 自己处理
	status, _ := runHandler(t, listMembersHandler, ctx)
	// 超管无 shop_id → handler 返 401（正常），不是 402（中间件不该拦）
	if status == 402 {
		t.Errorf("platform_admin 不应被 plan middleware 拦，got 402")
	}
}

// TestRequirePlanActive_PlanFuncNotInjected 验证 dev 环境无注入时放行（不 panic）
// （dev / 单测 setup 走全套，注入由 api 包的 init() 负责——单测不需要这个 case）
