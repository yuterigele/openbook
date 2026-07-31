package wecom

import (
	"testing"

	"github.com/yuterigele/openbook/storage"
)

func TestApplySharedConfigOverridesEnterpriseFieldsOnly(t *testing.T) {
	shop := &storage.Shop{
		ID:                  "shop-a",
		WecomCorpID:         "old-corp",
		WecomAgentID:        1,
		WecomSecret:         "old-secret",
		WecomToken:          "old-token",
		WecomEncodingAESKey: "old-aes",
		WecomKFLink:         "old-link",
		OpenKfID:            "kf-shop-a",
	}
	cfg := &Config{
		CorpID:         "shared-corp",
		AgentID:        100001,
		Secret:         "shared-secret",
		Token:          "shared-token",
		EncodingAESKey: "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG",
		KFLink:         "https://work.weixin.qq.com/kfid/shared",
	}

	applySharedConfig(shop, cfg)

	if shop.WecomCorpID != cfg.CorpID ||
		shop.WecomAgentID != cfg.AgentID ||
		shop.WecomSecret != cfg.Secret ||
		shop.WecomToken != cfg.Token ||
		shop.WecomEncodingAESKey != cfg.EncodingAESKey ||
		shop.WecomKFLink != cfg.KFLink {
		t.Fatalf("enterprise config not applied: %#v", shop)
	}
	if shop.ID != "shop-a" || shop.OpenKfID != "kf-shop-a" {
		t.Fatalf("shop routing identity was overwritten: %#v", shop)
	}
}

func TestApplySharedConfigKeepsDatabaseValuesForEmptyEnvFields(t *testing.T) {
	shop := &storage.Shop{
		WecomCorpID: "db-corp",
		WecomToken:  "db-token",
		OpenKfID:    "kf-shop-a",
	}

	applySharedConfig(shop, &Config{Secret: "env-secret"})

	if shop.WecomCorpID != "db-corp" || shop.WecomToken != "db-token" {
		t.Fatalf("empty environment fields must not erase database values: %#v", shop)
	}
	if shop.WecomSecret != "env-secret" {
		t.Fatalf("non-empty environment field was not applied: %#v", shop)
	}
}

func TestRouterReloadUsesSharedConfigForUnconfiguredShop(t *testing.T) {
	r := NewRouter()
	cfg := &Config{
		CorpID:         "shared-corp",
		AgentID:        100001,
		Secret:         "shared-secret",
		Token:          "shared-token",
		EncodingAESKey: "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG",
	}

	if err := r.reload([]storage.Shop{{ID: "default", OpenKfID: "kf-default"}}, cfg); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if r.Count() != 1 || r.CorpCount() != 1 {
		t.Fatalf("router counts = shops:%d corps:%d, want 1/1", r.Count(), r.CorpCount())
	}
	sc, ok := r.LookupByShopID("default")
	if !ok || sc.CorpID != cfg.CorpID || sc.Client == nil || sc.Crypto == nil {
		t.Fatalf("shared config route = %#v, %v", sc, ok)
	}
	if got, ok := r.LookupByOpenKfID("kf-default"); !ok || got.ShopID != "default" {
		t.Fatalf("open_kf route = %#v, %v", got, ok)
	}
}
