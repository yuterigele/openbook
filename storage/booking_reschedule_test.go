package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yuterigele/openbook/internal/booking/domain"
)

func TestRescheduleBookingForCustomerIsAtomicAndIdempotent(t *testing.T) {
	SetupTestDB(t)
	old, oldAllocations := validPersistedBooking(t, "booking-old", "create-old", "staff-1", "chair-1")
	old.StartAt = time.Now().Add(2 * time.Hour)
	old.EndAt = old.StartAt.Add(time.Hour)
	if _, err := CreateBookingWithAllocations(context.Background(), old, oldAllocations); err != nil {
		t.Fatal(err)
	}
	replacement, replacementAllocations := validPersistedBooking(t, "booking-new", "create-new", "staff-2", "chair-2")
	replacement.StartAt = old.StartAt.Add(4 * time.Hour)
	replacement.EndAt = replacement.StartAt.Add(time.Hour)
	rescheduled, err := RescheduleBookingForCustomer(context.Background(), "merchant-1", "location-1", "customer-1", old.ID, "reschedule-1", replacement, replacementAllocations)
	if err != nil {
		t.Fatalf("reschedule failed: %v", err)
	}
	if rescheduled.ID != replacement.ID || rescheduled.Status != string(domain.BookingPending) {
		t.Fatalf("unexpected replacement: %+v", rescheduled)
	}
	oldAfter, err := GetBookingForCustomer(context.Background(), "merchant-1", "location-1", "customer-1", old.ID)
	if err != nil || oldAfter.Status != string(domain.BookingCancelled) {
		t.Fatalf("old booking was not cancelled atomically: record=%+v err=%v", oldAfter, err)
	}
	outcome, err := GetOperationOutcomeByScopeAndKey(context.Background(), "merchant-1", "location-1", "customer-1", "reschedule_booking", "reschedule-1")
	if err != nil || outcome.Status != string(domain.OutcomeConfirmed) || outcome.BookingID != replacement.ID {
		t.Fatalf("reschedule outcome not confirmed: outcome=%+v err=%v", outcome, err)
	}
	var outboxCount int64
	if err := DB.Model(&BookingOutboxRecord{}).Where("booking_id = ?", replacement.ID).Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Fatalf("reschedule should write one outbox event, got %d", outboxCount)
	}
	replayed, err := RescheduleBookingForCustomer(context.Background(), "merchant-1", "location-1", "customer-1", old.ID, "reschedule-1", replacement, replacementAllocations)
	if err != nil || replayed.ID != replacement.ID {
		t.Fatalf("idempotent reschedule replay failed: record=%+v err=%v", replayed, err)
	}
}

func TestRescheduleBookingForCustomerKeepsOldBookingOnConflict(t *testing.T) {
	SetupTestDB(t)
	old, oldAllocations := validPersistedBooking(t, "booking-old", "create-old", "staff-1", "chair-1")
	old.StartAt = time.Now().Add(2 * time.Hour)
	old.EndAt = old.StartAt.Add(time.Hour)
	if _, err := CreateBookingWithAllocations(context.Background(), old, oldAllocations); err != nil {
		t.Fatal(err)
	}
	blocker, blockerAllocations := validPersistedBooking(t, "booking-blocker", "create-blocker", "staff-2", "chair-2")
	blocker.StartAt = old.StartAt.Add(4 * time.Hour)
	blocker.EndAt = blocker.StartAt.Add(time.Hour)
	if _, err := CreateBookingWithAllocations(context.Background(), blocker, blockerAllocations); err != nil {
		t.Fatal(err)
	}
	replacement, replacementAllocations := validPersistedBooking(t, "booking-new", "create-new", "staff-2", "chair-3")
	replacement.StartAt = blocker.StartAt
	replacement.EndAt = blocker.EndAt
	if _, err := RescheduleBookingForCustomer(context.Background(), "merchant-1", "location-1", "customer-1", old.ID, "reschedule-conflict", replacement, replacementAllocations); !errors.Is(err, ErrBookingConflict) {
		t.Fatalf("replacement conflict should be returned, got %v", err)
	}
	oldAfter, err := GetBookingForCustomer(context.Background(), "merchant-1", "location-1", "customer-1", old.ID)
	if err != nil || oldAfter.Status != string(domain.BookingPending) {
		t.Fatalf("old booking changed after conflict: record=%+v err=%v", oldAfter, err)
	}
	var replacementCount int64
	if err := DB.Model(&BookingRecord{}).Where("id = ?", replacement.ID).Count(&replacementCount).Error; err != nil {
		t.Fatal(err)
	}
	if replacementCount != 0 {
		t.Fatal("conflicted replacement should not be inserted")
	}
}
