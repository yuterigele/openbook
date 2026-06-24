package storage

// api_keys.go —— API key 存储（v4.12.1 api_access feature 实战）
//
// 设计：
//   - 明文 token 只在 CreateAPIKey 返一次（DB 只存 SHA256 哈希）
//   - 格式：`apikey_<32 hex>`（prefix 用于列表展示 + 调试）
//   - 默认有效期 1 年；revoked 即使明文对也拒
//   - scopes：JSON 数组字符串（sqlite 无 []string native）

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// APIKeyPrefix API key 明文前缀（用户可见 + 调试）
const APIKeyPrefix = "apikey_"

// APIKeyDefaultLifetime 默认有效期
const APIKeyDefaultLifetime = 365 * 24 * time.Hour

// ErrAPIKeyInvalid token 无效（不存在 / revoked / 过期）
var ErrAPIKeyInvalid = errors.New("api key invalid")

// ErrAPIKeyScopeInsufficient scope 不够
var ErrAPIKeyScopeInsufficient = errors.New("api key scope insufficient")

// CreateAPIKey 生成新 API key
//
//   - 明文 token 只在返回值 plaintext 字段出现一次，调用方负责展示给用户并立即丢弃
//   - hash 用 SHA256(plaintext) 存 DB
//   - prefix 存明文前 16 字符（"apikey_xxxxxxxx"）便于列表展示
//
// 返回值：
//   - plaintext：明文 token（apikey_<64 hex>），**只这一次**
//   - key：DB 记录（不含 hash 给前端，但内部用）
func CreateAPIKey(ctx context.Context, shopID, name string, scopes []string, lifetime time.Duration) (plaintext string, key *APIKey, err error) {
	if DB == nil {
		return "", nil, errors.New("storage.DB 未初始化")
	}
	if shopID == "" {
		return "", nil, errors.New("shop_id 不能为空")
	}
	if name == "" {
		return "", nil, errors.New("name 不能为空")
	}
	if scopes == nil {
		scopes = []string{}
	}
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return "", nil, fmt.Errorf("marshal scopes: %w", err)
	}
	if lifetime <= 0 {
		lifetime = APIKeyDefaultLifetime
	}
	// 生成 32 字节随机 → 64 hex
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	plaintext = APIKeyPrefix + hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(plaintext))
	hashHex := hex.EncodeToString(hash[:])

	key = &APIKey{
		ShopID:      shopID,
		Name:        name,
		TokenHash:   hashHex,
		TokenPrefix: plaintext[:16],
		Scopes:      string(scopesJSON),
		Status:      "active",
		ExpiresAt:   time.Now().Add(lifetime),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := DB.WithContext(ctx).Create(key).Error; err != nil {
		return "", nil, err
	}
	return plaintext, key, nil
}

// ListAPIKeys 列出某 shop 的所有 API key（不含 hash）
//
//   - 已 revoked 也返（让店主看历史）
//   - 按 id 降序（新→旧）
func ListAPIKeys(ctx context.Context, shopID string) ([]APIKey, error) {
	if DB == nil {
		return nil, errors.New("storage.DB 未初始化")
	}
	var out []APIKey
	if err := DB.WithContext(ctx).
		Where("shop_id = ?", shopID).
		Order("id desc").
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeAPIKey 吊销某 API key
//
//   - 幂等：已 revoked 再调也成功
//   - 不能跨店吊销（必须先校验 shopID）
func RevokeAPIKey(ctx context.Context, shopID string, keyID uint64, note string) error {
	if DB == nil {
		return errors.New("storage.DB 未初始化")
	}
	now := time.Now()
	res := DB.WithContext(ctx).Model(&APIKey{}).
		Where("id = ? AND shop_id = ?", keyID, shopID).
		Updates(map[string]interface{}{
			"status":      "revoked",
			"revoked_at":  now,
			"revoke_note": note,
			"updated_at":  now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("api key 不存在或不属于此 shop")
	}
	return nil
}

// ValidateAPIKey 校验明文 token，返 shopID + scopes
//
//   - 不存在 / 已 revoked / 已过期 → ErrAPIKeyInvalid
//   - 返回时**不**自动更新 LastUsedAt（v4.12.1 简化：留 v4.13 异步更新）
func ValidateAPIKey(ctx context.Context, token string) (shopID string, scopes []string, keyID uint64, err error) {
	if DB == nil {
		return "", nil, 0, errors.New("storage.DB 未初始化")
	}
	if !strings.HasPrefix(token, APIKeyPrefix) {
		return "", nil, 0, ErrAPIKeyInvalid
	}
	hash := sha256.Sum256([]byte(token))
	hashHex := hex.EncodeToString(hash[:])
	var key APIKey
	if err := DB.WithContext(ctx).
		Where("token_hash = ?", hashHex).
		First(&key).Error; err != nil {
		return "", nil, 0, ErrAPIKeyInvalid
	}
	if key.Status != "active" {
		return "", nil, 0, ErrAPIKeyInvalid
	}
	if key.ExpiresAt.Before(time.Now()) {
		return "", nil, 0, ErrAPIKeyInvalid
	}
	if key.Scopes != "" {
		_ = json.Unmarshal([]byte(key.Scopes), &scopes)
	}
	return key.ShopID, scopes, key.ID, nil
}

// HasAPIKeyScope 检查 scope 是否在列表里
func HasAPIKeyScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want || s == "*" {
			return true
		}
	}
	return false
}