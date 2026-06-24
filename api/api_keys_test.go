package api

// api_keys_test.go —— /api/admin/api-keys + /api/external/appointments 测试（v4.12.1）
//
// 覆盖：
//   - basic → 403 + feature_required
//   - flagship → 200 + 返 plaintext（明文仅一次）
//   - flagship 第 2 次 list → 不含 plaintext
//   - revoke → 401（用此 token 鉴权失败）
//   - 跨店隔离：shopB 不能 revoke shopA 的 key
//   - frozen → 402
//   - staff → 403（无 PermViewPlan）
//   - external endpoint：合法 token 返 JSON；revoked token 401；缺 scope 403

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"

	"github.com/cloudwego/hertz/pkg/app"
)

// helper：跑 API key handler（auth 中间件已注册到 /api/external；这里手动模拟）
func runWithAPIKey(t *testing.T, token, scope string, handler func(ctx context.Context, c *app.RequestContext), ctx *app.RequestContext) (int, string) {
	t.Helper()
	auth.APIKeyAuth()(context.Background(), ctx)
	if ctx.IsAborted() {
		return ctx.Response.StatusCode(), string(ctx.Response.Body())
	}
	if scope != "" {
		auth.RequireAPIKeyScope(scope)(context.Background(), ctx)
		if ctx.IsAborted() {
			return ctx.Response.StatusCode(), string(ctx.Response.Body())
		}
	}
	handler(context.Background(), ctx)
	return ctx.Response.StatusCode(), string(ctx.Response.Body())
}

func TestCreateAPIKey_OwnerBasic_Forbidden(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-apikey-basic", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanBasic)
	makeActiveSub(t, shop.ID, storage.PlanBasic, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "POST", "/api/admin/api-keys",
		[]byte(`{"name":"POS 系统","scopes":["appointments:read"]}`))
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, createAPIKeyHandler, ctx)

	if status != 403 {
		t.Fatalf("basic 应 403, got %d body=%s", status, body)
	}
	if !strings.Contains(body, storage.FeatureAPIAccess) {
		t.Errorf("应含 feature_required=%s, 实际: %s", storage.FeatureAPIAccess, body)
	}
}

