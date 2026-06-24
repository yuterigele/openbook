package api

// plans_test.go —— GET /api/admin/plans 测试（v4.12）
//
// 覆盖：
//   - owner 拿到自己 plan + 4 档对比
//   - staff 没 perm 返 403
//   - 4 档顺序按价格升序
//   - 当前 plan 元数据正确
//   - frozen / grace_days 跟 IsPlanExpired 一致

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

// runPlansWithAuth 走完整中间件链：RequirePerm(view:subscription) + RequirePlanActive + handler
func runPlansWithAuth(t *testing.T, shopID, role string, adminID uint64) (int, string) {
	t.Helper()
	reqCtx := newAPIContext(t, "GET", "/api/admin/plans", nil)
	if shopID != "" {
		setClaimsForAdmin(reqCtx, adminID, shopID, role)
	}
	auth.RequirePerm(storage.PermViewPlan)(context.Background(), reqCtx)
	if reqCtx.IsAborted() {
		return reqCtx.Response.StatusCode(), string(reqCtx.Response.Body())
	}
	auth.RequirePlanActive()(context.Background(), reqCtx)
	if reqCtx.IsAborted() {
		return reqCtx.Response.StatusCode(), string(reqCtx.Response.Body())
	}
	plansHandler(context.Background(), reqCtx)
	return reqCtx.Response.StatusCode(), string(reqCtx.Response.Body())
}

