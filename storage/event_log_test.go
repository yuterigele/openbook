package storage

// event_log_test.go
//
// Tests for event tracking and lifecycle helpers:
//   - TrackEvent: writes an event row
//   - HasShopEvent: idempotency check
//   - CountShopEvents: count by type
//   - FindShopsForLifecycle: D+N trigger discovery (tolerates both MySQL and SQLite time encoding)
//   - MarkAppointmentCompleted: status transition + customer total_visits + FREQUENT tag
//
// Run:
//   go test ./storage/... -v -run "TestTrackEvent|TestHasShopEvent|TestCountShopEvents|TestFindShopsForLifecycle|TestMarkAppointmentCompleted"

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// ===================== TrackEvent =====================

func TestTrackEvent_BasicWrite(t *testing.T) {
	SetupTestDB(t)

	TrackEvent(WithCtx(), "shop-1", EventAppointmentCreated, "appt-1", map[string]any{
		"customer": "Alice",
		"barber":   "Tony",
	})

	var rows []EventLog
	if err := DB.Where("shop_id = ?", "shop-1").Find(&rows).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 event, got %d", len(rows))
	}
	if rows[0].EventType != EventAppointmentCreated {
		t.Errorf("EventType = %q, want %q", rows[0].EventType, EventAppointmentCreated)
	}
	if rows[0].RefID != "appt-1" {
		t.Errorf("RefID = %q, want %q", rows[0].RefID, "appt-1")
	}
	if rows[0].Meta == "" {
		t.Error("Meta should be populated JSON, got empty")
	}
}

func TestTrackEvent_NilMeta(t *testing.T) {
	SetupTestDB(t)

	TrackEvent(WithCtx(), "shop-1", "naked_event", "ref-1", nil)

	var rows []EventLog
	DB.Where("event_type = ?", "naked_event").Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("expected 1 event, got %d", len(rows))
	}
	if rows[0].Meta != "" {
		t.Errorf("Meta = %q, want empty for nil meta", rows[0].Meta)
	}
}

// ===================== HasShopEvent =====================

func TestHasShopEvent_TrueAfterInsert(t *testing.T) {
	SetupTestDB(t)

	has, err := HasShopEvent(WithCtx(), "shop-1", EventFirstAppointment)
	if err != nil {
		t.Fatalf("HasShopEvent before: %v", err)
	}
	if has {
		t.Error("expected false before insert")
	}

	TrackEvent(WithCtx(), "shop-1", EventFirstAppointment, "appt-1", nil)

	has, err = HasShopEvent(WithCtx(), "shop-1", EventFirstAppointment)
	if err != nil {
		t.Fatalf("HasShopEvent after: %v", err)
	}
	if !has {
		t.Error("expected true after insert")
	}
}

func TestHasShopEvent_ShopIsolation(t *testing.T) {
	SetupTestDB(t)
	TrackEvent(WithCtx(), "shop-A", EventFirstAppointment, "appt-1", nil)

	has, _ := HasShopEvent(WithCtx(), "shop-B", EventFirstAppointment)
	if has {
		t.Error("shop-B should not see shop-A's event")
	}
}

func TestHasShopEvent_EventTypeIsolation(t *testing.T) {
	SetupTestDB(t)
	TrackEvent(WithCtx(), "shop-1", EventFirstAppointment, "x", nil)

	has, _ := HasShopEvent(WithCtx(), "shop-1", EventD3Active)
	if has {
		t.Error("different event types should not match")
	}
}

// ===================== CountShopEvents =====================

func TestCountShopEvents_AllTypes(t *testing.T) {
	SetupTestDB(t)
	for i := 0; i < 3; i++ {
		TrackEvent(WithCtx(), "shop-1", EventAppointmentCreated, uuid.NewString(), nil)
	}
	TrackEvent(WithCtx(), "shop-1", EventAppointmentCancelled, uuid.NewString(), nil)

	if got := CountShopEvents(WithCtx(), "shop-1", ""); got != 4 {
		t.Errorf("total = %d, want 4", got)
	}
}

