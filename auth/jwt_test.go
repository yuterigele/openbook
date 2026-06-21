package auth

// jwt_test.go
//
// JWT 签发 / 校验 / 中间件 单测（2026-06-21）
//
// 覆盖：
//   1. Sign / Verify 往返：claims 完整还原
//   2. 边界：空 token / 垃圾 token / 错误 secret / 非 HMAC / 过期
//   3. Middleware 三种取 token 路径：X-Admin-Token / Authorization: Bearer / ?token=
//   4. Middleware 优先级：X-Admin-Token > Authorization > query
//   5. GetClaims 在没有 claims 时返回 nil
//
// 跑法：
//   go test ./auth/... -v

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "unit-test-secret-do-not-use-in-prod"

// withSecret 临时设置 JWT_SECRET，测试结束后还原
func withSecret(t *testing.T, secret string) {
	t.Helper()
	prev, hadPrev := os.LookupEnv("JWT_SECRET")
	if err := os.Setenv("JWT_SECRET", secret); err != nil {
		t.Fatalf("setenv JWT_SECRET: %v", err)
	}
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv("JWT_SECRET", prev)
		} else {
			_ = os.Unsetenv("JWT_SECRET")
		}
	})
}

// ===================== Sign / Verify 往返 =====================

func TestSign_Verify_Roundtrip(t *testing.T) {
	withSecret(t, testSecret)
	adminID := uint64(42)
	shopID := "shop-001"
	role := "owner"

	tok, err := Sign(adminID, shopID, role)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if tok == "" {
		t.Fatal("Sign returned empty token")
	}
	// JWT 形态：xxx.yyy.zzz
	if strings.Count(tok, ".") != 2 {
		t.Errorf("token not in JWT format: %q", tok)
	}

	claims, err := Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.AdminID != adminID {
		t.Errorf("AdminID want %d got %d", adminID, claims.AdminID)
	}
	if claims.ShopID != shopID {
		t.Errorf("ShopID want %q got %q", shopID, claims.ShopID)
	}
	if claims.Role != role {
		t.Errorf("Role want %q got %q", role, claims.Role)
	}
	if claims.Issuer != "chatwitheino" {
		t.Errorf("Issuer want chatwitheino got %q", claims.Issuer)
	}
	if claims.ExpiresAt == nil {
		t.Error("ExpiresAt should be set")
	} else if time.Until(claims.ExpiresAt.Time) < 6*24*time.Hour {
		t.Errorf("ExpiresAt should be ~7 days out, got %v", claims.ExpiresAt.Time)
	}
}

// ===================== Verify 边界 =====================

func TestVerify_EmptyToken(t *testing.T) {
	_, err := Verify("")
	if err == nil {
		t.Fatal("empty token should error")
	}
}

func TestVerify_GarbageToken(t *testing.T) {
	_, err := Verify("not-a-jwt")
	if err == nil {
		t.Fatal("garbage token should error")
	}
}

func TestVerify_WrongSecret(t *testing.T) {
	// 用 secret-A 签，用 secret-B 校验
	withSecret(t, "secret-A")
	tok, err := Sign(1, "shop", "owner")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	withSecret(t, "secret-B")
	if _, err := Verify(tok); err == nil {
		t.Fatal("token signed with different secret must fail to verify")
	}
}

func TestVerify_RejectsNonHMAC(t *testing.T) {
	// 手搓一个 alg=none 的 token，Verify 必须拒绝（防止 alg confusion 攻击）
	withSecret(t, testSecret)
	// alg=none, payload={"aid":1,"sid":"x","role":"owner"}
	header := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0" // {"alg":"none","typ":"JWT"}
	payload := "eyJhaWQiOjEsInNpZCI6IngiLCJyb2xlIjoib3duZXIifQ"
	bogus := header + "." + payload + "."

	if _, err := Verify(bogus); err == nil {
		t.Fatal("alg=none token must be rejected")
	}
}

