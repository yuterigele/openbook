package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yuterigele/openbook/internal/booking/domain"
	"gorm.io/gorm"
)

func TestOperationOutcomePersistsScopedIdempotencyAndTransitions(t *testing.T) {
	SetupTestDB(t)
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	outcome, err := domain.NewPendingOutcome("op-1", "merchant-1", "location-1", "customer-1", "create_booking", "idem-1", now)
	if err != nil {
		t.Fatal(err)
	}
	record, err := CreatePendingOperationOutcome(context.Background(), outcome)
	if err != nil {
		t.Fatalf("create outcome failed: %v", err)
	}
	if record.Status != string(domain.OutcomePending) || record.AttemptCount != 0 {
		t.Fatalf("unexpected pending record: %+v", record)
	}

	got, err := GetOperationOutcomeByScopeAndKey(context.Background(), "merchant-1", "location-1", "customer-1", "create_booking", "idem-1")
	if err != nil || got.ID != "op-1" {
		t.Fatalf("scoped outcome lookup failed: record=%+v err=%v", got, err)
	}
	if _, err := GetOperationOutcomeByScopeAndKey(context.Background(), "merchant-1", "location-other", "customer-1", "create_booking", "idem-1"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-location lookup should not find outcome, got %v", err)
	}
	if _, err := CreatePendingOperationOutcome(context.Background(), outcome); !errors.Is(err, ErrOperationOutcomeAlreadyExists) {
		t.Fatalf("duplicate idempotency key should be rejected, got %v", err)
	}

	updated, err := TransitionOperationOutcome(context.Background(), "op-1", domain.OutcomePending, domain.OutcomeUnknown, "", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("pending -> unknown failed: %v", err)
	}
	if updated.Status != string(domain.OutcomeUnknown) || updated.AttemptCount != 1 {
		t.Fatalf("unexpected unknown record: %+v", updated)
	}
	if _, err := TransitionOperationOutcome(context.Background(), "op-1", domain.OutcomePending, domain.OutcomeConfirmed, "booking-1", now.Add(2*time.Minute)); !errors.Is(err, ErrOperationOutcomeStateChanged) {
		t.Fatalf("stale expected status should be rejected, got %v", err)
	}
	updated, err = TransitionOperationOutcome(context.Background(), "op-1", domain.OutcomeUnknown, domain.OutcomeConfirmed, "booking-1", now.Add(3*time.Minute))
	if err != nil || updated.Status != string(domain.OutcomeConfirmed) || updated.BookingID != "booking-1" {
		t.Fatalf("unknown -> confirmed failed: record=%+v err=%v", updated, err)
	}
}

func TestOperationOutcomeRejectsInvalidInitialState(t *testing.T) {
	SetupTestDB(t)
	outcome := domain.OperationOutcome{
		ID: "op-1", MerchantID: "merchant-1", LocationID: "location-1", CustomerID: "customer-1",
		Operation: "create_booking", IdempotencyKey: "idem-1", Status: domain.OutcomeConfirmed,
	}
	if _, err := CreatePendingOperationOutcome(context.Background(), outcome); !errors.Is(err, domain.ErrInvalidOutcome) {
		t.Fatalf("non-pending initial outcome should be rejected, got %v", err)
	}
}
