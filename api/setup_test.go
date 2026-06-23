package api

// setup_test.go
//
// Test helpers for the api package.
//
//   - setupAPITestDB: bind storage.DB to an isolated in-memory sqlite (delegates
//     to storage.SetupTestDB so per-test isolation is consistent with other packages).
//   - newAPIContext: build a Hertz RequestContext with optional headers / query /
//     body / params / claims so we can call handlers directly without spinning up a real router.
//   - runHandler: invoke handler + return (status, body) for assertions.
//
// Pattern mirrors auth/jwt_test.go's newCtxWithHeaders; we extend it with body /
// path-param / claims support and avoid spinning up a full hertz router.
//
// Run:
//   go test ./api/... -v

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/cloudwego/hertz/pkg/route/param"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

// setupAPITestDB wires storage.DB to a fresh in-memory sqlite.
//
// Always call this before any handler that touches storage.DB (which is most of
// them). Per-test isolation is handled inside storage.SetupTestDB.
func setupAPITestDB(t *testing.T) {
	t.Helper()
	storage.SetupTestDB(t)
}

// adminClaims returns a *auth.Claims for the given shop — useful for "商户登录了" fixture.
func adminClaims(shopID string) *auth.Claims {
	return &auth.Claims{
		AdminID: 1,
		ShopID:  shopID,
		Role:    "owner",
	}
}

// setClaimsForAdmin 直接给 ctx 装任意 admin 的 claims（v4.7 RBAC 测试用）
//
//   - 跟 withClaims(adminClaims(shop)) 区别：后者固定 AdminID=1/Role=owner
//   - 这个能指定任意 AdminID/Role，便于测 staff 路径
func setClaimsForAdmin(c *app.RequestContext, adminID uint64, shopID, role string) {
	c.Set("auth_claims", &auth.Claims{
		AdminID: adminID,
		ShopID:  shopID,
		Role:    role,
	})
}

// ctxOption is a functional option for newAPIContext.
type ctxOption func(*ctxCfg)

type ctxCfg struct {
	headers map[string]string
	query   map[string]string
	params  map[string]string
	claims  *auth.Claims
}

// withHeader sets a request header (e.g. "Authorization": "Bearer xxx").
func withHeader(k, v string) ctxOption {
	return func(c *ctxCfg) {
		if c.headers == nil {
			c.headers = map[string]string{}
		}
		c.headers[k] = v
	}
}

// withQuery sets a query string param (e.g. "limit": "20").
func withQuery(k, v string) ctxOption {
	return func(c *ctxCfg) {
		if c.query == nil {
			c.query = map[string]string{}
		}
		c.query[k] = v
	}
}

// withPathParam sets a path param (for routes like /barber/:id/leave).
func withPathParam(k, v string) ctxOption {
	return func(c *ctxCfg) {
		if c.params == nil {
			c.params = map[string]string{}
		}
		c.params[k] = v
	}
}

// withClaims attaches *auth.Claims so shopFromClaims / auth.GetClaims return non-nil.
func withClaims(cl *auth.Claims) ctxOption {
	return func(c *ctxCfg) {
		c.claims = cl
	}
}

// newAPIContext builds a Hertz RequestContext suitable for directly invoking
// handler funcs in this package (without spinning up a real router).
//
//   - method: "GET" / "POST" / "DELETE" / etc.
//   - path:   request path (e.g. "/api/admin/leave/create")
//   - body:   raw JSON body bytes (nil for GET/DELETE without body).
//             Content-Type is set to application/json so BindAndValidate picks JSON.
//   - opts:   headers / query / path-params / claims (see withXxx helpers).
//
// Hertz's Request/Response are NoCopy; we build a fresh protocol.Request, set
// everything on it, then CopyTo into the ctx.Request. Response is Acquired and
// copied too so c.JSON can write to it.
func newAPIContext(t *testing.T, method, path string, body []byte, opts ...ctxOption) *app.RequestContext {
	t.Helper()

	req := protocol.NewRequest(method, path, nil)
	req.Header.SetMethod(method)
	req.Header.SetContentTypeBytes([]byte("application/json"))

	cfg := &ctxCfg{}
	for _, o := range opts {
		o(cfg)
	}
	for k, v := range cfg.headers {
		req.Header.Set(k, v)
	}
	if len(cfg.query) > 0 {
		parts := make([]string, 0, len(cfg.query))
		for k, v := range cfg.query {
			parts = append(parts, k+"="+v)
		}
		req.SetQueryString(strings.Join(parts, "&"))
	}
	if len(body) > 0 {
		req.SetBody(body)
		req.Header.SetContentLength(len(body))
	}

	ctx := app.NewContext(0)
	req.CopyTo(&ctx.Request)

	if len(cfg.params) > 0 {
		ctx.Params = make(param.Params, 0, len(cfg.params))
		for k, v := range cfg.params {
			ctx.Params = append(ctx.Params, param.Param{Key: k, Value: v})
		}
	}

	resp := protocol.AcquireResponse()
	resp.CopyTo(&ctx.Response)

	if cfg.claims != nil {
		ctx.Set("auth_claims", cfg.claims)
	}
	return ctx
}