func TestVerify_ExpiredToken(t *testing.T) {
	withSecret(t, testSecret)
	// 手搓一个已过期的 token（claims 里 ExpiresAt 早于 now）
	claims := Claims{
		AdminID: 1,
		ShopID:  "shop",
		Role:    "owner",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
			Issuer:    "chatwitheino",
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := Verify(signed); err == nil {
		t.Fatal("expired token must fail to verify")
	}
}

// ===================== Middleware =====================

// newCtxWithHeaders 构造一个带指定 header / query 的 hertz RequestContext。
//
// Hertz 的 Request/Response 包含 NoCopy 锁，不能用 `=` 直接赋值；用 CopyTo
// 把准备好的 Request 拷进 ctx.Request。Response 直接用 AcquireResponse() 初始化。
func newCtxWithHeaders(t *testing.T, headers map[string]string, query map[string]string) *app.RequestContext {
	t.Helper()
	ctx := app.NewContext(0)
	req := protocol.NewRequest("GET", "/test", nil)
	req.Header.SetMethod("GET")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if len(query) > 0 {
		parts := make([]string, 0, len(query))
		for k, v := range query {
			parts = append(parts, k+"="+v)
		}
		req.SetQueryString(strings.Join(parts, "&"))
	}
	req.CopyTo(&ctx.Request)
	resp := protocol.AcquireResponse()
	resp.CopyTo(&ctx.Response)
	return ctx
}

// callMiddleware 跑 Middleware，通过 ctx.Response.StatusCode 和 GetClaims 判断结果。
//
// 中间件通过 c.Abort() 中断后续 handler；这里我们没法挂"next"回调进 middleware 链，
// 所以用"Abort 后 status code 会被设为 401" + "没 Abort 时 claims 应该被挂上 ctx"来判定。
func callMiddleware(t *testing.T, ctx *app.RequestContext) (claims *Claims, aborted bool, statusCode int) {
	t.Helper()
	mw := Middleware()
	mw(context.Background(), ctx)
	statusCode = ctx.Response.StatusCode()
	if statusCode == http.StatusUnauthorized {
		return nil, true, statusCode
	}
	return GetClaims(ctx), false, statusCode
}

func TestMiddleware_XAdminTokenHeader(t *testing.T) {
	withSecret(t, testSecret)
	tok, err := Sign(7, "shop-X", "manager")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	ctx := newCtxWithHeaders(t, map[string]string{"X-Admin-Token": tok}, nil)

	claims, aborted, _ := callMiddleware(t, ctx)
	if aborted {
		t.Fatal("middleware aborted on valid X-Admin-Token")
	}
	if claims == nil {
		t.Fatal("claims should be set after middleware")
	}
	if claims.AdminID != 7 || claims.ShopID != "shop-X" || claims.Role != "manager" {
		t.Errorf("claims mismatch: %+v", claims)
	}
}

func TestMiddleware_AuthorizationBearer(t *testing.T) {
	withSecret(t, testSecret)
	tok, _ := Sign(8, "shop-Y", "staff")
	ctx := newCtxWithHeaders(t, map[string]string{"Authorization": "Bearer " + tok}, nil)

	claims, aborted, _ := callMiddleware(t, ctx)
	if aborted {
		t.Fatal("middleware aborted on valid Authorization Bearer")
	}
	if claims == nil || claims.AdminID != 8 {
		t.Errorf("claims mismatch: %+v", claims)
	}
}

func TestMiddleware_AuthorizationWithoutBearer(t *testing.T) {
	// Authorization 头不带 "Bearer " 前缀，应当被忽略（不报错，但也没 claims）
	withSecret(t, testSecret)
	tok, _ := Sign(9, "shop", "staff")
	ctx := newCtxWithHeaders(t, map[string]string{"Authorization": tok}, nil)

	_, aborted, _ := callMiddleware(t, ctx)
	if !aborted {
		t.Fatal("middleware should abort when Authorization has no Bearer prefix")
	}
}

func TestMiddleware_QueryToken(t *testing.T) {
	withSecret(t, testSecret)
	tok, _ := Sign(10, "shop", "staff")
	ctx := newCtxWithHeaders(t, nil, map[string]string{"token": tok})

	claims, aborted, _ := callMiddleware(t, ctx)
	if aborted {
		t.Fatal("middleware aborted on valid query token")
	}
	if claims == nil || claims.AdminID != 10 {
		t.Errorf("claims mismatch: %+v", claims)
	}
}

func TestMiddleware_NoToken(t *testing.T) {
	withSecret(t, testSecret)
	ctx := newCtxWithHeaders(t, nil, nil)

	_, aborted, status := callMiddleware(t, ctx)
	if !aborted {
		t.Fatal("middleware should abort when no token provided")
	}
	if status != http.StatusUnauthorized {
		t.Errorf("status want 401, got %d", status)
	}
}

func TestMiddleware_InvalidToken(t *testing.T) {
	withSecret(t, testSecret)
	ctx := newCtxWithHeaders(t, map[string]string{"X-Admin-Token": "garbage"}, nil)

	_, aborted, status := callMiddleware(t, ctx)
	if !aborted {
		t.Fatal("middleware should abort on invalid token")
	}
	if status != http.StatusUnauthorized {
		t.Errorf("status want 401, got %d", status)
	}
}

func TestMiddleware_HeaderPriority(t *testing.T) {
	// X-Admin-Token 应该比 Authorization 优先：即使 Authorization 是合法 token，
	// 只要 X-Admin-Token 是无效的，整体应该 401。
	withSecret(t, testSecret)
	validTok, _ := Sign(1, "shop", "owner")
	ctx := newCtxWithHeaders(t, map[string]string{
		"X-Admin-Token":  "invalid",
		"Authorization":  "Bearer " + validTok,
	}, nil)

	_, aborted, _ := callMiddleware(t, ctx)
	if !aborted {
		t.Fatal("when X-Admin-Token is invalid, middleware must reject (ignore Authorization fallback)")
	}
}

// ===================== GetClaims =====================

func TestGetClaims_NotSet(t *testing.T) {
	ctx := app.NewContext(0)
	if cl := GetClaims(ctx); cl != nil {
		t.Errorf("GetClaims on fresh ctx should return nil, got %+v", cl)
	}
}

func TestGetClaims_WrongType(t *testing.T) {
	// ctx 里塞了非 *Claims 的值，GetClaims 应该安全返回 nil
	ctx := app.NewContext(0)
	ctx.Set("auth_claims", "not-claims")
	if cl := GetClaims(ctx); cl != nil {
		t.Errorf("GetClaims with wrong type should return nil, got %+v", cl)
	}
}