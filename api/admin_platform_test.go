package api

// admin_platform_test.go —— /api/admin/platform/* 测试（v4.13.0）
//
// 覆盖：
//   - platform_stats / platform_shops / platform_shops/:id
//   - platform_shops/:id/plan PUT（成功 + 各种 400/403/404）
//   - platform_audit
//   - 全部走 RequireRole(RolePlatformAdmin) — owner/staff 拿 403
//   - 写 audit log 后能从 /audit 拉回来

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

// runPlatformWithRole 走 RequireRole(RolePlatformAdmin) + handler
func runPlatformWithRole(t *testing.T, role string, method, path string, body []byte, params map[string]string) (int, string) {
	t.Helper()
	reqCtx := newAPIContext(t, method, path, body, withPathParams(params))
	if role != "" {
		setClaimsForAdmin(reqCtx, 1, "platform-shop", role)
	}
	auth.RequireRole(storage.RolePlatformAdmin)(context.Background(), reqCtx)
	if reqCtx.IsAborted() {
		return reqCtx.Response.StatusCode(), string(reqCtx.Response.Body())
	}
	// 根据 path 调对应 handler
	switch {
	case strings.HasSuffix(path, "/stats"):
		platformStatsHandler(context.Background(), reqCtx)
	case strings.HasSuffix(path, "/audit"):
		platformAuditHandler(context.Background(), reqCtx)
	case strings.Contains(path, "/plan"):
		platformSetShopPlanHandler(context.Background(), reqCtx)
	case strings.Contains(path, "/shops/") && method == "GET":
		platformShopDetailHandler(context.Background(), reqCtx)
	case strings.HasSuffix(path, "/shops"):
		platformShopsHandler(context.Background(), reqCtx)
	}
	return reqCtx.Response.StatusCode(), string(reqCtx.Response.Body())
}

// withPathParams 一次设置多个 path param
func withPathParams(m map[string]string) ctxOption {
	return func(c *ctxCfg) {
		if c.params == nil {
			c.params = map[string]string{}
		}
		for k, v := range m {
			c.params[k] = v
		}
	}
}

// TestPlatform_OwnerForbidden 非 platform_admin 全 403
func TestPlatform_OwnerForbidden(t *testing.T) {
	setupAPITestDB(t)
	for _, path := range []string{
		"/api/admin/platform/stats",
		"/api/admin/platform/shops",
		"/api/admin/platform/audit",
	} {
		status, body := runPlatformWithRole(t, "owner", "GET", path, nil, nil)
		if status != 403 {
			t.Errorf("owner 调 %s 应 403, got %d body=%s", path, status, body)
		}
	}
}

// TestPlatform_StaffForbidden staff 也 403
func TestPlatform_StaffForbidden(t *testing.T) {
	setupAPITestDB(t)
	status, _ := runPlatformWithRole(t, "staff", "GET", "/api/admin/platform/stats", nil, nil)
	if status != 403 {
		t.Errorf("staff 调 /stats 应 403, got %d", status)
	}
}

// TestPlatform_Stats_EmptyDB 空库也能跑（返 0）
func TestPlatform_Stats_EmptyDB(t *testing.T) {
	setupAPITestDB(t)
	status, body := runPlatformWithRole(t, storage.RolePlatformAdmin, "GET", "/api/admin/platform/stats", nil, nil)
	if status != 200 {
		t.Fatalf("应 200, got %d body=%s", status, body)
	}
	var resp PlatformStats
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if resp.TotalShops != 0 {
		t.Errorf("TotalShops = %d, want 0", resp.TotalShops)
	}
	if len(resp.PlanDistribution) != len(storage.AllPlanIDs) {
		t.Errorf("PlanDistribution 长度 = %d, want %d", len(resp.PlanDistribution), len(storage.AllPlanIDs))
	}
}