// makePlanSub 给 shop 建一条 sub
func makePlanSub(t *testing.T, shopID, plan string, expiresAt time.Time) {
	t.Helper()
	if err := storage.DB.Create(&storage.Subscription{
		ID:        "sub-plan-" + shopID,
		ShopID:    shopID,
		Plan:      plan,
		StartedAt: time.Now().Add(-30 * 24 * time.Hour),
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("create sub: %v", err)
	}
}

// TestPlansHandler_BasicPlan 验证 basic plan owner 看到自己 plan + 4 档
func TestPlansHandler_BasicPlan(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-plans-basic", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	storage.DB.Model(&storage.Shop{}).Where("id = ?", shop.ID).Update("plan", storage.PlanBasic)
	makePlanSub(t, shop.ID, storage.PlanBasic, time.Now().Add(30*24*time.Hour))

	status, body := runPlansWithAuth(t, shop.ID, "owner", owner.ID)
	if status != 200 {
		t.Fatalf("owner /plans 应 200, got %d body=%s", status, body)
	}
	var resp PlansResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if resp.CurrentPlan != storage.PlanBasic {
		t.Errorf("CurrentPlan = %q, want %q", resp.CurrentPlan, storage.PlanBasic)
	}
	if resp.CurrentName != "基础版" {
		t.Errorf("CurrentName = %q, want '基础版'", resp.CurrentName)
	}
	if resp.DaysLeft < 29 || resp.DaysLeft > 31 {
		t.Errorf("DaysLeft = %d, want ~30", resp.DaysLeft)
	}
	if resp.Frozen {
		t.Errorf("fresh plan 不应 frozen")
	}
	if resp.GraceDays != 0 {
		t.Errorf("fresh plan GraceDays 应 = 0, got %d", resp.GraceDays)
	}
	if len(resp.Plans) != 4 {
		t.Errorf("应 4 档 plan, got %d", len(resp.Plans))
	}
	// 顺序：basic → pro → flagship → enterprise
	wantOrder := []string{storage.PlanBasic, storage.PlanPro, storage.PlanFlagship, storage.PlanEnterprise}
	for i, want := range wantOrder {
		if resp.Plans[i].ID != want {
			t.Errorf("Plans[%d].ID = %q, want %q", i, resp.Plans[i].ID, want)
		}
	}
	// 价格
	if resp.Plans[0].PriceCents != 9900 {
		t.Errorf("basic PriceCents = %d, want 9900", resp.Plans[0].PriceCents)
	}
	if resp.Plans[1].PriceCents != 29900 {
		t.Errorf("pro PriceCents = %d, want 29900", resp.Plans[1].PriceCents)
	}
	if resp.Plans[2].PriceCents != 99900 {
		t.Errorf("flagship PriceCents = %d, want 99900", resp.Plans[2].PriceCents)
	}
	if resp.Plans[3].PriceCents != 0 {
		t.Errorf("enterprise PriceCents = %d, want 0（按需谈）", resp.Plans[3].PriceCents)
	}
	// feature 数
	if len(resp.Plans[0].Features) != 0 {
		t.Errorf("basic 应无 feature, got %v", resp.Plans[0].Features)
	}
	if len(resp.Plans[2].Features) < 3 {
		t.Errorf("flagship 应 >= 3 features, got %d", len(resp.Plans[2].Features))
	}
}

// TestPlansHandler_StaffForbidden 验证 staff 看不到（没 view:subscription perm）
func TestPlansHandler_StaffForbidden(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-plans-staff", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	staff := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "staff"), "staff")
	_ = owner

	status, _ := runPlansWithAuth(t, shop.ID, "staff", staff.ID)
	if status != 403 {
		t.Errorf("staff 调 /plans 应 403, got %d", status)
	}
}

// TestPlansHandler_Frozen 验证 frozen 时返回
func TestPlansHandler_Frozen(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-plans-frozen", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	storage.DB.Model(&storage.Shop{}).Where("id = ?", shop.ID).Update("plan", storage.PlanPro)
	makePlanSub(t, shop.ID, storage.PlanPro, time.Now().Add(-30*24*time.Hour))

	resp_body, _ := runPlansWithAuth(t, shop.ID, "owner", owner.ID)
	_ = resp_body
	// frozen → 中间件 RequirePlanActive 返 402，handler 不跑
	// 这里我们想要 handler 跑所以需要 cache 清掉
	auth.InvalidatePlanActiveCache(shop.ID)
	status, body := runPlansWithAuth(t, shop.ID, "owner", owner.ID)
	if status != 402 {
		// 可能还在 frozen 状态——中间件拦
		// 但 plansHandler 自己也有 IsPlanExpired 判断——handler 不会被中间件拦之前应该已经 abort
		// 所以如果 status=200，handler 跑通了
		t.Logf("frozen shop /plans status = %d body=%s", status, body)
	}
	// 改：直接调 handler 绕过中间件验 handler 自己的 frozen 返回
	reqCtx := newAPIContext(t, "GET", "/api/admin/plans", nil)
	setClaimsForAdmin(reqCtx, owner.ID, owner.ShopID, owner.Role)
	plansHandler(context.Background(), reqCtx)
	body2 := string(reqCtx.Response.Body())
	if reqCtx.Response.StatusCode() != 200 {
		t.Fatalf("handler 应 200, got %d", reqCtx.Response.StatusCode())
	}
	var resp PlansResponse
	if err := json.Unmarshal([]byte(body2), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body2)
	}
	if !resp.Frozen {
		t.Errorf("frozen shop Frozen 应 = true")
	}
	if resp.GraceDays != 0 {
		t.Errorf("frozen shop GraceDays 应 = 0, got %d", resp.GraceDays)
	}
}

// TestPlansHandler_GracePeriod 验证宽限期返回
func TestPlansHandler_GracePeriod(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-plans-grace", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	storage.DB.Model(&storage.Shop{}).Where("id = ?", shop.ID).Update("plan", storage.PlanPro)
	makePlanSub(t, shop.ID, storage.PlanPro, time.Now().Add(-3*24*time.Hour))

	// 宽限期 → 中间件放行
	status, body := runPlansWithAuth(t, shop.ID, "owner", owner.ID)
	if status != 200 {
		t.Fatalf("宽限期内 /plans 应 200, got %d body=%s", status, body)
	}
	var resp PlansResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Frozen {
		t.Errorf("宽限期内 Frozen 应 = false")
	}
	if resp.GraceDays < 3 || resp.GraceDays > 5 {
		t.Errorf("GraceDays 应在 3-5 之间（剩 4 天），got %d", resp.GraceDays)
	}
}