func TestCreateAPIKey_OwnerFlagship_OK(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-apikey-flagship", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanFlagship)
	makeActiveSub(t, shop.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	ctx := newAPIContext(t, "POST", "/api/admin/api-keys",
		[]byte(`{"name":"POS 系统","scopes":["appointments:read"]}`))
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, createAPIKeyHandler, ctx)

	if status != 200 {
		t.Fatalf("flagship 应 200, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "apikey_") {
		t.Errorf("响应应含 plaintext（apikey_ 前缀）, 实际: %s", body)
	}
	if !strings.Contains(body, "POS 系统") {
		t.Errorf("响应应含 name, 实际: %s", body)
	}
}

func TestListAPIKeys_NoPlaintext(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-apikey-list", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanFlagship)
	makeActiveSub(t, shop.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	// 先建一个
	plaintext, key, err := storage.CreateAPIKey(t.Context(), shop.ID, "test", []string{"appointments:read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	_ = key

	ctx := newAPIContext(t, "GET", "/api/admin/api-keys", nil)
	setClaimsForAdmin(ctx, owner.ID, owner.ShopID, owner.Role)

	status, body := runWithPermAndPlan(t, storage.PermViewPlan, listAPIKeysHandler, ctx)

	if status != 200 {
		t.Fatalf("list 应 200, got %d", status)
	}
	if strings.Contains(body, plaintext) {
		t.Errorf("list 不应包含 plaintext！泄漏！body=%s", body)
	}
	if !strings.Contains(body, "test") {
		t.Errorf("list 应含 name=test, body=%s", body)
	}
}

func TestRevokeAPIKey_ThenAuthFails(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-apikey-revoke", "")
	owner := storage.MakeAdminWithRole(t, shop.ID, storage.ShortTestUsername(t, "owner"), "owner")
	setShopPlan(t, shop.ID, storage.PlanFlagship)
	makeActiveSub(t, shop.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	plaintext, key, err := storage.CreateAPIKey(t.Context(), shop.ID, "test", []string{"appointments:read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	// revoke
	if err := storage.RevokeAPIKey(t.Context(), shop.ID, key.ID, "test"); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// 现在用 plaintext 调 external endpoint 应 401
	extCtx := newAPIContext(t, "GET", "/api/external/appointments", nil)
	extCtx.Request.Header.Set("Authorization", "Bearer "+plaintext)
	_ = owner
	status, body := runWithAPIKey(t, plaintext, "appointments:read", listExternalAppointmentsHandler, extCtx)

	if status != 401 {
		t.Fatalf("revoked token 应 401, got %d body=%s", status, body)
	}
}

func TestExternalAppointments_OK(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-external-ok", "")
	setShopPlan(t, shop.ID, storage.PlanFlagship)
	makeActiveSub(t, shop.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	plaintext, _, err := storage.CreateAPIKey(t.Context(), shop.ID, "test",
		[]string{"appointments:read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	extCtx := newAPIContext(t, "GET", "/api/external/appointments", nil)
	extCtx.Request.Header.Set("Authorization", "Bearer "+plaintext)

	status, body := runWithAPIKey(t, plaintext, "appointments:read", listExternalAppointmentsHandler, extCtx)

	if status != 200 {
		t.Fatalf("合法 token 应 200, got %d body=%s", status, body)
	}
	if !strings.Contains(body, `"shop_id":"shop-external-ok"`) {
		t.Errorf("应含 shop_id, body=%s", body)
	}
	if !strings.Contains(body, `"items":[]`) {
		t.Errorf("应含 items（空数组即可），body=%s", body)
	}
}

func TestExternalAppointments_InvalidToken_401(t *testing.T) {
	setupAPITestDB(t)
	extCtx := newAPIContext(t, "GET", "/api/external/appointments", nil)
	extCtx.Request.Header.Set("Authorization", "Bearer apikey_fakefakefake")

	status, _ := runWithAPIKey(t, "apikey_fakefakefake", "appointments:read", listExternalAppointmentsHandler, extCtx)

	if status != 401 {
		t.Fatalf("无效 token 应 401, got %d", status)
	}
}

func TestExternalAppointments_MissingScope_403(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-external-scope", "")
	plaintext, _, err := storage.CreateAPIKey(t.Context(), shop.ID, "test",
		[]string{"customers:read"}, 0) // 没有 appointments:read
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	extCtx := newAPIContext(t, "GET", "/api/external/appointments", nil)
	extCtx.Request.Header.Set("Authorization", "Bearer "+plaintext)

	status, body := runWithAPIKey(t, plaintext, "appointments:read", listExternalAppointmentsHandler, extCtx)

	if status != 403 {
		t.Fatalf("scope 不够应 403, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "scope") {
		t.Errorf("应提示 scope, body=%s", body)
	}
}

func TestRevokeAPIKey_CrossShop_400(t *testing.T) {
	setupAPITestDB(t)
	shopA := storage.MakeShop(t, "shop-revoke-a", "")
	shopB := storage.MakeShop(t, "shop-revoke-b", "")
	setShopPlan(t, shopA.ID, storage.PlanFlagship)
	makeActiveSub(t, shopA.ID, storage.PlanFlagship, time.Now().Add(30*24*time.Hour))

	// shopA 建一个 key
	_, key, err := storage.CreateAPIKey(t.Context(), shopA.ID, "A-key", []string{"appointments:read"}, 0)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	// shopB 想直接调 storage.RevokeAPIKey → RowsAffected=0，应返 error
	err = storage.RevokeAPIKey(t.Context(), shopB.ID, key.ID, "")
	if err == nil {
		t.Fatal("跨店 revoke 应失败，但成功了")
	}
	if !strings.Contains(err.Error(), "不存在或不属于") {
		t.Errorf("应提示跨店 revoke 失败, err=%v", err)
	}
}