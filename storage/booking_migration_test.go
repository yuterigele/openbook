package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/internal/booking/domain"
)

func TestPlanLegacyAppointmentMigrationMapsStableFields(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 8, 1, 10, 0, 0, 0, location)
	appointments := []Appointment{{
		ID: "appointment-1", ShopID: "shop-1", BarberID: "barber-1", BarberName: "Tony",
		CustomerID: "customer-1", Customer: "Alice", Date: "2026-08-30", Time: "14:00",
		Service: "剪发", Status: "active", CreatedAt: createdAt, UpdatedAt: createdAt,
	}}
	services := []Service{{ID: "service-1", ShopID: "shop-1", Name: "剪发", EstimatedMin: 60}}
	barbers := []Barber{{ID: "barber-1", ShopID: "shop-1", Name: "Tony", Active: true}}

	plan, err := PlanLegacyAppointmentMigration(appointments, services, barbers, LegacyBookingMigrationOptions{
		MerchantID: "merchant-1", Location: location,
	})
	if err != nil {
		t.Fatalf("plan migration: %v", err)
	}
	if plan.Version != LegacyBookingMigrationVersion || plan.Total != 1 || plan.Ready != 1 || plan.Blocked != 0 {
		t.Fatalf("unexpected plan summary: %+v", plan)
	}
	rows := plan.ReadyRows()
	if len(rows) != 1 {
		t.Fatalf("ready rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.LegacyAppointmentID != "appointment-1" || row.BookingID != "legacy-appointment-1" {
		t.Fatalf("unexpected ID mapping: %+v", row)
	}
	if row.IdempotencyKey != "legacy:appointment:appointment-1" || row.Booking.Status != string(domain.BookingConfirmed) {
		t.Fatalf("unexpected booking mapping: %+v", row.Booking)
	}
	if row.Booking.MerchantID != "merchant-1" || row.Booking.LocationID != "shop-1" || row.Booking.ServiceID != "service-1" || row.Booking.StaffID != "barber-1" {
		t.Fatalf("unexpected booking scope mapping: %+v", row.Booking)
	}
	wantStart := time.Date(2026, 8, 30, 14, 0, 0, 0, location)
	if !row.Booking.StartAt.Equal(wantStart) || !row.Booking.EndAt.Equal(wantStart.Add(time.Hour)) {
		t.Fatalf("unexpected interval: %s - %s", row.Booking.StartAt, row.Booking.EndAt)
	}
	if row.Allocation.StaffID != "barber-1" || row.Allocation.ResourceID != "" || row.Allocation.BookingID != row.Booking.ID {
		t.Fatalf("unexpected allocation: %+v", row.Allocation)
	}
}

func TestPlanLegacyAppointmentMigrationBlocksAmbiguityAndUnsupportedData(t *testing.T) {
	appointments := []Appointment{
		{ID: "missing-customer", ShopID: "shop-1", BarberID: "barber-1", CustomerID: "", Date: "2026-08-30", Time: "14:00", Service: "剪发", Status: "active"},
		{ID: "unsupported-status", ShopID: "shop-1", BarberID: "barber-1", CustomerID: "customer-2", Date: "2026-08-30", Time: "15:00", Service: "剪发", Status: "noshow"},
	}
	services := []Service{
		{ID: "service-1", ShopID: "shop-1", Name: "剪发", EstimatedMin: 60},
		{ID: "service-2", ShopID: "shop-1", Name: "剪发", EstimatedMin: 30},
	}
	barbers := []Barber{{ID: "barber-1", ShopID: "shop-1", Name: "Tony", Active: true}}

	plan, err := PlanLegacyAppointmentMigration(appointments, services, barbers, LegacyBookingMigrationOptions{MerchantID: "merchant-1"})
	if err != nil {
		t.Fatalf("plan migration: %v", err)
	}
	if plan.Ready != 0 || plan.Blocked != 2 {
		t.Fatalf("unexpected plan summary: %+v", plan)
	}
	for _, row := range plan.Rows {
		if row.State != legacyMigrationBlocked || len(row.Reasons) == 0 {
			t.Fatalf("row should be blocked with reasons: %+v", row)
		}
		joined := strings.Join(row.Reasons, "|")
		if row.LegacyAppointmentID == "missing-customer" && !strings.Contains(joined, "顾客 ID 为空") {
			t.Fatalf("missing customer reason absent: %v", row.Reasons)
		}
		if row.LegacyAppointmentID == "unsupported-status" && !strings.Contains(joined, "未自动猜测映射") {
			t.Fatalf("unsupported status reason absent: %v", row.Reasons)
		}
		if !strings.Contains(joined, "不唯一") {
			t.Fatalf("ambiguous service reason absent: %v", row.Reasons)
		}
	}
}

