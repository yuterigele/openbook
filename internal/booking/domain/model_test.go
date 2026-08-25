package domain

import (
	"errors"
	"testing"
	"time"
)

func TestServiceIntervalAtIncludesBuffers(t *testing.T) {
	service := validService()
	start := time.Date(2026, 8, 29, 15, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	interval, err := service.IntervalAt(start)
	if err != nil {
		t.Fatalf("IntervalAt failed: %v", err)
	}
	if !interval.StartAt.Equal(start.Add(-15*time.Minute)) || !interval.EndAt.Equal(start.Add(75*time.Minute)) {
		t.Fatalf("unexpected service interval: %+v", interval)
	}
}

func TestNewBookingCalculatesIntervalAndRequiresIdempotency(t *testing.T) {
	service := validService()
	staff := validStaff()
	start := time.Date(2026, 8, 29, 15, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	booking, err := NewBooking("booking-1", "merchant-1", "location-1", "customer-1", "idem-1", service, staff, start)
	if err != nil {
		t.Fatalf("NewBooking failed: %v", err)
	}
	if booking.Status != BookingPending || booking.ServiceID != service.ID || booking.StaffID != staff.ID {
		t.Fatalf("unexpected booking: %+v", booking)
	}
	if booking.StartAt != start.Add(-15*time.Minute) || booking.EndAt != start.Add(75*time.Minute) {
		t.Fatalf("booking did not retain buffered interval: %+v", booking)
	}
	if _, err := NewBooking("booking-2", "merchant-1", "location-1", "customer-1", "", service, staff, start); !errors.Is(err, ErrInvalidEntity) {
		t.Fatalf("missing idempotency key should be rejected, got %v", err)
	}
}

func TestServiceValidateResourcesMatchesRequirements(t *testing.T) {
	service := validService()
	resources := []Resource{{
		ID: "chair-1", MerchantID: "merchant-1", LocationID: "location-1", Kind: "chair", Name: "一号工位", Active: true,
	}}
	if err := service.ValidateResources(resources); err != nil {
		t.Fatalf("matching resource rejected: %v", err)
	}
	resources[0].Kind = "bed"
	if err := service.ValidateResources(resources); !errors.Is(err, ErrInvalidEntity) {
		t.Fatalf("wrong resource kind should be rejected, got %v", err)
	}
	resources[0].Kind = "chair"
	resources[0].LocationID = "location-other"
	if !errors.Is(service.ValidateResources(resources), ErrTenantMismatch) {
		t.Fatal("cross-location resource should be rejected")
	}
}

func TestServiceValidateResourcesRejectsInactiveAndExtraResources(t *testing.T) {
	service := validService()
	resource := Resource{
		ID: "chair-1", MerchantID: "merchant-1", LocationID: "location-1", Kind: "chair", Name: "一号工位", Active: false,
	}
	if !errors.Is(service.ValidateResources([]Resource{resource}), ErrInvalidEntity) {
		t.Fatal("inactive resource should be rejected")
	}
	resource.Active = true
	if !errors.Is(service.ValidateResources([]Resource{resource, resource}), ErrInvalidEntity) {
		t.Fatal("extra resource should be rejected")
	}
}

func TestNewBookingRejectsCrossTenantServiceOrStaff(t *testing.T) {
	service := validService()
	service.LocationID = "location-other"
	if _, err := NewBooking("booking-1", "merchant-1", "location-1", "customer-1", "idem-1", service, validStaff(), time.Now()); !errors.Is(err, ErrTenantMismatch) {
		t.Fatalf("cross-location service should be rejected, got %v", err)
	}

	staff := validStaff()
	staff.MerchantID = "merchant-other"
	if _, err := NewBooking("booking-1", "merchant-1", "location-1", "customer-1", "idem-1", validService(), staff, time.Now()); !errors.Is(err, ErrTenantMismatch) {
		t.Fatalf("cross-merchant staff should be rejected, got %v", err)
	}
}

func TestBookingValidateScopeRejectsForeignAllocation(t *testing.T) {
	service := validService()
	staff := validStaff()
	booking, err := NewBooking("booking-1", "merchant-1", "location-1", "customer-1", "idem-1", service, staff, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	allocation := Allocation{
		ID: "allocation-1", BookingID: booking.ID, MerchantID: "merchant-1", LocationID: "location-other", StaffID: staff.ID,
	}
	if err := booking.ValidateScope(service, staff, []Allocation{allocation}); !errors.Is(err, ErrTenantMismatch) {
		t.Fatalf("foreign allocation should be rejected, got %v", err)
	}
}

func TestBookingPolicyValidate(t *testing.T) {
	valid := BookingPolicy{
		Location:        time.FixedZone("CST", 8*60*60),
		SlotGranularity: 15 * time.Minute,
		MinAdvance:      30 * time.Minute,
		MaxAdvance:      30 * 24 * time.Hour,
		CancelBefore:    2 * time.Hour,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	valid.SlotGranularity = 17 * time.Minute
	if !errors.Is(valid.Validate(), ErrInvalidEntity) {
		t.Fatal("granularity that does not divide a day should be rejected")
	}
}

func validService() Service {
	return Service{
		ID:                   "service-1",
		MerchantID:           "merchant-1",
		LocationID:           "location-1",
		Name:                 "剪发",
		Duration:             60 * time.Minute,
		BufferBefore:         15 * time.Minute,
		BufferAfter:          15 * time.Minute,
		AllowedStaffIDs:      []string{"staff-1"},
		ResourceRequirements: []ResourceRequirement{{Kind: "chair", Quantity: 1}},
		Active:               true,
	}
}

func validStaff() Staff {
	return Staff{ID: "staff-1", MerchantID: "merchant-1", LocationID: "location-1", Name: "Tony", Active: true}
}
