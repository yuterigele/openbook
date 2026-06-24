package api

// api_keys.go —— 商户管理自己的 API key（v4.12.1 api_access feature 实战）
//
// POST /api/admin/api-keys              生成（返明文，仅一次）
// GET  /api/admin/api-keys              列表（不含明文）
// POST /api/admin/api-keys/:id/revoke   吊销
//
// 设计：
//   - perm: view:plan（owner-only，staff 不该发 API key）
//   - feature: api_access（basic 403，flagship+ 允许）
//   - plan: active（frozen 走 middleware 402）
//
// 明文 token 只 POST 创建时返一次，存 DB 的是 SHA256 哈希——列表永远看不到明文

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yuterigele/openbook/storage"
)

// CreateAPIKeyRequest POST /api/admin/api-keys
type CreateAPIKeyRequest struct {
	Name   string   `json:"name"`   // 用户标识（"POS 系统"）
	Scopes []string `json:"scopes"` // ["appointments:read"] —— 不传默认 ["appointments:read"]
}

// APIKeyListItem API key 列表项（不含 hash）
type APIKeyListItem struct {
	ID          uint64    `json:"id"`
	Name        string    `json:"name"`
	TokenPrefix string    `json:"token_prefix"` // 前 16 字符
	Scopes      []string  `json:"scopes"`
	Status      string    `json:"status"`
	ExpiresAt   time.Time `json:"expires_at"`
	LastUsedAt  time.Time `json:"last_used_at"`
	CreatedAt   time.Time `json:"created_at"`
	RevokedAt   time.Time `json:"revoked_at,omitempty"`
}

// CreateAPIKeyResponse POST 创建响应（**包含明文 token，仅这一次**）
type CreateAPIKeyResponse struct {
	Key       APIKeyListItem  `json:"key"`
	Plaintext string          `json:"plaintext"` // ⚠ 仅这一次！前端要展示给用户并提示"立即保存"
	Scopes    []string        `json:"scopes"`
}

// createAPIKeyHandler POST /api/admin/api-keys
//
// perm: view:plan
// feature: api_access
func createAPIKeyHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	var req CreateAPIKeyRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "name 不能为空"})
		return
	}
	if len(req.Name) > 64 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "name 最长 64 字"})
		return
	}
	if req.Scopes == nil || len(req.Scopes) == 0 {
		req.Scopes = []string{"appointments:read"}
	}

	// feature gate: basic 没 api_access
	shop, err := storage.GetShopByID(ctx, shopID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "读 shop 失败: " + err.Error()})
		return
	}
	currentPlan := shop.Plan
	if !storage.IsValidPlanID(currentPlan) {
		currentPlan = storage.DefaultPlanID
	}
	if !storage.HasFeature(currentPlan, storage.FeatureAPIAccess) {
		c.JSON(http.StatusForbidden, map[string]string{
			"error":            "当前 plan 不支持 API 访问，请升级到旗舰或以上版本",
			"feature_required": storage.FeatureAPIAccess,
			"current_plan":     currentPlan,
		})
		return
	}

	// 生成
	plaintext, key, err := storage.CreateAPIKey(ctx, shopID, req.Name, req.Scopes, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "生成失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, CreateAPIKeyResponse{
		Key:       apiKeyToListItem(*key),
		Plaintext: plaintext,
		Scopes:    req.Scopes,
	})
}

// listAPIKeysHandler GET /api/admin/api-keys
//
// perm: view:plan
func listAPIKeysHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	keys, err := storage.ListAPIKeys(ctx, shopID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "列 API key 失败: " + err.Error()})
		return
	}
	items := make([]APIKeyListItem, 0, len(keys))
	for _, k := range keys {
		items = append(items, apiKeyToListItem(k))
	}
	c.JSON(http.StatusOK, map[string]interface{}{
		"keys":  items,
		"total": len(items),
	})
}

// revokeAPIKeyHandler POST /api/admin/api-keys/:id/revoke
//
// perm: view:plan
func revokeAPIKeyHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "id 必须为数字"})
		return
	}
	// 简单 JSON body for note（可选）
	var body struct {
		Note string `json:"note"`
	}
	_ = c.BindAndValidate(&body) // 缺 body 也 OK

	if err := storage.RevokeAPIKey(ctx, shopID, id, body.Note); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "revoked"})
}

// apiKeyToListItem APIKey → APIKeyListItem（拆 scopes JSON 字符串）
func apiKeyToListItem(k storage.APIKey) APIKeyListItem {
	scopes := []string{}
	if k.Scopes != "" {
		// 简单 split（JSON unmarshal 也可，但 scopes 都是简单字符串）
		var s []string
		// 用 json.Unmarshal 不在 api package，加 mini parser
		s = parseJSONScopes(k.Scopes)
		scopes = s
	}
	return APIKeyListItem{
		ID:          k.ID,
		Name:        k.Name,
		TokenPrefix: k.TokenPrefix,
		Scopes:      scopes,
		Status:      k.Status,
		ExpiresAt:   k.ExpiresAt,
		LastUsedAt:  k.LastUsedAt,
		CreatedAt:   k.CreatedAt,
		RevokedAt:   k.RevokedAt,
	}
}

// parseJSONScopes 简单 JSON 数组解析（不引 encoding/json 避免冲突）
//
// scopes 是 `["appointments:read"]` 格式，简单 split
func parseJSONScopes(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "[]" || s == "null" {
		return []string{}
	}
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return []string{}
	}
	body := s[1 : len(s)-1]
	parts := strings.Split(body, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}