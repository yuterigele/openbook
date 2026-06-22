// Package wecom 多店路由：根据 CorpID 找对应的 Crypto + Client
package wecom

import (
	"errors"
	"sync"

	"github.com/yuterigele/openbook/storage"
)

// ShopCrypto per-shop 加解密实例（每个店铺 CorpID 独立 Token/AESKey）
type ShopCrypto struct {
	CorpID string // 冗余存一份，避免反查
	ShopID string
	Crypto *Crypto
	Client *Client
}

// Router 多店路由
type Router struct {
	mu     sync.RWMutex
	shops  map[string]*ShopCrypto // corpID → shop crypto
}

// NewRouter 构造一个空 router
func NewRouter() *Router {
	return &Router{shops: map[string]*ShopCrypto{}}
}

// Register 注册一个店铺（从 Shop 记录构造 Crypto + Client）
func (r *Router) Register(shop *storage.Shop) error {
	if shop.WecomCorpID == "" {
		return errors.New("shop has no WecomCorpID")
	}
	if shop.WecomToken == "" || shop.WecomEncodingAESKey == "" {
		return errors.New("shop missing WecomToken / WecomEncodingAESKey")
	}
	crypto, err := NewCrypto(shop.WecomToken, shop.WecomEncodingAESKey, shop.WecomCorpID)
	if err != nil {
		return err
	}
	client := NewClient(shop.WecomCorpID, shop.WecomSecret, shop.WecomAgentID)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.shops[shop.WecomCorpID] = &ShopCrypto{
		CorpID: shop.WecomCorpID,
		ShopID: shop.ID,
		Crypto: crypto,
		Client: client,
	}
	return nil
}

// ReloadFromDB 从 DB 重新加载所有 Shop 的 Crypto（用于运行时新增店铺）
func (r *Router) ReloadFromDB(ctx ...interface{}) error {
	// 用 storage.DB 查 shops 表
	type shopRow struct {
		ID                string
		WecomCorpID       string
		WecomAgentID      int
		WecomSecret       string
		WecomToken        string
		WecomEncodingAESKey string
	}
	var rows []shopRow
	if err := storage.DB.Table("shops").
		Where("wecom_corp_id <> '' AND wecom_token <> '' AND wecom_encoding_aes_key <> ''").
		Scan(&rows).Error; err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.shops = map[string]*ShopCrypto{}
	for _, s := range rows {
		crypto, err := NewCrypto(s.WecomToken, s.WecomEncodingAESKey, s.WecomCorpID)
		if err != nil {
			continue
		}
		client := NewClient(s.WecomCorpID, s.WecomSecret, s.WecomAgentID)
		r.shops[s.WecomCorpID] = &ShopCrypto{
			CorpID: s.WecomCorpID,
			ShopID: s.ID,
			Crypto: crypto,
			Client: client,
		}
	}
	return nil
}

// Lookup 按 CorpID 找 ShopCrypto
func (r *Router) Lookup(corpID string) (*ShopCrypto, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sc, ok := r.shops[corpID]
	return sc, ok
}

// LookupCorpIDByPtr 用 ShopCrypto 指针反查 corpID（O(n)）
func (r *Router) LookupCorpIDByPtr(target *ShopCrypto) string {
	if target == nil {
		return ""
	}
	// 优先用结构体自带的 CorpID
	if target.CorpID != "" {
		return target.CorpID
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for corpID, sc := range r.shops {
		if sc == target {
			return corpID
		}
	}
	return ""
}

// Count 返回已注册的店铺数（用于监控 / 启动检查）
func (r *Router) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.shops)
}

// AllShops 返回所有已注册 ShopCrypto 的快照（调用方勿修改）
func (r *Router) AllShops() []*ShopCrypto {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*ShopCrypto, 0, len(r.shops))
	for _, sc := range r.shops {
		out = append(out, sc)
	}
	return out
}

// Fallback 默认的旧单 corpID 配置（启动时如果 router 是空的，回退到这个）
type Fallback struct {
	CorpID         string
	Token          string
	EncodingAESKey string
	AgentID        int
	Secret         string
}

// SetFallback 设置回退配置（仅用于兼容旧的 .env 单 corpID 部署）
func (r *Router) SetFallback(fb Fallback) error {
	if fb.CorpID == "" {
		return nil
	}
	crypto, err := NewCrypto(fb.Token, fb.EncodingAESKey, fb.CorpID)
	if err != nil {
		return err
	}
	client := NewClient(fb.CorpID, fb.Secret, fb.AgentID)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shops[fb.CorpID] = &ShopCrypto{
		CorpID: fb.CorpID,
		ShopID: "default",
		Crypto: crypto,
		Client: client,
	}
	return nil
}