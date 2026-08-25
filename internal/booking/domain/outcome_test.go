package domain

import (
	"errors"
	"testing"
	"time"
)

func TestOutcomeTransitionFollowsUnknownRecoveryGraph(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	outcome, err := NewPendingOutcome("op-1", "merchant-1", "location-1", "customer-1", "create_booking", "idem-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := outcome.Transition(OutcomeUnknown, "", now.Add(5*time.Second)); err != nil {
		t.Fatalf("pending -> unknown failed: %v", err)
	}
	if err := outcome.Transition(OutcomeConfirmed, "booking-1", now.Add(10*time.Second)); err != nil {
		t.Fatalf("unknown -> confirmed failed: %v", err)
	}
	if outcome.Status != OutcomeConfirmed || outcome.BookingID != "booking-1" || outcome.AttemptCount != 2 {
		t.Fatalf("unexpected confirmed outcome: %+v", outcome)
	}
	if err := outcome.Transition(OutcomeUnknown, "", now.Add(15*time.Second)); !errors.Is(err, ErrInvalidOutcomeTransition) {
		t.Fatalf("confirmed outcome should not return to unknown, got %v", err)
	}
}

func TestOutcomeTransitionRequiresBookingOnConfirmation(t *testing.T) {
	now := time.Now()
	outcome, err := NewPendingOutcome("op-1", "merchant-1", "location-1", "customer-1", "create_booking", "idem-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := outcome.Transition(OutcomeConfirmed, "", now); !errors.Is(err, ErrInvalidOutcome) {
		t.Fatalf("confirmation without booking should be rejected, got %v", err)
	}
	if outcome.Status != OutcomePending || outcome.AttemptCount != 0 {
		t.Fatalf("failed transition should not mutate outcome: %+v", outcome)
	}
}

func TestOutcomeUnknownCanBecomeNeedsHumanButNotPending(t *testing.T) {
	outcome, err := NewPendingOutcome("op-1", "merchant-1", "location-1", "customer-1", "cancel_booking", "idem-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := outcome.Transition(OutcomeUnknown, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := outcome.Transition(OutcomePending, "", time.Now()); !errors.Is(err, ErrInvalidOutcomeTransition) {
		t.Fatalf("unknown outcome must not return to pending, got %v", err)
	}
	if err := outcome.Transition(OutcomeNeedsHuman, "", time.Now()); err != nil {
		t.Fatalf("unknown -> needs_human failed: %v", err)
	}
	if err := outcome.Transition(OutcomeRejected, "", time.Now()); !errors.Is(err, ErrInvalidOutcomeTransition) {
		t.Fatalf("needs_human should be terminal, got %v", err)
	}
}

func TestOutcomeValidateRejectsConfirmedWithoutBooking(t *testing.T) {
	outcome := OperationOutcome{
		ID: "op-1", MerchantID: "merchant-1", LocationID: "location-1", CustomerID: "customer-1",
		Operation: "create_booking", IdempotencyKey: "idem-1", Status: OutcomeConfirmed,
	}
	if !errors.Is(outcome.Validate(), ErrInvalidOutcome) {
		t.Fatal("confirmed outcome without booking id should be rejected")
	}
}
