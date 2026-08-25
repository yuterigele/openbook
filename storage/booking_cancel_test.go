package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/yuterigele/openbook/internal/booking/domain"
	"gorm.io/gorm"
)

func TestCancelBookingForCustomerUsesTransactionAndIdempotency(t *testing.T) {
	SetupTestDB(t)
	booking, allocations := validPersistedBooking(t, "booking-1", "create-idem-1", "staff-1", "chair-1")
	if _, err := CreateBookingWithAllocations(context.Background(), booking, allocations); err != nil {
		t.Fatal(err)
	}
	cancelled, err := CancelBookingForCustomer(context.Background(), "merchant-1", "location-1", "customer-1", booking.ID, "cancel-idem-1")
	if err != nil {
		t.Fatalf("cancel booking failed: %v", err)
	}
	if cancelled.Status != string(domain.BookingCancelled) {
		t.Fatalf("status = %q, want cancelled", cancelled.Status)
	}
	outcome, err := GetOperationOutcomeByScopeAndKey(context.Background(), "merchant-1", "location-1", "customer-1", "cancel_booking", "cancel-idem-1")
	if err != nil || outcome.Status != string(domain.OutcomeConfirmed) || outcome.BookingID != booking.ID {
		t.Fatalf("cancel outcome not confirmed: outcome=%+v err=%v", outcome, err)
	}
	var outboxCount int64
	if err := DB.Model(&BookingOutboxRecord{}).Where("booking_id = ?", booking.ID).Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if outboxCount != 2 {
		t.Fatalf("outbox count = %d, want create + cancel", outboxCount)
	}
	replayed, err := CancelBookingForCustomer(context.Background(), "merchant-1", "location-1", "customer-1", booking.ID, "cancel-idem-1")
	if err != nil || replayed.ID != booking.ID || replayed.Status != string(domain.BookingCancelled) {
		t.Fatalf("idempotent cancel replay failed: record=%+v err=%v", replayed, err)
	}
	if err := DB.Model(&BookingOutboxRecord{}).Where("booking_id = ?", booking.ID).Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if outboxCount != 2 {
		t.Fatalf("idempotent cancel wrote another outbox: %d", outboxCount)
	}
}

func TestCancelBookingForCustomerEnforcesOwnershipAndState(t *testing.T) {
	SetupTestDB(t)
	booking, allocations := validPersistedBooking(t, "booking-1", "create-idem-1", "staff-1", "chair-1")
	if _, err := CreateBookingWithAllocations(context.Background(), booking, allocations); err != nil {
		t.Fatal(err)
	}
	if _, err := CancelBookingForCustomer(context.Background(), "merchant-1", "location-1", "customer-other", booking.ID, "cancel-idem-other"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("foreign customer cancellation should be rejected, got %v", err)
	}
	if _, err := CancelBookingForCustomer(context.Background(), "merchant-1", "location-1", "customer-1", booking.ID, "cancel-idem-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := CancelBookingForCustomer(context.Background(), "merchant-1", "location-1", "customer-1", booking.ID, "cancel-idem-2"); !errors.Is(err, ErrBookingNotCancellable) {
		t.Fatalf("already cancelled booking should be rejected, got %v", err)
	}
}
