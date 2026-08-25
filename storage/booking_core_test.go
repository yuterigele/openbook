package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yuterigele/openbook/internal/booking/domain"
	"gorm.io/gorm"
)

func TestCreateBookingWithAllocationsUsesOneTransactionAndIdempotency(t *testing.T) {
	SetupTestDB(t)
	booking, allocations := validPersistedBooking(t, "booking-1", "idem-1", "staff-1", "chair-1")
	record, err := CreateBookingWithAllocations(context.Background(), booking, allocations)
	if err != nil {
		t.Fatalf("create booking failed: %v", err)
	}
	if record.ID != booking.ID || record.Status != string(domain.BookingPending) {
		t.Fatalf("unexpected booking record: %+v", record)
	}
	var allocationCount int64
	if err := DB.Model(&BookingAllocationRecord{}).Where("booking_id = ?", booking.ID).Count(&allocationCount).Error; err != nil {
		t.Fatal(err)
	}
	if allocationCount != 2 {
		t.Fatalf("allocation count = %d, want 2", allocationCount)
	}
	var outboxCount int64
	if err := DB.Model(&BookingOutboxRecord{}).Where("booking_id = ?", booking.ID).Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Fatalf("outbox count = %d, want 1", outboxCount)
	}

	replay := booking
	replay.ID = "booking-retry"
	replayed, err := CreateBookingWithAllocations(context.Background(), replay, allocationsForBooking(allocations, replay.ID))
	if err != nil || replayed.ID != booking.ID {
		t.Fatalf("idempotent replay should return original booking: record=%+v err=%v", replayed, err)
	}
	if err := DB.Model(&BookingRecord{}).Where("customer_id = ?", booking.CustomerID).Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Fatalf("idempotent replay created another booking: count=%d", outboxCount)
	}
}

func TestCreateBookingWithAllocationsRejectsStaffAndResourceConflicts(t *testing.T) {
	SetupTestDB(t)
	first, firstAllocations := validPersistedBooking(t, "booking-1", "idem-1", "staff-1", "chair-1")
	if _, err := CreateBookingWithAllocations(context.Background(), first, firstAllocations); err != nil {
		t.Fatal(err)
	}
	staffConflict, allocations := validPersistedBooking(t, "booking-2", "idem-2", "staff-1", "chair-2")
	if _, err := CreateBookingWithAllocations(context.Background(), staffConflict, allocations); !errors.Is(err, ErrBookingConflict) {
		t.Fatalf("staff conflict should be rejected, got %v", err)
	}
	resourceConflict, allocations := validPersistedBooking(t, "booking-3", "idem-3", "staff-2", "chair-1")
	if _, err := CreateBookingWithAllocations(context.Background(), resourceConflict, allocations); !errors.Is(err, ErrBookingConflict) {
		t.Fatalf("resource conflict should be rejected, got %v", err)
	}
}

func TestCreateBookingWithAllocationsRejectsCrossCustomerRead(t *testing.T) {
	SetupTestDB(t)
	booking, allocations := validPersistedBooking(t, "booking-1", "idem-1", "staff-1", "chair-1")
	if _, err := CreateBookingWithAllocations(context.Background(), booking, allocations); err != nil {
		t.Fatal(err)
	}
	if _, err := GetBookingForCustomer(context.Background(), booking.MerchantID, booking.LocationID, "customer-other", booking.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-customer booking read should be rejected, got %v", err)
	}
}

func TestCreateBookingWithAllocationsRollsBackWhenAllocationIsInvalid(t *testing.T) {
	SetupTestDB(t)
	booking, allocations := validPersistedBooking(t, "booking-1", "idem-1", "staff-1", "chair-1")
	allocations[1].ID = ""
	if _, err := CreateBookingWithAllocations(context.Background(), booking, allocations); !errors.Is(err, domain.ErrInvalidEntity) {
		t.Fatalf("invalid allocation should be rejected, got %v", err)
	}
	var count int64
	if err := DB.Model(&BookingRecord{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid allocation wrote booking rows: %d", count)
	}
}

func validPersistedBooking(t *testing.T, id, idempotencyKey, staffID, resourceID string) (domain.Booking, []domain.Allocation) {
	t.Helper()
	service := domain.Service{
		ID: "service-1", MerchantID: "merchant-1", LocationID: "location-1", Name: "剪发",
		Duration: 60 * time.Minute, BufferBefore: 15 * time.Minute, BufferAfter: 15 * time.Minute,
		Active: true,
	}
	staff := domain.Staff{ID: staffID, MerchantID: "merchant-1", LocationID: "location-1", Name: staffID, Active: true}
	booking, err := domain.NewBooking(id, "merchant-1", "location-1", "customer-1", idempotencyKey, service, staff, time.Date(2026, 8, 29, 15, 0, 0, 0, time.FixedZone("CST", 8*60*60)))
	if err != nil {
		t.Fatal(err)
	}
	allocations := []domain.Allocation{
		{ID: id + "-staff", BookingID: id, MerchantID: "merchant-1", LocationID: "location-1", StaffID: staffID},
		{ID: id + "-resource", BookingID: id, MerchantID: "merchant-1", LocationID: "location-1", ResourceID: resourceID},
	}
	return booking, allocations
}

func allocationsForBooking(allocations []domain.Allocation, bookingID string) []domain.Allocation {
	result := make([]domain.Allocation, len(allocations))
	copy(result, allocations)
	for index := range result {
		result[index].BookingID = bookingID
		result[index].ID = bookingID + map[bool]string{true: "-staff", false: "-resource"}[result[index].StaffID != ""]
	}
	return result
}
