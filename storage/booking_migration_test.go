package storage

import (
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