// TestPlatform_Stats_WithShops 有店时正确聚合
func TestPlatform_Stats_WithShops(t *testing.T) {
	setupAPITestDB(t)
	// 建 3 家店：basic × 2、pro × 1
	s1 := storage.MakeShop(t, "shop-stats-1", "")
	s2 := storage.MakeShop(t, "shop-stats-2", "")
	s3 := storage.MakeShop(t, "shop-stats-3", "")
	storage.DB.Model(&storage.Shop{}).Where("id = ?", s1.ID).Update("plan", storage.PlanBasic)
	storage.DB.Model(&storage.Shop{}).Where("id = ?", s2.ID).Update("plan", storage.PlanBasic)
	storage.DB.Model(&storage.Shop{}).Where("id = ?", s3.ID).Update("plan", storage.PlanPro)
	// 给每家店一个有效 sub（避免 IsPlanExpired 返 frozen）
	makePlanSub(t, s1.ID, storage.PlanBasic, time.Now().Add(30*24*time.Hour))
	makePlanSub(t, s2.ID, storage.PlanBasic, time.Now().Add(30*24*time.Hour))
	makePlanSub(t, s3.ID, storage.PlanPro, time.Now().Add(30*24*time.Hour))

	status, body := runPlatformWithRole(t, storage.RolePlatformAdmin, "GET", "/api/admin/platform/stats", nil, nil)
	if status != 200 {
		t.Fatalf("应 200, got %d body=%s", status, body)
	}
	var resp PlatformStats
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if resp.TotalShops != 3 {
		t.Errorf("TotalShops = %d, want 3", resp.TotalShops)
	}
	// 验 plan 分布
	distByID := make(map[string]int)
	for _, b := range resp.PlanDistribution {
		distByID[b.Plan] = b.ShopCount
	}
	if distByID[storage.PlanBasic] != 2 {
		t.Errorf("basic 店铺数 = %d, want 2", distByID[storage.PlanBasic])
	}
	if distByID[storage.PlanPro] != 1 {
		t.Errorf("pro 店铺数 = %d, want 1", distByID[storage.PlanPro])
	}
	if distByID[storage.PlanFlagship] != 0 {
		t.Errorf("flagship 店铺数 = %d, want 0", distByID[storage.PlanFlagship])
	}
	// monthly_revenue = 2*99 + 1*299 = 497
	if resp.MonthlyRevenueYuan != 497 {
		t.Errorf("MonthlyRevenueYuan = %d, want 497", resp.MonthlyRevenueYuan)
	}
}