// runHandler invokes handler(ctx, c) with a fresh background context and returns
// (status, body) so tests can assert. Body comes from c.Response.Body().
// init v4.7 RBAC: 测试里也要把 storage.AdminHasPermission 注入到 auth 包
// （生产环境由 api.RegisterRoutes 完成；测试不走 RegisterRoutes）
func init() {
	auth.SetHasPermissionFunc(func(ctx context.Context, adminID uint64, perm string) (bool, error) {
		return storage.AdminHasPermission(ctx, adminID, perm)
	})
}

func runHandler(t *testing.T, handler func(ctx context.Context, c *app.RequestContext), ctx *app.RequestContext) (int, string) {
	t.Helper()
	handler(context.Background(), ctx)
	return ctx.Response.StatusCode(), string(ctx.Response.Body())
}

// runWithPerm 模拟 Hertz 中间件链：先跑 RequirePerm(perm)，放行则跑业务 handler
//
// 真实 router 自动串起中间件；测试里手动模拟：
//   - middleware 调 c.Abort() → response 已被写 → 直接返回
//   - middleware 不 abort → 我们手动调 handler
//
// 用途：测试 v4.7 RBAC 中间件对 endpoint 的拦截行为
func runWithPerm(t *testing.T, perm string, handler func(ctx context.Context, c *app.RequestContext), ctx *app.RequestContext) (int, string) {
	t.Helper()
	auth.RequirePerm(perm)(context.Background(), ctx)
	if !ctx.IsAborted() {
		handler(context.Background(), ctx)
	}
	return ctx.Response.StatusCode(), string(ctx.Response.Body())
}

// runWithRole 模拟 Hertz 中间件链：先跑 RequireRole(...allowed)，放行则跑业务 handler
//
// v4.10.1 新增：用于测试"按 role 强约束"接口（多店看板等只给 platform_admin 的）
// 跟 runWithPerm 一样手动模拟中间件链
func runWithRole(t *testing.T, allowedRoles []string, handler func(ctx context.Context, c *app.RequestContext), ctx *app.RequestContext) (int, string) {
	t.Helper()
	auth.RequireRole(allowedRoles...)(context.Background(), ctx)
	if !ctx.IsAborted() {
		handler(context.Background(), ctx)
	}
	return ctx.Response.StatusCode(), string(ctx.Response.Body())
}

// jsonRaw is a tiny helper for clarity at call sites.
func jsonRaw(s string) []byte {
	if s == "" {
		return nil
	}
	return []byte(s)
}

// jsonBuf returns the JSON-encoding bytes (for tests that want to build via map).
// It exists for symmetry; most tests use string literals.
func jsonBuf(v any) []byte {
	switch x := v.(type) {
	case string:
		return []byte(x)
	case []byte:
		if len(x) == 0 {
			return nil
		}
		return x
	case nil:
		return nil
	}
	return bytes.NewBufferString("").Bytes()
}

// Status code constants — re-declared here so tests don't need to import net/http.
const (
	statusOK           = http.StatusOK
	statusBadRequest   = http.StatusBadRequest
	statusUnauthorized = http.StatusUnauthorized
	statusForbidden    = http.StatusForbidden
	statusNotFound     = http.StatusNotFound
	statusConflict     = http.StatusConflict
)