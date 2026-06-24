package auth

// api_key.go —— API key 鉴权中间件（v4.12.1 api_access feature 实战）
//
// 区别于 JWT（auth.Middleware）：
//   - JWT 给"商户后台登录"，有完整 Claims{AdminID, ShopID, Role}
//   - API key 给"外部系统对接"，用同一份 Claims 但 Role="api_key"，AdminID=keyID
//
//   - 通过 header `Authorization: Bearer apikey_xxx` 鉴权
//   - 校验成功：把 Claims{ShopID, AdminID=keyID, Role="api_key"} + Scopes 塞 ctx
//   - 校验失败：401
//
// 使用：
//   protected := apiKeyAuth()  // 装在 /api/external/* group 上

import (
	"context"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yuterigele/openbook/storage"
)

// apiKeyScopesKey scopes 在 ctx 的 key
const apiKeyScopesKey = "api_key_scopes"

// APIKeyAuth 鉴权中间件
//
//   - 校验 header Authorization: Bearer apikey_xxx
//   - 成功 → 塞 Claims + scopes
//   - 失败 → 401
func APIKeyAuth() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		authz := string(c.GetHeader("Authorization"))
		if !strings.HasPrefix(authz, "Bearer ") {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing or invalid Authorization header (need Bearer apikey_xxx)"})
			c.Abort()
			return
		}
		token := strings.TrimPrefix(authz, "Bearer ")
		shopID, scopes, keyID, err := storage.ValidateAPIKey(ctx, token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid api key: " + err.Error()})
			c.Abort()
			return
		}
		// 复用 Claims 结构（Role="api_key" 标识来源）
		claims := &Claims{
			AdminID: keyID, // 用 key ID 当作 ID 字段，方便日志
			ShopID:  shopID,
			Role:    "api_key",
		}
		c.Set("auth_claims", claims)
		c.Set(apiKeyScopesKey, scopes)
		c.Next(ctx)
	}
}

// GetAPIKeyScopes 从 ctx 取 scopes（APIKeyAuth 后才有）
func GetAPIKeyScopes(c *app.RequestContext) []string {
	v, ok := c.Get(apiKeyScopesKey)
	if !ok {
		return nil
	}
	s, _ := v.([]string)
	return s
}

// RequireAPIKeyScope 中间件：要求 API key 含某 scope
//
//   - 必须装在 APIKeyAuth 之后
//   - 没有 scope → 403
func RequireAPIKeyScope(want string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		scopes := GetAPIKeyScopes(c)
		if !storage.HasAPIKeyScope(scopes, want) {
			c.JSON(http.StatusForbidden, map[string]string{
				"error":          "api key missing required scope: " + want,
				"scope_required": want,
			})
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}