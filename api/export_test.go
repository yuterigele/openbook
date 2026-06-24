package api

// export_test.go —— GET /api/admin/data/export 测试（v4.12.1）
//
// 覆盖：
//   - basic plan → 403 + feature_required（gate 实战）
//   - pro plan → 200 + CSV header + 内容
//   - flagship plan → 200（features 包含 data_export）
//   - 缺 PermViewPlan → 403（staff 默认进不来）
//   - frozen plan → 402（RequirePlanActive middleware）
//   - 参数错（from > to, 非法格式） → 400
//   - 默认区间（缺 from/to）→ 200（最近 30 天）
//   - 不支持 type → 400

import (
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

func TestDataExport_OwnerBasic_Forbidden(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-export-basic", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanBasic)
	makeActiveSub(t, shop.ID, storage.PlanBasic, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/data/export", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, dataExportHandler, ctx)

	if status != 403 {
		t.Fatalf("basic plan 应 403（feature 禁）, got %d body=%s", status, body)
	}
	if !strings.Contains(body, storage.FeatureDataExport) {
		t.Errorf("body 应包含 feature_required=%s, 实际: %s", storage.FeatureDataExport, body)
	}
	if !strings.Contains(body, storage.PlanBasic) {
		t.Errorf("body 应包含 current_plan=%s, 实际: %s", storage.PlanBasic, body)
	}
	if !strings.Contains(body, "升级") {
		t.Errorf("body 应引导用户升级, 实际: %s", body)
	}
}

func TestDataExport_OwnerPro_OK(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-export-pro", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanPro)
	makeActiveSub(t, shop.ID, storage.PlanPro, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/data/export", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, dataExportHandler, ctx)

	if status != 200 {
		t.Fatalf("pro plan 应 200, got %d body=%s", status, body)
	}
	// BOM 头（Excel 兼容）
	if !strings.HasPrefix(body, "\xEF\xBB\xBF") {
		t.Error("CSV 应以 UTF-8 BOM 开头")
	}
	// 表头中文
	if !strings.Contains(body, "日期") || !strings.Contains(body, "理发师") || !strings.Contains(body, "客户") {
		t.Errorf("CSV 应含中文表头（日期/理发师/客户）, 实际前 200: %s", body[:min(len(body), 200)])
	}
}

func TestDataExport_OwnerFlagship_OK(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-export-flagship", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanFlagship)
	makeActiveSub(t, shop.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/data/export", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, _ := runWithPermAndPlan(t, storage.PermViewPlan, dataExportHandler, ctx)

	if status != 200 {
		t.Fatalf("flagship plan 应 200, got %d", status)
	}
}

func TestDataExport_NoPerm_Forbidden(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-export-noperm", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanFlagship)
	makeActiveSub(t, shop.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/data/export", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	// 不传 perm → RequirePerm 返 403
	status, _ := runWithPermAndPlan(t, "", dataExportHandler, ctx)

	if status != 403 {
		t.Fatalf("无 perm 应 403, got %d", status)
	}
}

func TestDataExport_FrozenPlan_402(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-export-frozen", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanPro)
	// 过期 30 天（远超 7 天宽限期） → frozen
	makeActiveSub(t, shop.ID, storage.PlanPro, time.Now().Add(-30*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/data/export", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, dataExportHandler, ctx)

	if status != 402 {
		t.Fatalf("frozen plan 应 402, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "frozen") {
		t.Errorf("响应应含 frozen 标记, 实际: %s", body)
	}
}

func TestDataExport_DefaultRange_OK(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-export-default-range", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanPro)
	makeActiveSub(t, shop.ID, storage.PlanPro, time.Now().Add(30*24*time.Hour))

	// 不传 from/to
	ctx := newAPIContext(t, "GET", "/api/admin/data/export", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, _ := runWithPermAndPlan(t, storage.PermViewPlan, dataExportHandler, ctx)

	if status != 200 {
		t.Fatalf("默认区间应 200, got %d", status)
	}
}

func TestDataExport_InvalidFrom_400(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-export-bad-from", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanPro)
	makeActiveSub(t, shop.ID, storage.PlanPro, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/data/export", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
	ctx.Request.SetQueryString("from=2026/06/01&to=2026-06-30") // 错格式（斜杠）

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, dataExportHandler, ctx)

	if status != 400 {
		t.Fatalf("非法 from 应 400, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "from") {
		t.Errorf("应提示 from 错, 实际: %s", body)
	}
}

func TestDataExport_FromAfterTo_400(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-export-after", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanPro)
	makeActiveSub(t, shop.ID, storage.PlanPro, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/data/export", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
	ctx.Request.SetQueryString("from=2026-06-30&to=2026-06-01")

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, dataExportHandler, ctx)

	if status != 400 {
		t.Fatalf("from>to 应 400, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "from") {
		t.Errorf("应提示 from 错, 实际: %s", body)
	}
}

func TestDataExport_UnsupportedType_400(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-export-bad-type", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanPro)
	makeActiveSub(t, shop.ID, storage.PlanPro, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "GET", "/api/admin/data/export", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)
	ctx.Request.SetQueryString("type=customers&from=2026-06-01&to=2026-06-30")

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, dataExportHandler, ctx)

	if status != 400 {
		t.Fatalf("不支持 type 应 400, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "appointments") {
		t.Errorf("应提示 v4.12.1 只支持 appointments, 实际: %s", body)
	}
}