func TestCountShopEvents_Filtered(t *testing.T) {
	SetupTestDB(t)
	for i := 0; i < 5; i++ {
		TrackEvent(WithCtx(), "shop-1", EventAppointmentCreated, uuid.NewString(), nil)
	}
	TrackEvent(WithCtx(), "shop-1", EventAppointmentCancelled, uuid.NewString(), nil)

	if got := CountShopEvents(WithCtx(), "shop-1", EventAppointmentCreated); got != 5 {
		t.Errorf("created count = %d, want 5", got)
	}
	if got := CountShopEvents(WithCtx(), "shop-1", EventAppointmentCancelled); got != 1 {
		t.Errorf("cancelled count = %d, want 1", got)
	}
	if got := CountShopEvents(WithCtx(), "shop-1", "nonexistent_event"); got != 0 {
		t.Errorf("nonexistent count = %d, want 0", got)
	}
}

func TestCountShopEvents_ShopIsolation(t *testing.T) {
	SetupTestDB(t)
	TrackEvent(WithCtx(), "shop-A", EventAppointmentCreated, "x", nil)
	TrackEvent(WithCtx(), "shop-A", EventAppointmentCreated, "y", nil)

	if got := CountShopEvents(WithCtx(), "shop-B", ""); got != 0 {
		t.Errorf("shop-B count = %d, want 0", got)
	}
}

// ===================== FindShopsForLifecycle =====================

func TestFindShopsForLifecycle_NoEvents(t *testing.T) {
	SetupTestDB(t)
	if got := FindShopsForLifecycle(WithCtx(), 3, EventD3Active); len(got) != 0 {
		t.Errorf("empty DB: got %d shops, want 0", len(got))
	}
}

func TestFindShopsForLifecycle_DueToday(t *testing.T) {
	SetupTestDB(t)
	// Seed a first_appointment event "now" so D+3 is due within the ±6h window
	TrackEvent(WithCtx(), "shop-A", EventFirstAppointment, "appt-1", nil)

	got := FindShopsForLifecycle(WithCtx(), 0, EventD3Active)
	// D+0 = "now" should be in window
	if len(got) != 1 || got[0] != "shop-A" {
		t.Errorf("D+0 due now: got %v, want [shop-A]", got)
	}
}

func TestFindShopsForLifecycle_NotDue(t *testing.T) {
	SetupTestDB(t)
	// Insert a first_appointment event with backdated created_at (30 days ago)
	// so D+3 window has passed; the shop should NOT be due for D+3
	rec := EventLog{
		ShopID:    "shop-old",
		EventType: EventFirstAppointment,
		RefID:     "appt-old",
		CreatedAt: time.Now().AddDate(0, 0, -30),
	}
	DB.Create(&rec)

	got := FindShopsForLifecycle(WithCtx(), 3, EventD3Active)
	for _, s := range got {
		if s == "shop-old" {
			t.Errorf("shop-old should not be due for D+3 (its D+3 was 27 days ago)")
		}
	}
}

func TestFindShopsForLifecycle_Idempotent(t *testing.T) {
	SetupTestDB(t)
	TrackEvent(WithCtx(), "shop-A", EventFirstAppointment, "appt-1", nil)

	// Trigger D+3 once
	first := FindShopsForLifecycle(WithCtx(), 0, EventD3Active)
	if len(first) != 1 {
		t.Fatalf("first call: got %v, want [shop-A]", first)
	}
	// Mark D+3 as fired
	TrackEvent(WithCtx(), "shop-A", EventD3Active, "marker", nil)

	// Re-query should now exclude shop-A
	second := FindShopsForLifecycle(WithCtx(), 0, EventD3Active)
	for _, s := range second {
		if s == "shop-A" {
			t.Errorf("shop-A should be excluded after EventD3Active already fired")
		}
	}
}

