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

func TestListOperationOutcomesForReconciliationIsReadOnlyAndBounded(t *testing.T) {
	SetupTestDB(t)
	zone := time.FixedZone("CST", 8*60*60)
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, zone)
	create := func(id, key string, checkedAt time.Time) {
		t.Helper()
		outcome, err := domain.NewPendingOutcome(id, "merchant-1", "location-1", "customer-1", "create_booking", key, checkedAt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CreatePendingOperationOutcome(context.Background(), outcome); err != nil {
			t.Fatal(err)
		}
	}
	create("op-old", "idem-old", now.Add(-20*time.Minute))
	create("op-fresh", "idem-fresh", now.Add(-time.Minute))
	create("op-unknown", "idem-unknown", now)
	if _, err := TransitionOperationOutcome(context.Background(), "op-unknown", domain.OutcomePending, domain.OutcomeUnknown, "", now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	records, err := ListOperationOutcomesForReconciliation(context.Background(), now, 5*time.Minute, 10)
	if err != nil {
		t.Fatalf("list reconciliation candidates failed: %v", err)
	}
	if len(records) != 2 || records[0].ID != "op-old" || records[1].ID != "op-unknown" {
		t.Fatalf("unexpected reconciliation candidates: %+v", records)
	}
	limited, err := ListOperationOutcomesForReconciliation(context.Background(), now, 5*time.Minute, 1)
	if err != nil || len(limited) != 1 || limited[0].ID != "op-old" {
		t.Fatalf("reconciliation limit/order failed: records=%+v err=%v", limited, err)
	}
	fresh, err := GetOperationOutcomeByScopeAndKey(context.Background(), "merchant-1", "location-1", "customer-1", "create_booking", "idem-fresh")
	if err != nil || fresh.Status != string(domain.OutcomePending) {
		t.Fatalf("read-only candidate query changed fresh pending record: record=%+v err=%v", fresh, err)
	}
}

func TestReconcileOperationOutcomeOnlyReadsFinalStateAndEscalatesUnknown(t *testing.T) {
	SetupTestDB(t)
	zone := time.FixedZone("CST", 8*60*60)
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, zone)
	outcome, err := domain.NewPendingOutcome("op-1", "merchant-1", "location-1", "customer-1", "create_booking", "idem-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePendingOperationOutcome(context.Background(), outcome); err != nil {
		t.Fatal(err)
	}
	readerCalls := 0
	reader := func(_ context.Context, record OperationOutcomeRecord) (OutcomeFinalObservation, error) {
		readerCalls++
		if record.Status == string(domain.OutcomePending) {
			return OutcomeFinalObservation{Status: domain.OutcomeUnknown}, nil
		}
		return OutcomeFinalObservation{Status: domain.OutcomeUnknown}, nil
	}
	policy := OutcomeReconcilePolicy{MaxAttempts: 2, MaxAge: time.Hour}
	updated, err := ReconcileOperationOutcome(context.Background(), "op-1", policy, reader, now.Add(time.Minute))
	if err != nil || updated.Status != string(domain.OutcomeUnknown) {
		t.Fatalf("pending -> unknown reconciliation failed: record=%+v err=%v", updated, err)
	}
	updated, err = ReconcileOperationOutcome(context.Background(), "op-1", policy, reader, now.Add(2*time.Minute))
	if err != nil || updated.Status != string(domain.OutcomeNeedsHuman) {
		t.Fatalf("unknown should escalate after max attempts: record=%+v err=%v", updated, err)
	}
	if readerCalls != 2 {
		t.Fatalf("unexpected reader calls: %d", readerCalls)
	}
	updated, err = ReconcileOperationOutcome(context.Background(), "op-1", policy, func(context.Context, OperationOutcomeRecord) (OutcomeFinalObservation, error) {
		t.Fatal("terminal outcome must not be queried again")
		return OutcomeFinalObservation{}, nil
	}, now.Add(3*time.Minute))
	if err != nil || updated.Status != string(domain.OutcomeNeedsHuman) {
		t.Fatalf("terminal outcome should be returned unchanged: record=%+v err=%v", updated, err)
	}
}

func TestReconcileOperationOutcomeRejectsConfirmedObservationWithoutBooking(t *testing.T) {
	SetupTestDB(t)
	now := time.Now()
	outcome, err := domain.NewPendingOutcome("op-1", "merchant-1", "location-1", "customer-1", "create_booking", "idem-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePendingOperationOutcome(context.Background(), outcome); err != nil {
		t.Fatal(err)
	}
	_, err = ReconcileOperationOutcome(context.Background(), "op-1", DefaultOutcomeReconcilePolicy(), func(context.Context, OperationOutcomeRecord) (OutcomeFinalObservation, error) {
		return OutcomeFinalObservation{Status: domain.OutcomeConfirmed}, nil
	}, now.Add(time.Minute))
	if !errors.Is(err, domain.ErrInvalidOutcome) {
		t.Fatalf("confirmation without booking should be rejected, got %v", err)
	}
}
