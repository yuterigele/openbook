package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

func TestListAuditsHandlerKeepsTenantIsolation(t *testing.T) {
	setupAPITestDB(t)
	for _, shopID := range []string{"shop-audit-a", "shop-audit-b"} {
		if err := storage.WriteAudit(context.Background(), storage.AuditLog{
			ShopID: shopID, Action: "test.action", ResourceType: "test", ResourceID: shopID,
			Outcome: storage.AuditOutcomeSuccess,
		}, nil); err != nil {
			t.Fatalf("WriteAudit: %v", err)
		}
	}
	c := newAPIContext(t, http.MethodGet, "/api/admin/audits", nil,
		withClaims(&auth.Claims{AdminID: 1, ShopID: "shop-audit-a", Role: storage.RoleOwner}),
		withQuery("shop_id", "shop-audit-b"))
	status, body := runHandler(t, listAuditsHandler, c)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var response struct {
		Items []storage.AuditLog `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].ShopID != "shop-audit-a" {
		t.Fatalf("tenant leak: %+v", response.Items)
	}
}
