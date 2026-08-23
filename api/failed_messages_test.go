package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/route/param"
	"github.com/yuterigele/openbook/storage"
)

func plantFailedMessage(t *testing.T, msgID, shopID, status string) {
	t.Helper()
	err := storage.SaveKfInboxBatchAndCursor(context.Background(), "kf-1", "cursor-1", []storage.KfInboxInput{{
		MsgID: msgID, ShopID: shopID, OpenKfID: "kf-1", ExternalUserID: "customer-1",
		Content: "我要预约", SendTime: time.Now().Unix(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.DB.Model(&storage.KfInboxMessage{}).Where("msg_id = ?", msgID).
		Updates(map[string]any{"status": status, "last_error": "模型超时"}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestListFailedMessagesEnforcesShopBoundary(t *testing.T) {
	setupAPITestDB(t)
	shopA, shopB := newShopID(), newShopID()
	storage.MakeShop(t, shopA, "")
	storage.MakeShop(t, shopB, "")
	plantFailedMessage(t, "msg-a", shopA, storage.KfInboxDeadLetter)
	plantFailedMessage(t, "msg-b", shopB, storage.KfInboxDeadLetter)
	ctx := newAPIContext(t, "GET", "/api/admin/failed-messages", nil, withClaims(adminClaims(shopA)))
	status, body := runHandler(t, listFailedMessagesHandler, ctx)
	if status != statusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var response struct {
		Items []storage.KfInboxMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].MsgID != "msg-a" {
		t.Fatalf("unexpected rows: %+v", response.Items)
	}
}

func TestReplayFailedMessageRejectsOtherShop(t *testing.T) {
	setupAPITestDB(t)
	shopA, shopB := newShopID(), newShopID()
	storage.MakeShop(t, shopA, "")
	storage.MakeShop(t, shopB, "")
	plantFailedMessage(t, "msg-private", shopA, storage.KfInboxDeadLetter)
	ctx := newAPIContext(t, "POST", "/api/admin/failed-messages/msg-private/replay", nil, withClaims(adminClaims(shopB)))
	ctx.Params = append(ctx.Params, param.Param{Key: "id", Value: "msg-private"})
	status, _ := runHandler(t, replayFailedMessageHandler, ctx)
	if status != statusNotFound {
		t.Fatalf("status=%d, want 404", status)
	}
}

func TestReplayFailedMessageQueuesRetry(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	plantFailedMessage(t, "msg-replay", shopID, storage.KfInboxDeadLetter)
	ctx := newAPIContext(t, "POST", "/api/admin/failed-messages/msg-replay/replay", nil, withClaims(adminClaims(shopID)))
	ctx.Params = append(ctx.Params, param.Param{Key: "id", Value: "msg-replay"})
	status, body := runHandler(t, replayFailedMessageHandler, ctx)
	if status != 202 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	row, err := storage.GetKfInboxMessage(context.Background(), "msg-replay", shopID, false)
	if err != nil || row.Status != storage.KfInboxPending || row.Attempts != 0 {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}
