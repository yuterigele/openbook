package storage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestWriteAuditInTxUsesTrustedContext(t *testing.T) {
	SetupTestDB(t)
	ctx := WithTraceID(context.Background(), "trace-test-1")
	ctx = WithAuditActor(ctx, AuditActorAdmin, "admin-1")

	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return WriteAuditInTx(ctx, tx, AuditLog{
			ShopID:       "shop-1",
			Action:       "appointment.cancel",
			ResourceType: "appointment",
			ResourceID:   "appt-1",
			Outcome:      AuditOutcomeSuccess,
		}, map[string]any{"source": "admin"})
	})
	if err != nil {
		t.Fatalf("WriteAuditInTx: %v", err)
	}

	var got AuditLog
	if err := DB.First(&got).Error; err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if got.TraceID != "trace-test-1" || got.ActorType != AuditActorAdmin || got.ActorID != "admin-1" {
		t.Fatalf("审计上下文未正确保存: %+v", got)
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(got.Details), &details); err != nil || details["source"] != "admin" {
		t.Fatalf("审计详情无效: details=%q err=%v", got.Details, err)
	}
}

func TestAppointmentCreateAndCancelAuditShareTrace(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-audit-flow", "")
	barber := MakeBarber(t, "barber-audit-flow", shop.ID, "审计师傅")
	ctx := WithTraceID(context.Background(), "trace-flow-1")
	ctx = WithAuditActor(ctx, AuditActorAgent, "")

	appt, err := CreateAppointmentFullContext(ctx, shop.ID, barber.Name, "审计顾客", "13800000000", "", "", "2099-01-02", "10:00", "剪发")
	if err != nil {
		t.Fatalf("create appointment: %v", err)
	}
	if _, err := CancelAppointmentWithPolicy(ctx, appt.ID, CancelSourceAgent, "不记录原文"); err != nil {
		t.Fatalf("cancel appointment: %v", err)
	}

	var logs []AuditLog
	if err := DB.Where("resource_id = ?", appt.ID).Order("id ASC").Find(&logs).Error; err != nil {
		t.Fatalf("query audit logs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("audit count = %d, want 2", len(logs))
	}
	if logs[0].Action != "appointment.create" || logs[1].Action != "appointment.cancel" {
		t.Fatalf("unexpected actions: %+v", logs)
	}
	for _, log := range logs {
		if log.TraceID != "trace-flow-1" {
			t.Fatalf("trace_id = %q, want trace-flow-1", log.TraceID)
		}
		if strings.Contains(log.Details, "13800000000") || strings.Contains(log.Details, "审计顾客") || strings.Contains(log.Details, "不记录原文") {
			t.Fatalf("审计详情泄露敏感原文: %s", log.Details)
		}
	}
	if err := VerifyAuditChain(ctx, shop.ID); err != nil {
		t.Fatalf("VerifyAuditChain: %v", err)
	}
	var outboxCount int64
	if err := DB.Model(&AuditOutbox{}).Where("audit_log_id IN ?", []uint64{logs[0].ID, logs[1].ID}).Count(&outboxCount).Error; err != nil || outboxCount != 2 {
		t.Fatalf("outbox count=%d err=%v, want 2", outboxCount, err)
	}
	if err := DB.Model(&AuditLog{}).Where("id = ?", logs[0].ID).Update("details", `{"tampered":true}`).Error; err != nil {
		t.Fatalf("tamper fixture: %v", err)
	}
	if err := VerifyAuditChain(ctx, shop.ID); err == nil {
		t.Fatal("篡改审计记录后校验应失败")
	}
}

func TestListAuditLogsEnforcesShopIsolation(t *testing.T) {
	SetupTestDB(t)
	for _, shopID := range []string{"shop-a", "shop-b"} {
		if err := WriteAudit(context.Background(), AuditLog{
			ShopID: shopID, Action: "test.action", ResourceType: "test", ResourceID: shopID,
			Outcome: AuditOutcomeSuccess,
		}, nil); err != nil {
			t.Fatalf("WriteAudit: %v", err)
		}
	}
	rows, err := ListAuditLogs(context.Background(), AuditQuery{ShopID: "shop-a", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ShopID != "shop-a" {
		t.Fatalf("shop isolation rows=%+v err=%v", rows, err)
	}
}

func TestTraceSpanAndRetention(t *testing.T) {
	SetupTestDB(t)
	ctx := WithAuditShop(WithTraceID(context.Background(), "trace-span-1"), "shop-span")
	old := time.Now().AddDate(0, 0, -30)
	RecordTraceSpan(ctx, TraceSpan{Name: "tool.test", Kind: "internal", Status: "ok", StartedAt: old}, map[string]any{"arguments_bytes": 12})
	var span TraceSpan
	if err := DB.Where("trace_id = ?", "trace-span-1").First(&span).Error; err != nil {
		t.Fatalf("query span: %v", err)
	}
	if span.ShopID != "shop-span" || strings.Contains(span.Attributes, "secret") {
		t.Fatalf("unexpected span: %+v", span)
	}
	deleted, err := DeleteTraceSpansBefore(context.Background(), time.Now().AddDate(0, 0, -14))
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v, want 1", deleted, err)
	}
}