func TestPlanLegacyAppointmentMigrationBlocksBothSidesOfConflict(t *testing.T) {
	appointments := []Appointment{
		{ID: "appointment-a", ShopID: "shop-1", BarberID: "barber-1", CustomerID: "customer-a", Date: "2026-08-30", Time: "14:00", Service: "剪发", Status: "active"},
		{ID: "appointment-b", ShopID: "shop-1", BarberID: "barber-1", CustomerID: "customer-b", Date: "2026-08-30", Time: "14:30", Service: "剪发", Status: "active"},
	}
	services := []Service{{ID: "service-1", ShopID: "shop-1", Name: "剪发", EstimatedMin: 60}}
	barbers := []Barber{{ID: "barber-1", ShopID: "shop-1", Name: "Tony", Active: true}}

	plan, err := PlanLegacyAppointmentMigration(appointments, services, barbers, LegacyBookingMigrationOptions{MerchantID: "merchant-1"})
	if err != nil {
		t.Fatalf("plan migration: %v", err)
	}
	if plan.Ready != 0 || plan.Blocked != 2 {
		t.Fatalf("conflicting rows must both be blocked: %+v", plan)
	}
	for _, row := range plan.Rows {
		if !strings.Contains(strings.Join(row.Reasons, "|"), "员工冲突") {
			t.Fatalf("missing conflict reason for %s: %v", row.LegacyAppointmentID, row.Reasons)
		}
	}
}

func TestPlanLegacyAppointmentMigrationRequiresExplicitMerchant(t *testing.T) {
	if _, err := PlanLegacyAppointmentMigration(nil, nil, nil, LegacyBookingMigrationOptions{}); err == nil || !strings.Contains(err.Error(), "requires merchant ID") {
		t.Fatalf("expected explicit merchant validation, got %v", err)
	}
}

