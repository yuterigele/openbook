// Package wecom routes callbacks and outbound requests for configured shops.
package wecom

import (
	"errors"
	"sync"

	"github.com/yuterigele/openbook/storage"
)

// ShopCrypto contains a shop's routing identity and the CorpID-scoped client.
// Shops in the same enterprise share Crypto and Client, but retain their own
// ShopID and OpenKfID routing entries.
type ShopCrypto struct {
	CorpID string
	ShopID string
	Crypto *Crypto
	Client *Client
}

// Router indexes cryptographic material by CorpID and shops by their stable
// identifiers. CorpID is deliberately not a shop key: one enterprise can host
// several shops, distinguished by their OpenKfID.
type Router struct {
	mu          sync.RWMutex
	corps       map[string]*ShopCrypto
	shops       map[string]*ShopCrypto
	openKfShops map[string]*ShopCrypto
}

func NewRouter() *Router {
	return &Router{
		corps:       map[string]*ShopCrypto{},
		shops:       map[string]*ShopCrypto{},
		openKfShops: map[string]*ShopCrypto{},
	}
}

func newShopCrypto(shop *storage.Shop, existing *ShopCrypto) (*ShopCrypto, error) {
	if shop.WecomCorpID == "" || shop.WecomToken == "" || shop.WecomEncodingAESKey == "" {
		return nil, errors.New("shop missing WeCom CorpID, Token, or EncodingAESKey")
	}
	if existing != nil {
		return &ShopCrypto{CorpID: shop.WecomCorpID, ShopID: shop.ID, Crypto: existing.Crypto, Client: existing.Client}, nil
	}
	crypto, err := NewCrypto(shop.WecomToken, shop.WecomEncodingAESKey, shop.WecomCorpID)
	if err != nil {
		return nil, err
	}
	return &ShopCrypto{CorpID: shop.WecomCorpID, ShopID: shop.ID, Crypto: crypto, Client: NewClient(shop.WecomCorpID, shop.WecomSecret, shop.WecomAgentID)}, nil
}

// Register adds or updates one shop. Multiple shops may share a CorpID.
func (r *Router) Register(shop *storage.Shop) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	sc, err := newShopCrypto(shop, r.corps[shop.WecomCorpID])
	if err != nil {
		return err
	}
	if old := r.shops[shop.ID]; old != nil && old.ShopID == shop.ID {
		for openKfID, candidate := range r.openKfShops {
			if candidate == old {
				delete(r.openKfShops, openKfID)
			}
		}
	}
	r.corps[shop.WecomCorpID] = firstNonNil(r.corps[shop.WecomCorpID], sc)
	r.shops[shop.ID] = sc
	if shop.OpenKfID != "" {
		if old, exists := r.openKfShops[shop.OpenKfID]; exists && old.ShopID != shop.ID {
			return errors.New("open_kf_id is already assigned to another shop")
		}
		r.openKfShops[shop.OpenKfID] = sc
	}
	return nil
}

func firstNonNil(a, b *ShopCrypto) *ShopCrypto {
	if a != nil {
		return a
	}
	return b
}

// ReloadFromDB replaces the routing snapshot with all completely configured shops.
func (r *Router) ReloadFromDB(ctx ...interface{}) error {
	var rows []storage.Shop
	if err := storage.DB.Where("wecom_corp_id <> '' AND wecom_token <> '' AND wecom_encoding_aes_key <> ''").Find(&rows).Error; err != nil {
		return err
	}
	return r.reload(rows, nil)
}

// ReloadFromDBWithConfig loads shop routing identities from the database while
// using one shared enterprise-WeChat configuration for every shop. This is the
// normal deployment mode when all shops belong to the same enterprise.
//
// Shop-level OpenKfID and other routing fields remain in the database. Non-zero
// config values override the legacy per-shop credential columns, allowing old
// deployments to keep working while avoiding duplicated secrets in every row.
func (r *Router) ReloadFromDBWithConfig(cfg *Config) error {
	var rows []storage.Shop
	if err := storage.DB.Find(&rows).Error; err != nil {
		return err
	}
	return r.reload(rows, cfg)
}

func (r *Router) reload(rows []storage.Shop, cfg *Config) error {
	next := NewRouter()
	for i := range rows {
		applySharedConfig(&rows[i], cfg)
		if rows[i].WecomCorpID == "" || rows[i].WecomToken == "" || rows[i].WecomEncodingAESKey == "" {
			continue
		}
		if err := next.Register(&rows[i]); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.corps, r.shops, r.openKfShops = next.corps, next.shops, next.openKfShops
	return nil
}

func applySharedConfig(shop *storage.Shop, cfg *Config) {
	if shop == nil || cfg == nil {
		return
	}
	if cfg.CorpID != "" {
		shop.WecomCorpID = cfg.CorpID
	}
	if cfg.AgentID != 0 {
		shop.WecomAgentID = cfg.AgentID
	}
	if cfg.Secret != "" {
		shop.WecomSecret = cfg.Secret
	}
	if cfg.Token != "" {
		shop.WecomToken = cfg.Token
	}
	if cfg.EncodingAESKey != "" {
		shop.WecomEncodingAESKey = cfg.EncodingAESKey
	}
	if cfg.KFLink != "" {
		shop.WecomKFLink = cfg.KFLink
	}
}

func (r *Router) Lookup(corpID string) (*ShopCrypto, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sc, ok := r.corps[corpID]
	return sc, ok
}
func (r *Router) LookupByShopID(shopID string) (*ShopCrypto, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sc, ok := r.shops[shopID]
	return sc, ok
}
func (r *Router) LookupByOpenKfID(openKfID string) (*ShopCrypto, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sc, ok := r.openKfShops[openKfID]
	return sc, ok
}
func (r *Router) LookupCorpIDByPtr(target *ShopCrypto) string {
	if target == nil {
		return ""
	}
	return target.CorpID
}
func (r *Router) Count() int     { r.mu.RLock(); defer r.mu.RUnlock(); return len(r.shops) }
func (r *Router) CorpCount() int { r.mu.RLock(); defer r.mu.RUnlock(); return len(r.corps) }
func (r *Router) AllShops() []*ShopCrypto {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*ShopCrypto, 0, len(r.corps))
	for _, sc := range r.corps {
		out = append(out, sc)
	}
	return out
}
func (r *Router) SingleClient() (*Client, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.corps) != 1 {
		return nil, false
	}
	for _, sc := range r.corps {
		return sc.Client, true
	}
	return nil, false
}

// SingleShop returns the only configured shop. It is used as a safe fallback
// for a single-shop deployment when the first WeChat Customer Service callback
// reveals its open_kfid before that ID has been persisted in the shop record.
func (r *Router) SingleShop() (*ShopCrypto, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.shops) != 1 {
		return nil, false
	}
	for _, sc := range r.shops {
		return sc, true
	}
	return nil, false
}
