package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/yuterigele/openbook/internal/booking/domain"
)

func TestCreateBookingWithAllocationsConfirmsOutcome(t *testing.T) {
	SetupTestDB(t)
	booking, allocations := validPersistedBooking(t, "booking-outcome-1", "idem-outcome-1", "staff-1", "chair-1")
	if _, err := CreateBookingWithAllocations(context.Background(), booking, allocations); err != nil {
		t.Fatal(err)
	}

	record, err := GetOperationOutcomeByScopeAndKey(context.Background(), booking.MerchantID, booking.LocationID, booking.CustomerID, "create_booking", booking.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != string(domain.OutcomeConfirmed) || record.BookingID != booking.ID {
		t.Fatalf("unexpected create outcome: %+v", record)
	}
}

func TestCreateBookingWithAllocationsRejectsAndReplaysConflictOutcome(t *testing.T) {
	SetupTestDB(t)
	first, firstAllocations := validPersistedBooking(t, "booking-outcome-first", "idem-outcome-first", "staff-1", "chair-1")
	if _, err := CreateBookingWithAllocations(context.Background(), first, firstAllocations); err != nil {
		t.Fatal(err)
	}
	conflict, conflictAllocations := validPersistedBooking(t, "booking-outcome-conflict", "idem-outcome-conflict", "staff-1", "chair-2")
	if _, err := CreateBookingWithAllocations(context.Background(), conflict, conflictAllocations); !errors.Is(err, ErrBookingConflict) {
		t.Fatalf("expected booking conflict, got %v", err)
	}
	record, err := GetOperationOutcomeByScopeAndKey(context.Background(), conflict.MerchantID, conflict.LocationID, conflict.CustomerID, "create_booking", conflict.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != string(domain.OutcomeRejected) {
		t.Fatalf("unexpected rejected outcome: %+v", record)
	}
	if _, err := CreateBookingWithAllocations(context.Background(), conflict, conflictAllocations); !errors.Is(err, ErrBookingConflict) {
		t.Fatalf("replayed conflict should remain rejected, got %v", err)
	}
}
