package api

// shops_test.go —— /api/admin/shops 测试（v4.12.1 multi_store gate 实战）
//
// 覆盖：
//   - basic plan → 403 + feature_required（gate 实战）
//   - flagship plan → 200 + 列本店
//   - flagship plan + 4 分店 → 第 5 个 OK（flagship 限 5），第 6 个 402
//   - frozen → 402
//   - 缺 name → 400
//   - 从分店建分店 → 403（必须主店）
//   - staff → 403（无 PermViewPlan）
//   - 跨店隔离：shopB 看到的是自己 group，不是 shopA 的

import (
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

func TestListShops_OwnerBasic_OK(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-list-basic", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanBasic)
	makeActiveSub(t, shop.ID, storage.PlanBasic, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/shops", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, listShopsHandler, ctx)

	if status != 200 {
		t.Fatalf("basic 应能 list（feature 只 gate POST）, got %d body=%s", status, body)
	}
	// basic 限 1 店，当前 count = 1
	if !strings.Contains(body, `"current_count":1`) {
		t.Errorf("basic 应只 1 店, body=%s", body)
	}
	if !strings.Contains(body, `"max_shops":1`) {
		t.Errorf("basic plan 应 max_shops=1, body=%s", body)
	}
}

func TestCreateShop_OwnerBasic_Forbidden(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-create-basic", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanBasic)
	makeActiveSub(t, shop.ID, storage.PlanBasic, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "POST", "/api/admin/shops",
		[]byte(`{"name":"分店甲","address":"中关村南大街5号"}`))
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, createShopHandler, ctx)

	if status != 403 {
		t.Fatalf("basic plan 应 403, got %d body=%s", status, body)
	}
	if !strings.Contains(body, storage.FeatureMultiStore) {
		t.Errorf("应含 feature_required=%s, 实际: %s", storage.FeatureMultiStore, body)
	}
}

func TestCreateShop_OwnerFlagship_OK(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-create-flagship", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanFlagship)
	makeActiveSub(t, shop.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "POST", "/api/admin/shops",
		[]byte(`{"name":"分店甲","address":"中关村南大街5号"}`))
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, createShopHandler, ctx)

	if status != 200 {
		t.Fatalf("flagship 应 200, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "分店创建成功") {
		t.Errorf("应含成功提示, 实际: %s", body)
	}
	if !strings.Contains(body, "分店甲") {
		t.Errorf("应含新店名, 实际: %s", body)
	}
}

func TestCreateShop_Flagship_ExceedLimit_402(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-create-limit", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanFlagship)
	makeActiveSub(t, shop.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	// 先建 4 个分店（flagship 限 5：含主店 = 5）
	for i := 0; i < 4; i++ {
		_, err := storage.CreateSubsidiaryShop(t.Context(), shop.ID, "sub"+string(rune('a'+i)), "")
		if err != nil {
			t.Fatalf("建分店 %d: %v", i, err)
		}
	}

	// 第 5 次（建第 5 个分店 = 6 个总，超 flagship 限 5）
	ctx := newAPIContext(t, "POST", "/api/admin/shops",
		[]byte(`{"name":"超限分店"}`))
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, createShopHandler, ctx)

	if status != 402 {
		t.Fatalf("超 plan limit 应 402, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "shops") {
		t.Errorf("应指明资源 shops, 实际: %s", body)
	}
	if !strings.Contains(body, storage.PlanFlagship) {
		t.Errorf("应含 plan, 实际: %s", body)
	}
}

func TestCreateShop_FrozenPlan_402(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-create-frozen", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanFlagship)
	makeActiveSub(t, shop.ID, storage.PlanFlagship, time.Now().Add(-30*24*time.Hour)) // frozen

	ctx := newAPIContext(t, "POST", "/api/admin/shops",
		[]byte(`{"name":"分店甲"}`))
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, _ := runWithPermAndPlan(t, storage.PermViewPlan, createShopHandler, ctx)

	if status != 402 {
		t.Fatalf("frozen plan 应 402, got %d", status)
	}
}

func TestCreateShop_EmptyName_400(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-create-noname", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanFlagship)
	makeActiveSub(t, shop.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "POST", "/api/admin/shops",
		[]byte(`{"name":"   ","address":"x"}`))
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, createShopHandler, ctx)

	if status != 400 {
		t.Fatalf("空名应 400, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "名") {
		t.Errorf("应提示名字错, 实际: %s", body)
	}
}

func TestCreateShop_FromSubsidiary_403(t *testing.T) {
	setupAPITestDB(t)
	parent := storage.MakeShop(t, "shop-parent", "")
	// 手动建分店
	sub, err := storage.CreateSubsidiaryShop(t.Context(), parent.ID, "分店甲", "")
	if err != nil {
		t.Fatalf("建分店: %v", err)
	}
	owner := storage.MakeAdminWithRole(t, sub.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, sub.ID, storage.PlanFlagship)
	makeActiveSub(t, sub.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "POST", "/api/admin/shops",
		[]byte(`{"name":"分店乙"}`))
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, createShopHandler, ctx)

	if status != 403 {
		t.Fatalf("从分店建分店应 403, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "主店") {
		t.Errorf("应提示必须主店, 实际: %s", body)
	}
}

func TestCreateShop_NoPerm_403(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-create-noperm", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanFlagship)
	makeActiveSub(t, shop.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "POST", "/api/admin/shops",
		[]byte(`{"name":"分店甲"}`))
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	// 传空 perm → RequirePerm 返 403
	status, _ := runWithPermAndPlan(t, "", createShopHandler, ctx)

	if status != 403 {
		t.Fatalf("无 perm 应 403, got %d", status)
	}
}

// TestListShops_CrossShopIsolation: shopB 看到自己 group（仅自己），不应看到 shopA 分店
func TestListShops_CrossShopIsolation(t *testing.T) {
	setupAPITestDB(t)
	shopA := storage.MakeShop(t, "shop-iso-a", "")
	shopB := storage.MakeShop(t, "shop-iso-b", "")
	ownerA := storage.MakeAdminWithRole(t, shopA.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shopA.ID, storage.PlanFlagship)
	makeActiveSub(t, shopA.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	// shopA 建一个分店
	_, err := storage.CreateSubsidiaryShop(t.Context(), shopA.ID, "A 分店", "")
	if err != nil {
		t.Fatalf("建分店: %v", err)
	}

	// 用 shopA owner 列 → 应看到 2（主+分）
	ctxA := newAPIContext(t, "GET", "/api/admin/shops", nil)
	setClaimsForAdmin(ctxA, ownerA.ID, ownerA.ShopID, ownerA.Role)
	statusA, bodyA := runWithPermAndPlan(t, storage.PermViewPlan, listShopsHandler, ctxA)
	if statusA != 200 {
		t.Fatalf("shopA list 应 200, got %d", statusA)
	}
	if !strings.Contains(bodyA, `"current_count":2`) {
		t.Errorf("shopA 应有 2 店（含分店）, body=%s", bodyA)
	}

	// shopB 也列 → 应只有 1（自己，跨店隔离）
	ownerB := storage.MakeAdminWithRole(t, shopB.ID, storage.ShortTestUsername(t, "owner2"), "owner")
	setShopPlan(t, shopB.ID, storage.PlanFlagship)
	makeActiveSub(t, shopB.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))
	ctxB := newAPIContext(t, "GET", "/api/admin/shops", nil)
	setClaimsForAdmin(ctxB, ownerB.ID, ownerB.ShopID, ownerB.Role)
	statusB, bodyB := runWithPermAndPlan(t, storage.PermViewPlan, listShopsHandler, ctxB)
	if statusB != 200 {
		t.Fatalf("shopB list 应 200, got %d", statusB)
	}
	if !strings.Contains(bodyB, `"current_count":1`) {
		t.Errorf("shopB 应只有 1 店（不应看到 shopA 分店）, body=%s", bodyB)
	}
	if strings.Contains(bodyB, "A 分店") {
		t.Errorf("shopB 不应看到 shopA 分店名, body=%s", bodyB)
	}
}