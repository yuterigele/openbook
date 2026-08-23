package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func saveInboxForTest(t *testing.T, msgID, shopID string) {
	t.Helper()
	err := SaveKfInboxBatchAndCursor(context.Background(), "kf-1", "cursor-1", []KfInboxInput{{
		MsgID: msgID, ShopID: shopID, OpenKfID: "kf-1", ExternalUserID: "customer-1",
		Content: "我要预约", SendTime: time.Now().Unix(),
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSaveKfInboxBatchAndCursorIsIdempotent(t *testing.T) {
	SetupTestDB(t)
	saveInboxForTest(t, "msg-1", "shop-1")
	saveInboxForTest(t, "msg-1", "shop-1")
	var count int64
	if err := DB.Model(&KfInboxMessage{}).Where("msg_id = ?", "msg-1").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("inbox rows = %d, want 1", count)
	}
	cursor, err := GetKfCursor("kf-1")
	if err != nil || cursor != "cursor-1" {
		t.Fatalf("cursor = %q err=%v", cursor, err)
	}
}

func TestKfInboxClaimFailureRetryAndDeadLetter(t *testing.T) {
	SetupTestDB(t)
	saveInboxForTest(t, "msg-retry", "shop-1")
	claimed, err := ClaimKfInboxDue(context.Background(), "kf-1", 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%d err=%v", len(claimed), err)
	}
	if claimed[0].Attempts != 1 || claimed[0].LeaseToken == "" {
		t.Fatalf("invalid claim: %+v", claimed[0])
	}
	if err := MarkKfInboxFailed(context.Background(), claimed[0].MsgID, claimed[0].LeaseToken,
		claimed[0].Attempts, 2, errors.New("temporary")); err != nil {
		t.Fatal(err)
	}
	if err := DB.Model(&KfInboxMessage{}).Where("msg_id = ?", "msg-retry").Update("next_retry_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	claimed, err = ClaimKfInboxDue(context.Background(), "kf-1", 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("second claim=%d err=%v", len(claimed), err)
	}
	if err := MarkKfInboxFailed(context.Background(), claimed[0].MsgID, claimed[0].LeaseToken,
		claimed[0].Attempts, 2, errors.New("still broken")); err != nil {
		t.Fatal(err)
	}
	var row KfInboxMessage
	if err := DB.First(&row, "msg_id = ?", "msg-retry").Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != KfInboxDeadLetter || row.NextRetryAt != nil {
		t.Fatalf("status=%s next=%v", row.Status, row.NextRetryAt)
	}
}

func TestKfInboxExpiredLeaseCanBeRecovered(t *testing.T) {
	SetupTestDB(t)
	saveInboxForTest(t, "msg-expired", "shop-1")
	first, _ := ClaimKfInboxDue(context.Background(), "kf-1", 1, time.Minute)
	if len(first) != 1 {
		t.Fatal("first claim missing")
	}
	if err := DB.Model(&KfInboxMessage{}).Where("msg_id = ?", "msg-expired").Update("lease_until", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	second, err := ClaimKfInboxDue(context.Background(), "kf-1", 1, time.Minute)
	if err != nil || len(second) != 1 {
		t.Fatalf("recovered=%d err=%v", len(second), err)
	}
	if second[0].LeaseToken == first[0].LeaseToken {
		t.Fatal("recovered claim must use a new lease token")
	}
	if err := MarkKfInboxSucceeded(context.Background(), first[0].MsgID, first[0].LeaseToken); err == nil {
		t.Fatal("stale worker must not overwrite the recovered lease")
	}
}

func TestReplayKfInboxMessageEnforcesShopBoundary(t *testing.T) {
	SetupTestDB(t)
	saveInboxForTest(t, "msg-dead", "shop-a")
	if err := DB.Model(&KfInboxMessage{}).Where("msg_id = ?", "msg-dead").Update("status", KfInboxDeadLetter).Error; err != nil {
		t.Fatal(err)
	}
	if err := ReplayKfInboxMessage(context.Background(), "msg-dead", "shop-b", false); err == nil {
		t.Fatal("another shop must not replay this message")
	}
	if err := ReplayKfInboxMessage(context.Background(), "msg-dead", "shop-a", false); err != nil {
		t.Fatal(err)
	}
	var row KfInboxMessage
	DB.First(&row, "msg_id = ?", "msg-dead")
	if row.Status != KfInboxPending || row.Attempts != 0 || row.ReplayedAt == nil {
		t.Fatalf("unexpected replayed row: %+v", row)
	}
}

func TestCleanupSucceededKfInboxKeepsFailures(t *testing.T) {
	SetupTestDB(t)
	saveInboxForTest(t, "msg-success-old", "shop-a")
	saveInboxForTest(t, "msg-dead-old", "shop-a")
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := DB.Model(&KfInboxMessage{}).Where("msg_id = ?", "msg-success-old").Updates(map[string]any{
		"status": KfInboxSucceeded, "processed_at": old,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Model(&KfInboxMessage{}).Where("msg_id = ?", "msg-dead-old").Updates(map[string]any{
		"status": KfInboxDeadLetter, "processed_at": old,
	}).Error; err != nil {
		t.Fatal(err)
	}
	deleted, err := CleanupSucceededKfInbox(context.Background(), time.Now().Add(-7*24*time.Hour))
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	if _, err := GetKfInboxMessage(context.Background(), "msg-dead-old", "shop-a", false); err != nil {
		t.Fatalf("dead-letter must be retained: %v", err)
	}
}
