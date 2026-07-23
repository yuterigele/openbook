package wecom

import (
	"testing"

	"github.com/yuterigele/openbook/storage"
)

func TestRouter_SharedCorpIDRoutesEachShopByOpenKfID(t *testing.T) {
	r := NewRouter()
	const corpID = "ww-shared"
	const aesKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	for _, shop := range []*storage.Shop{
		{ID: "shop-a", WecomCorpID: corpID, WecomSecret: "secret", WecomToken: "token", WecomEncodingAESKey: aesKey, OpenKfID: "kf-a"},
		{ID: "shop-b", WecomCorpID: corpID, WecomSecret: "secret", WecomToken: "token", WecomEncodingAESKey: aesKey, OpenKfID: "kf-b"},
	} {
		if err := r.Register(shop); err != nil {
			t.Fatalf("Register(%s): %v", shop.ID, err)
		}
	}

	if got := r.Count(); got != 2 {
		t.Fatalf("shop count = %d, want 2", got)
	}
	if got := r.CorpCount(); got != 1 {
		t.Fatalf("corp count = %d, want 1", got)
	}
	for openKfID, wantShopID := range map[string]string{"kf-a": "shop-a", "kf-b": "shop-b"} {
		sc, ok := r.LookupByOpenKfID(openKfID)
		if !ok || sc.ShopID != wantShopID {
			t.Fatalf("LookupByOpenKfID(%q) = %#v, %v; want shop %q", openKfID, sc, ok, wantShopID)
		}
	}
	if _, ok := r.SingleClient(); !ok {
		t.Fatal("SingleClient() = no client, want shared-CorpID client")
	}
}