func TestFindShopsForLifecycle_MultipleShops(t *testing.T) {
	SetupTestDB(t)
	TrackEvent(WithCtx(), "shop-A", EventFirstAppointment, "a", nil)
	TrackEvent(WithCtx(), "shop-B", EventFirstAppointment, "b", nil)

	got := FindShopsForLifecycle(WithCtx(), 0, EventD3Active)
	if len(got) != 2 {
		t.Errorf("expected 2 shops, got %v", got)
	}
}

// ===================== MarkAppointmentCompleted =====================

func TestMarkAppointmentCompleted_TransitionsStatus(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Alice", 0, 0)
	appt := MakeAppointment(t, "shop-1", cust.ID, "Alice", "Tony", "2099-01-01", "10:00")

	if err := MarkAppointmentCompleted(WithCtx(), appt.ID); err != nil {
		t.Fatalf("MarkAppointmentCompleted: %v", err)
	}
	var got Appointment
	DB.First(&got, "id = ?", appt.ID)
	if got.Status != "completed" {
		t.Errorf("Status = %q, want completed", got.Status)
	}
}

func TestMarkAppointmentCompleted_IncrementsCustomerVisits(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Bob", 0, 0)
	appt := MakeAppointment(t, "shop-1", cust.ID, "Bob", "Tony", "2099-01-01", "10:00")

	if err := MarkAppointmentCompleted(WithCtx(), appt.ID); err != nil {
		t.Fatalf("MarkAppointmentCompleted: %v", err)
	}
	var got Customer
	DB.First(&got, "id = ?", cust.ID)
	if got.TotalVisits != 1 {
		t.Errorf("TotalVisits = %d, want 1", got.TotalVisits)
	}
	if got.LastVisitAt == nil {
		t.Error("LastVisitAt should be set")
	}
}

func TestMarkAppointmentCompleted_FrequentTag(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Carol", 0, 0)
	// Simulate 4 prior completed visits by directly setting TotalVisits
	DB.Model(&cust).Update("total_visits", 4)

	// 5th visit: after MarkAppointmentCompleted, total_visits becomes 5 → FREQUENT tag
	appt := MakeAppointment(t, "shop-1", cust.ID, "Carol", "Tony", "2099-01-01", "10:00")
	if err := MarkAppointmentCompleted(WithCtx(), appt.ID); err != nil {
		t.Fatalf("MarkAppointmentCompleted: %v", err)
	}
	var got Customer
	DB.First(&got, "id = ?", cust.ID)
	if !got.IsFrequent() {
		t.Errorf("expected FREQUENT after 5 visits, got tags=%q", got.Tags)
	}
}

func TestMarkAppointmentCompleted_Idempotent(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Dave", 0, 0)
	appt := MakeAppointment(t, "shop-1", cust.ID, "Dave", "Tony", "2099-01-01", "10:00")

	// First call: status active → completed
	if err := MarkAppointmentCompleted(WithCtx(), appt.ID); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call: status completed → idempotent (no error, no double-count)
	if err := MarkAppointmentCompleted(WithCtx(), appt.ID); err != nil {
		t.Fatalf("second call (idempotent): %v", err)
	}
	var got Customer
	DB.First(&got, "id = ?", cust.ID)
	if got.TotalVisits != 1 {
		t.Errorf("TotalVisits = %d, want 1 (idempotent)", got.TotalVisits)
	}
}

func TestMarkAppointmentCompleted_RejectsCancelled(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Eve", 0, 0)
	appt := MakeAppointment(t, "shop-1", cust.ID, "Eve", "Tony", "2099-01-01", "10:00")
	DB.Model(appt).Update("status", "cancelled")

	err := MarkAppointmentCompleted(WithCtx(), appt.ID)
	if err == nil {
		t.Error("expected error when marking cancelled appointment completed")
	}
}

func TestMarkAppointmentCompleted_NotFound(t *testing.T) {
	SetupTestDB(t)
	err := MarkAppointmentCompleted(WithCtx(), uuid.NewString())
	if err == nil {
		t.Error("expected error for non-existent appointment")
	}
}