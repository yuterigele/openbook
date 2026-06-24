package api

// plan_gate_test.go —— plan limit gate 端到端测试（v4.12 增量）
//
// 覆盖：
//   - basic plan 限 3 个 barber：建第 4 个应 402
//   - pro plan 限 10 个 barber：建到第 10 个 OK，第 11 个 402
//   - flagship plan 不限：建 20 个 OK
//   - 已 disabled 的 barber 不算限额

import (
	"strings"
	"testing"

	"github.com/yuterigele/openbook/storage"
)

// setShopPlan 直接改 shop.plan（不用 renew handler，绕开 monthly 流程）
func setShopPlan(t *testing.T, shopID, plan string) {
	t.Helper()
	storage.DB.Model(&storage.Shop{}).Where("id = ?", shopID).Update("plan", plan)
}

// TestCreateBarber_BasicPlan_ExceedLimit 验证 basic 限 3 个，超了 402
func TestCreateBarber_BasicPlan_ExceedLimit(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-planbasic", "")
	setShopPlan(t, shop.ID, storage.PlanBasic)
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")

	// 建 3 个 barber（basic 限额）
	for i := 0; i < 3; i++ {
		ctx := newAPIContext(t, "POST", "/api/admin/barbers",
			jsonRaw(`{"name":"b`+itoa(uint64(i))+`","skills":"剪"}`))
		setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
		status, _ := runWithPerm(t, storage.PermEditBarbers, createBarberHandler, ctx)
		if status != 200 {
			t.Fatalf("第 %d 个 barber 应建成功, got %d", i+1, status)
		}
	}

	// 第 4 个应被 plan gate 拒（402）
	ctx := newAPIContext(t, "POST", "/api/admin/barbers",
		jsonRaw(`{"name":"b4","skills":"剪"}`))
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
	status, body := runWithPerm(t, storage.PermEditBarbers, createBarberHandler, ctx)
	if status != 402 {
		t.Errorf("第 4 个 barber 应 402（超 plan 限额）, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "basic") {
		t.Errorf("错误信息应含 plan 'basic', got: %s", body)
	}
	if !strings.Contains(body, "升级") {
		t.Errorf("错误信息应提示'升级', got: %s", body)
	}
}

// TestCreateBarber_FlagshipPlan_NoLimit 验证 flagship 不限
func TestCreateBarber_FlagshipPlan_NoLimit(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-planflagship", "")
	setShopPlan(t, shop.ID, storage.PlanFlagship)
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")

	// 建 20 个（远超 basic 限 3 / pro 限 10）
	for i := 0; i < 20; i++ {
		ctx := newAPIContext(t, "POST", "/api/admin/barbers",
			jsonRaw(`{"name":"f`+itoa(uint64(i))+`","skills":"剪"}`))
		setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
		status, body := runWithPerm(t, storage.PermEditBarbers, createBarberHandler, ctx)
		if status != 200 {
			t.Fatalf("flagship 第 %d 个应建成功, got %d body=%s", i+1, status, body)
		}
	}

	// 验证 DB 实际 20 个
	n, err := storage.CountBarbersByShop(t.Context(), shop.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 20 {
		t.Errorf("flagship 应 20 个 active barber, got %d", n)
	}
}

// TestCreateBarber_ProPlan_Limit10 验证 pro 限 10（边界）
func TestCreateBarber_ProPlan_Limit10(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-planpro", "")
	setShopPlan(t, shop.ID, storage.PlanPro)
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")

	// 建 10 个（边界）
	for i := 0; i < 10; i++ {
		ctx := newAPIContext(t, "POST", "/api/admin/barbers",
			jsonRaw(`{"name":"p`+itoa(uint64(i))+`","skills":"剪"}`))
		setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
		status, body := runWithPerm(t, storage.PermEditBarbers, createBarberHandler, ctx)
		if status != 200 {
			t.Fatalf("pro 第 %d 个应建成功, got %d body=%s", i+1, status, body)
		}
	}

	// 第 11 个应 402
	ctx := newAPIContext(t, "POST", "/api/admin/barbers",
		jsonRaw(`{"name":"p10","skills":"剪"}`))
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
	status, body := runWithPerm(t, storage.PermEditBarbers, createBarberHandler, ctx)
	if status != 402 {
		t.Errorf("pro 第 11 个应 402, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "pro") {
		t.Errorf("错误信息应含 plan 'pro', got: %s", body)
	}
}

// TestCreateBarber_DisabledNotCounted 验证 disabled barber 不占限额
func TestCreateBarber_DisabledNotCounted(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-disabled-not-counted", "")
	setShopPlan(t, shop.ID, storage.PlanBasic)
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")

	// 建 3 个，删 1 个（软删）
	for i := 0; i < 3; i++ {
		ctx := newAPIContext(t, "POST", "/api/admin/barbers",
			jsonRaw(`{"name":"d`+itoa(uint64(i))+`","skills":"剪"}`))
		setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
		status, _ := runWithPerm(t, storage.PermEditBarbers, createBarberHandler, ctx)
		if status != 200 {
			t.Fatalf("建第 %d 个失败", i+1)
		}
	}
	// 软删第 1 个
	storage.DB.Model(&storage.Barber{}).
		Where("shop_id = ? AND name = ?", shop.ID, "d0").
		Update("active", false)

	// active=2，再建 1 个应 OK（变成 3，没超）
	ctx := newAPIContext(t, "POST", "/api/admin/barbers",
		jsonRaw(`{"name":"d-new","skills":"剪"}`))
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
	status, body := runWithPerm(t, storage.PermEditBarbers, createBarberHandler, ctx)
	if status != 200 {
		t.Errorf("disabled 不算限额，再建 1 个应 OK, got %d body=%s", status, body)
	}
}
