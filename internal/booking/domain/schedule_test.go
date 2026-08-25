package domain

import (
	"errors"
	"testing"
	"time"
)

func TestFindConflictsChecksStaffAndResources(t *testing.T) {
	base := validBooking(t, "booking-1", "staff-1", "idem-1")
	candidate := validBooking(t, "booking-2", "staff-2", "idem-2")
	occupied, err := NewOccupancy(base, []string{"chair-1"})
	if err != nil {
		t.Fatal(err)
	}
	candidateOccupancy, err := NewOccupancy(candidate, []string{"chair-1"})
	if err != nil {
		t.Fatal(err)
	}
	conflicts, err := FindConflicts(candidateOccupancy, []Occupancy{occupied})
	if err != nil {
		t.Fatalf("FindConflicts failed: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0].Kind != ConflictResource || conflicts[0].ResourceID != "chair-1" {
		t.Fatalf("unexpected resource conflicts: %+v", conflicts)
	}

	candidate.StaffID = "staff-1"
	candidateOccupancy, err = NewOccupancy(candidate, []string{"chair-2"})
	if err != nil {
		t.Fatal(err)
	}
	conflicts, err = FindConflicts(candidateOccupancy, []Occupancy{occupied})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0].Kind != ConflictStaff {
		t.Fatalf("unexpected staff conflicts: %+v", conflicts)
	}
}

func TestFindConflictsTreatsAdjacentAndCancelledAsAvailable(t *testing.T) {
	base := validBooking(t, "booking-1", "staff-1", "idem-1")
	base.EndAt = base.StartAt.Add(30 * time.Minute)
	candidate := base
	candidate.ID = "booking-2"
	candidate.IdempotencyKey = "idem-2"
	candidate.StartAt = base.EndAt
	candidate.EndAt = candidate.StartAt.Add(60 * time.Minute)
	occupied, err := NewOccupancy(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidateOccupancy, err := NewOccupancy(candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	conflicts, err := FindConflicts(candidateOccupancy, []Occupancy{occupied})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("adjacent intervals should not conflict: conflicts=%+v err=%v", conflicts, err)
	}

	base.Status = BookingCancelled
	occupied, err = NewOccupancy(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	conflicts, err = FindConflicts(candidateOccupancy, []Occupancy{occupied})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("cancelled booking should not block availability: conflicts=%+v err=%v", conflicts, err)
	}
}

func TestFindConflictsRejectsForeignTenant(t *testing.T) {
	base := validBooking(t, "booking-1", "staff-1", "idem-1")
	foreign := base
	foreign.ID = "booking-2"
	foreign.IdempotencyKey = "idem-2"
	foreign.LocationID = "location-other"
	occupied, err := NewOccupancy(foreign, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := NewOccupancy(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FindConflicts(candidate, []Occupancy{occupied}); !errors.Is(err, ErrTenantMismatch) {
		t.Fatalf("foreign tenant should be rejected, got %v", err)
	}
}

func TestValidateBookingWindowHonorsGranularityAndBounds(t *testing.T) {
	zone := time.FixedZone("CST", 8*60*60)
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, zone)
	policy := BookingPolicy{
		Location:        zone,
		SlotGranularity: 15 * time.Minute,
		MinAdvance:      1 * time.Hour,
		MaxAdvance:      7 * 24 * time.Hour,
	}
	if err := ValidateBookingWindow(now.Add(75*time.Minute), now, policy); err != nil {
		t.Fatalf("valid booking window rejected: %v", err)
	}
	if !errors.Is(ValidateBookingWindow(now.Add(45*time.Minute), now, policy), ErrTooSoon) {
		t.Fatal("booking before min advance should be rejected")
	}
	if !errors.Is(ValidateBookingWindow(now.Add(8*24*time.Hour), now, policy), ErrTooFar) {
		t.Fatal("booking after max advance should be rejected")
	}
	if !errors.Is(ValidateBookingWindow(now.Add(37*time.Minute), now, policy), ErrInvalidSlot) {
		t.Fatal("booking off the 15-minute grid should be rejected")
	}
}

func TestNewOccupancyRejectsDuplicateResources(t *testing.T) {
	booking := validBooking(t, "booking-1", "staff-1", "idem-1")
	if _, err := NewOccupancy(booking, []string{"chair-1", "chair-1"}); !errors.Is(err, ErrInvalidEntity) {
		t.Fatalf("duplicate resources should be rejected, got %v", err)
	}
}

func validBooking(t *testing.T, id, staffID, idempotencyKey string) Booking {
	t.Helper()
	service := validService()
	service.AllowedStaffIDs = nil
	booking, err := NewBooking(
		id,
		"merchant-1",
		"location-1",
		"customer-1",
		idempotencyKey,
		service,
		Staff{ID: staffID, MerchantID: "merchant-1", LocationID: "location-1", Name: staffID, Active: true},
		time.Date(2026, 8, 29, 15, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return booking
}