// TestPlatform_Shops_List 列全平台店铺
func TestPlatform_Shops_List(t *testing.T) {
	setupAPITestDB(t)
	s1 := storage.MakeShop(t, "shop-list-1", "")
	s2 := storage.MakeShop(t, "shop-list-2", "")
	storage.DB.Model(&storage.Shop{}).Where("id = ?", s1.ID).Update("plan", storage.PlanFlagship)
	storage.DB.Model(&storage.Shop{}).Where("id = ?", s2.ID).Update("plan", storage.PlanBasic)
	makePlanSub(t, s1.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	status, body := runPlatformWithRole(t, storage.RolePlatformAdmin, "GET", "/api/admin/platform/shops", nil, nil)
	if status != 200 {
		t.Fatalf("应 200, got %d body=%s", status, body)
	}
	var resp PlatformShopsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if resp.Total != 2 {
		t.Errorf("Total = %d, want 2", resp.Total)
	}
	// 找 flagship 店验字段
	var foundFlagship *PlatformShopItem
	for i := range resp.Shops {
		if resp.Shops[i].Plan == storage.PlanFlagship {
			foundFlagship = &resp.Shops[i]
		}
	}
	if foundFlagship == nil {
		t.Fatalf("没找到 flagship 店")
	}
	if foundFlagship.PlanName != "旗舰版" {
		t.Errorf("PlanName = %q, want '旗舰版'", foundFlagship.PlanName)
	}
	if foundFlagship.DaysLeft < 29 || foundFlagship.DaysLeft > 31 {
		t.Errorf("DaysLeft = %d, want ~30", foundFlagship.DaysLeft)
	}
}

// TestPlatform_SetPlan_Success 改套餐成功 + 写 audit
func TestPlatform_SetPlan_Success(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-setplan-ok", "")
	storage.DB.Model(&storage.Shop{}).Where("id = ?", shop.ID).Update("plan", storage.PlanBasic)
	// 建一个旧 sub 验证"取消旧的"逻辑
	makePlanSub(t, shop.ID, storage.PlanBasic, time.Now().Add(30*24*time.Hour))

	body := `{"plan":"flagship","months":12,"note":"投资人 demo 升级到旗舰"}`
	status, respBody := runPlatformWithRole(t, storage.RolePlatformAdmin, "PUT",
		"/api/admin/platform/shops/"+shop.ID+"/plan", []byte(body),
		map[string]string{"id": shop.ID})
	if status != 200 {
		t.Fatalf("应 200, got %d body=%s", status, respBody)
	}
	var resp SetShopPlanResponse
	if err := json.Unmarshal([]byte(respBody), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, respBody)
	}
	if resp.OldPlan != storage.PlanBasic {
		t.Errorf("OldPlan = %q, want %q", resp.OldPlan, storage.PlanBasic)
	}
	if resp.NewPlan != storage.PlanFlagship {
		t.Errorf("NewPlan = %q, want %q", resp.NewPlan, storage.PlanFlagship)
	}
	// 验 shop.plan 已更新
	updated, _ := storage.GetShopByID(context.Background(), shop.ID)
	if updated.Plan != storage.PlanFlagship {
		t.Errorf("shop.plan = %q, want %q", updated.Plan, storage.PlanFlagship)
	}
	// 验旧 sub 被 cancel、新 sub 存在
	var subs []storage.Subscription
	storage.DB.Where("shop_id = ?", shop.ID).Order("started_at DESC").Find(&subs)
	if len(subs) < 2 {
		t.Errorf("期望至少 2 条 sub（旧被 cancel + 新），got %d", len(subs))
	}
	// 旧 sub 应有 cancelled_at
	oldSub := subs[len(subs)-1]
	if oldSub.CancelledAt == nil {
		t.Errorf("旧 sub 未被 cancel")
	}
	// 新 sub 应是 flagship + 未 cancel
	newSub := subs[0]
	if newSub.Plan != storage.PlanFlagship || newSub.CancelledAt != nil {
		t.Errorf("新 sub plan=%q cancelled=%v", newSub.Plan, newSub.CancelledAt)
	}
}

