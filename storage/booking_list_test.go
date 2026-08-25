package storage

import (
	"context"
	"testing"
	"time"

	"github.com/yuterigele/openbook/internal/booking/domain"
)

func TestListBookingsForCustomerScopesSortsAndFilters(t *testing.T) {
	SetupTestDB(t)
	now := time.Now().Truncate(time.Second)
	first, firstAllocations := validPersistedBooking(t, "booking-1", "idem-1", "staff-1", "chair-1")
	first.StartAt = now.Add(2 * time.Hour)
	first.EndAt = first.StartAt.Add(time.Hour)
	if _, err := CreateBookingWithAllocations(context.Background(), first, firstAllocations); err != nil {
		t.Fatal(err)
	}
	second, secondAllocations := validPersistedBooking(t, "booking-2", "idem-2", "staff-2", "chair-2")
	second.StartAt = now.Add(4 * time.Hour)
	second.EndAt = second.StartAt.Add(time.Hour)
	if _, err := CreateBookingWithAllocations(context.Background(), second, secondAllocations); err != nil {
		t.Fatal(err)
	}
	if err := DB.Model(&BookingRecord{}).Where("id = ?", second.ID).Update("status", string(domain.BookingConfirmed)).Error; err != nil {
		t.Fatal(err)
	}
	old, oldAllocations := validPersistedBooking(t, "booking-old", "idem-old", "staff-3", "chair-3")
	old.StartAt = now.Add(-3 * time.Hour)
	old.EndAt = now.Add(-2 * time.Hour)
	if _, err := CreateBookingWithAllocations(context.Background(), old, oldAllocations); err != nil {
		t.Fatal(err)
	}
	cancelled, cancelledAllocations := validPersistedBooking(t, "booking-cancelled", "idem-cancelled", "staff-4", "chair-4")
	cancelled.StartAt = now.Add(6 * time.Hour)
	cancelled.EndAt = cancelled.StartAt.Add(time.Hour)
	if _, err := CreateBookingWithAllocations(context.Background(), cancelled, cancelledAllocations); err != nil {
		t.Fatal(err)
	}
	if err := DB.Model(&BookingRecord{}).Where("id = ?", cancelled.ID).Update("status", string(domain.BookingCancelled)).Error; err != nil {
		t.Fatal(err)
	}
	foreign, foreignAllocations := validPersistedBooking(t, "booking-foreign", "idem-foreign", "staff-5", "chair-5")
	foreign.CustomerID = "customer-other"
	foreign.StartAt = now.Add(time.Hour)
	foreign.EndAt = foreign.StartAt.Add(time.Hour)
	for index := range foreignAllocations {
		foreignAllocations[index].BookingID = foreign.ID
	}
	if _, err := CreateBookingWithAllocations(context.Background(), foreign, foreignAllocations); err != nil {
		t.Fatal(err)
	}

	records, err := ListBookingsForCustomer(context.Background(), "merchant-1", "location-1", "customer-1", now, 10)
	if err != nil {
		t.Fatalf("list bookings failed: %v", err)
	}
	if len(records) != 2 || records[0].ID != first.ID || records[1].ID != second.ID {
		t.Fatalf("unexpected customer bookings: %+v", records)
	}
	limited, err := ListBookingsForCustomer(context.Background(), "merchant-1", "location-1", "customer-1", now, 1)
	if err != nil || len(limited) != 1 || limited[0].ID != first.ID {
		t.Fatalf("booking list limit failed: records=%+v err=%v", limited, err)
	}
}