func TestLoadLegacyBookingMigrationInputsReadsOnlySelectedScope(t *testing.T) {
	SetupTestDB(t)
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if err := DB.Create(&Service{ID: "service-1", ShopID: "shop-1", Name: "剪发", EstimatedMin: 60}).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Create(&Barber{ID: "barber-1", ShopID: "shop-1", Name: "Tony", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Create(&Appointment{ID: "appointment-1", ShopID: "shop-1", BarberID: "barber-1", CustomerID: "customer-1", Date: "2026-08-30", Time: "14:00", Service: "剪发", Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Create(&Appointment{ID: "appointment-other-shop", ShopID: "shop-2", BarberID: "barber-2", CustomerID: "customer-2", Date: "2026-08-30", Time: "14:00", Service: "剪发", Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Create(&BookingRecord{
		ID: "next-booking-1", MerchantID: "merchant-1", LocationID: "shop-1", CustomerID: "customer-next",
		ServiceID: "service-next", StaffID: "staff-next", StartAt: time.Date(2026, 8, 30, 16, 0, 0, 0, location), EndAt: time.Date(2026, 8, 30, 17, 0, 0, 0, location),
		Status: string(domain.BookingConfirmed), IdempotencyKey: "next-idem-1",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Create(&BookingAllocationRecord{ID: "next-allocation-1", BookingID: "next-booking-1", MerchantID: "merchant-1", LocationID: "shop-1", StaffID: "staff-next", ResourceID: "chair-1"}).Error; err != nil {
		t.Fatal(err)
	}

	inputs, err := LoadLegacyBookingMigrationInputs(context.Background(), DB, "merchant-1", "shop-1")
	if err != nil {
		t.Fatalf("load migration inputs: %v", err)
	}
	if len(inputs.Appointments) != 1 || inputs.Appointments[0].ID != "appointment-1" {
		t.Fatalf("unexpected appointments: %+v", inputs.Appointments)
	}
	if len(inputs.Services) != 1 || len(inputs.Barbers) != 1 {
		t.Fatalf("unexpected catalog scope: services=%d barbers=%d", len(inputs.Services), len(inputs.Barbers))
	}
	if len(inputs.ExistingBookings) != 1 || len(inputs.ExistingBookingIDs) != 1 || inputs.ExistingBookingIDs[0] != "next-booking-1" {
		t.Fatalf("unexpected next snapshot: %+v ids=%v", inputs.ExistingBookings, inputs.ExistingBookingIDs)
	}
	if len(inputs.ExistingIdempotencyKeys) != 1 || inputs.ExistingIdempotencyKeys[0] != "next-idem-1" {
		t.Fatalf("unexpected idempotency snapshot: %v", inputs.ExistingIdempotencyKeys)
	}
	if len(inputs.ExistingBookings[0].ResourceIDs) != 1 || inputs.ExistingBookings[0].ResourceIDs[0] != "chair-1" {
		t.Fatalf("existing resource allocation was not loaded: %+v", inputs.ExistingBookings[0])
	}
}

func TestExecuteLegacyBookingMigrationIsAtomicAndIdempotent(t *testing.T) {
	SetupTestDB(t)
	appointment := Appointment{ID: "appointment-1", ShopID: "shop-1", BarberID: "barber-1", CustomerID: "customer-1", Date: "2026-08-30", Time: "14:00", Service: "剪发", Status: "active"}
	service := Service{ID: "service-1", ShopID: "shop-1", Name: "剪发", EstimatedMin: 60}
	barber := Barber{ID: "barber-1", ShopID: "shop-1", Name: "Tony", Active: true}
	plan, err := PlanLegacyAppointmentMigration([]Appointment{appointment}, []Service{service}, []Barber{barber}, LegacyBookingMigrationOptions{MerchantID: "merchant-1"})
	if err != nil || plan.Blocked != 0 {
		t.Fatalf("unexpected migration plan: plan=%+v err=%v", plan, err)
	}
	if err := ExecuteLegacyBookingMigration(context.Background(), DB, plan); err != nil {
		t.Fatalf("execute migration: %v", err)
	}
	if err := ExecuteLegacyBookingMigration(context.Background(), DB, plan); err != nil {
		t.Fatalf("replay migration: %v", err)
	}

	var bookingCount, allocationCount, mappingCount int64
	if err := DB.Model(&BookingRecord{}).Where("merchant_id = ?", "merchant-1").Count(&bookingCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Model(&BookingAllocationRecord{}).Where("merchant_id = ?", "merchant-1").Count(&allocationCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Model(&LegacyBookingMigrationRecord{}).Where("merchant_id = ?", "merchant-1").Count(&mappingCount).Error; err != nil {
		t.Fatal(err)
	}
	if bookingCount != 1 || allocationCount != 1 || mappingCount != 1 {
		t.Fatalf("migration should be atomic and idempotent: booking=%d allocation=%d mapping=%d", bookingCount, allocationCount, mappingCount)
	}
}

func TestExecuteLegacyBookingMigrationRejectsBlockedPlanWithoutWrites(t *testing.T) {
	SetupTestDB(t)
	plan, err := PlanLegacyAppointmentMigration([]Appointment{{
		ID: "blocked", ShopID: "shop-1", BarberID: "barber-1", Date: "2026-08-30", Time: "14:00", Service: "剪发", Status: "active",
	}}, []Service{{ID: "service-1", ShopID: "shop-1", Name: "剪发", EstimatedMin: 60}}, []Barber{{ID: "barber-1", ShopID: "shop-1", Name: "Tony", Active: true}}, LegacyBookingMigrationOptions{MerchantID: "merchant-1"})
	if err != nil || plan.Blocked != 1 {
		t.Fatalf("expected blocked plan: plan=%+v err=%v", plan, err)
	}
	if err := ExecuteLegacyBookingMigration(context.Background(), DB, plan); !errors.Is(err, ErrLegacyMigrationPlanBlocked) {
		t.Fatalf("expected blocked plan error, got %v", err)
	}
	var bookingCount, mappingCount int64
	DB.Model(&BookingRecord{}).Count(&bookingCount)
	DB.Model(&LegacyBookingMigrationRecord{}).Count(&mappingCount)
	if bookingCount != 0 || mappingCount != 0 {
		t.Fatalf("blocked plan must not write: booking=%d mapping=%d", bookingCount, mappingCount)
	}
}

func TestExecuteLegacyBookingMigrationRechecksTargetConflict(t *testing.T) {
	SetupTestDB(t)
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	startAt := time.Date(2026, 8, 30, 14, 0, 0, 0, location)
	if err := DB.Create(&BookingRecord{
		ID: "existing-booking", MerchantID: "merchant-1", LocationID: "shop-1", CustomerID: "customer-existing",
		ServiceID: "service-existing", StaffID: "barber-1", StartAt: startAt, EndAt: startAt.Add(time.Hour),
		Status: string(domain.BookingConfirmed), IdempotencyKey: "existing-idem",
	}).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := PlanLegacyAppointmentMigration([]Appointment{{
		ID: "appointment-conflict", ShopID: "shop-1", BarberID: "barber-1", CustomerID: "customer-1", Date: "2026-08-30", Time: "14:00", Service: "剪发", Status: "active",
	}}, []Service{{ID: "service-1", ShopID: "shop-1", Name: "剪发", EstimatedMin: 60}}, []Barber{{ID: "barber-1", ShopID: "shop-1", Name: "Tony", Active: true}}, LegacyBookingMigrationOptions{MerchantID: "merchant-1"})
	if err != nil || plan.Blocked != 0 {
		t.Fatalf("unexpected migration plan: plan=%+v err=%v", plan, err)
	}
	if err := ExecuteLegacyBookingMigration(context.Background(), DB, plan); !errors.Is(err, ErrBookingConflict) {
		t.Fatalf("expected target conflict, got %v", err)
	}
	var migratedCount int64
	DB.Model(&LegacyBookingMigrationRecord{}).Count(&migratedCount)
	if migratedCount != 0 {
		t.Fatalf("conflicting migration must not write mapping: %d", migratedCount)
	}
}