// TestPlatform_SetPlan_InvalidPlan 未知 plan 返 400
func TestPlatform_SetPlan_InvalidPlan(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-setplan-bad", "")

	body := `{"plan":"unicorn","months":1}`
	status, respBody := runPlatformWithRole(t, storage.RolePlatformAdmin, "PUT",
		"/api/admin/platform/shops/"+shop.ID+"/plan", []byte(body),
		map[string]string{"id": shop.ID})
	if status != 400 {
		t.Errorf("应 400, got %d body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "未知 plan") {
		t.Errorf("响应应含 '未知 plan', got %s", respBody)
	}
}

// TestPlatform_SetPlan_InvalidMonths months <= 0 返 400
func TestPlatform_SetPlan_InvalidMonths(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-setplan-months", "")

	for _, m := range []string{`{"plan":"basic","months":0}`, `{"plan":"basic","months":-1}`} {
		status, _ := runPlatformWithRole(t, storage.RolePlatformAdmin, "PUT",
			"/api/admin/platform/shops/"+shop.ID+"/plan", []byte(m),
			map[string]string{"id": shop.ID})
		if status != 400 {
			t.Errorf("months=%s 应 400, got %d", m, status)
		}
	}
}

// TestPlatform_SetPlan_ShopNotFound 不存在的店 404
func TestPlatform_SetPlan_ShopNotFound(t *testing.T) {
	setupAPITestDB(t)

	body := `{"plan":"basic","months":1}`
	status, _ := runPlatformWithRole(t, storage.RolePlatformAdmin, "PUT",
		"/api/admin/platform/shops/ghost-shop/plan", []byte(body),
		map[string]string{"id": "ghost-shop"})
	if status != 404 {
		t.Errorf("应 404, got %d", status)
	}
}

// TestPlatform_Audit_AfterSetPlan 改套餐后能从 /audit 拉到 audit
func TestPlatform_Audit_AfterSetPlan(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-audit", "")
	storage.DB.Model(&storage.Shop{}).Where("id = ?", shop.ID).Update("plan", storage.PlanBasic)

	// 改 plan
	body := `{"plan":"pro","months":3,"note":"test audit"}`
	if status, b := runPlatformWithRole(t, storage.RolePlatformAdmin, "PUT",
		"/api/admin/platform/shops/"+shop.ID+"/plan", []byte(body),
		map[string]string{"id": shop.ID}); status != 200 {
		t.Fatalf("setplan 失败: %d %s", status, b)
	}

	// 拉 audit
	status, respBody := runPlatformWithRole(t, storage.RolePlatformAdmin, "GET",
		"/api/admin/platform/audit", nil, nil)
	if status != 200 {
		t.Fatalf("应 200, got %d body=%s", status, respBody)
	}
	var resp struct {
		Total int                 `json:"total"`
		Items []PlatformAuditItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(respBody), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, respBody)
	}
	if resp.Total < 1 {
		t.Fatalf("audit 应至少 1 条, got %d", resp.Total)
	}
	// 找最新的（应是刚刚的 plan_changed_by_admin）
	found := false
	for _, a := range resp.Items {
		if a.ShopID == shop.ID && a.NewPlan == storage.PlanPro && a.OldPlan == storage.PlanBasic {
			found = true
			if a.Months != 3 {
				t.Errorf("audit Months = %d, want 3", a.Months)
			}
			if a.Note != "test audit" {
				t.Errorf("audit Note = %q, want 'test audit'", a.Note)
			}
		}
	}
	if !found {
		t.Errorf("没找到刚改的 audit 记录，items=%+v", resp.Items)
	}
}

// TestPlatform_ShopDetail 单店详情含 sub 历史 + 成员
func TestPlatform_ShopDetail(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-detail", "")
	storage.DB.Model(&storage.Shop{}).Where("id = ?", shop.ID).Update("plan", storage.PlanPro)
	makePlanSub(t, shop.ID, storage.PlanPro, time.Now().Add(30*24*time.Hour))
	// 加一个 member
	storage.MakeAdminWithRole(t, shop.ID, "owner-detail", "owner")

	status, body := runPlatformWithRole(t, storage.RolePlatformAdmin, "GET",
		"/api/admin/platform/shops/"+shop.ID, nil,
		map[string]string{"id": shop.ID})
	if status != 200 {
		t.Fatalf("应 200, got %d body=%s", status, body)
	}
	var resp PlatformShopDetail
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if resp.Shop.ID != shop.ID {
		t.Errorf("Shop.ID = %q, want %q", resp.Shop.ID, shop.ID)
	}
	if resp.Shop.Plan != storage.PlanPro {
		t.Errorf("Shop.Plan = %q, want %q", resp.Shop.Plan, storage.PlanPro)
	}
	if len(resp.Subscriptions) != 1 {
		t.Errorf("Subscriptions = %d, want 1", len(resp.Subscriptions))
	}
	if len(resp.Members) != 1 {
		t.Errorf("Members = %d, want 1", len(resp.Members))
	}
	if resp.Members[0].Username != "owner-detail" {
		t.Errorf("Member.Username = %q, want 'owner-detail'", resp.Members[0].Username)
	}
}
