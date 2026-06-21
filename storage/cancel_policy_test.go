package storage

// cancel_policy_test.go
//
// P3 cancel/no-show policy tests:
//   1. Cancel >= 2h ahead → early_cancel, no penalty
//   2. Cancel < 2h ahead → late_cancel, +1 LateCancelCount
//   3. Cancel after due time → ErrAfterDueCancel (status kept active for noshow scanner)
//   4. Admin cancel → admin_cancel, no penalty
//   5. Threshold breach → auto-BLACKLIST tag + event log
//   6. Legacy CancelAppointment(id) still works (backward compat)
//
// Run:
//   go test ./storage/... -v -run "TestCancel"

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// buildApptTime formats an appointment at `hoursFromNow` hours from now.
func buildApptTime(t *testing.T, hoursFromNow float64) (string, string) {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	apptTime := time.Now().In(loc).Add(time.Duration(hoursFromNow * float64(time.Hour)))
	return apptTime.Format("2006-01-02"), apptTime.Format("15:04")
}

// ===================== Early cancel =====================

func TestCancel_EarlyCancel_NoPenalty(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Alice", 0, 0)
	date, tm := buildApptTime(t, 4) // 4h ahead → early
	appt := MakeAppointment(t, "shop-1", cust.ID, "Alice", "Tony", date, tm)

	res, err := CancelAppointmentWithPolicy(WithCtx(), appt.ID, CancelSourceAgent, "")
	if err != nil {
		t.Fatalf("CancelAppointmentWithPolicy: %v", err)
	}
	if res.CancelType != CancelTypeEarly {
		t.Errorf("CancelType = %q, want %q", res.CancelType, CancelTypeEarly)
	}
	if res.PenaltyApplied {
		t.Error("early cancel should NOT apply penalty")
	}
	if res.Blacklisted {
		t.Error("early cancel should NOT trigger blacklist")
	}

	// Reload appointment
	var got Appointment
	DB.First(&got, "id = ?", appt.ID)
	if got.Status != "cancelled" {
		t.Errorf("Status = %q, want cancelled", got.Status)
	}
	if got.CancelType != CancelTypeEarly {
		t.Errorf("persisted CancelType = %q, want %q", got.CancelType, CancelTypeEarly)
	}

	// Customer's late_cancel_count should be unchanged
	var gotCust Customer
	DB.First(&gotCust, "id = ?", cust.ID)
	if gotCust.LateCancelCount != 0 {
		t.Errorf("LateCancelCount = %d, want 0", gotCust.LateCancelCount)
	}
}

// ===================== Late cancel =====================

func TestCancel_LateCancel_PenaltyApplied(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Bob", 0, 0)
	date, tm := buildApptTime(t, 0.5) // 30 min ahead → late
	appt := MakeAppointment(t, "shop-1", cust.ID, "Bob", "Tony", date, tm)

	res, err := CancelAppointmentWithPolicy(WithCtx(), appt.ID, CancelSourceAgent, "")
	if err != nil {
		t.Fatalf("CancelAppointmentWithPolicy: %v", err)
	}
	if res.CancelType != CancelTypeLate {
		t.Errorf("CancelType = %q, want %q", res.CancelType, CancelTypeLate)
	}
	if !res.PenaltyApplied {
		t.Error("late cancel should apply penalty")
	}
	if res.Warning == "" {
		t.Error("late cancel should produce a warning text")
	}

	// Customer's late_cancel_count should be +1
	var gotCust Customer
	DB.First(&gotCust, "id = ?", cust.ID)
	if gotCust.LateCancelCount != 1 {
		t.Errorf("LateCancelCount = %d, want 1", gotCust.LateCancelCount)
	}
}

func TestCancel_LateCancel_TriggersBlacklist(t *testing.T) {
	SetupTestDB(t)
	// Start with 2 late cancels (threshold = 3)
	cust := MakeCustomer(t, "Carol", 2, 0)
	date, tm := buildApptTime(t, 0.5)
	appt := MakeAppointment(t, "shop-1", cust.ID, "Carol", "Tony", date, tm)

	res, err := CancelAppointmentWithPolicy(WithCtx(), appt.ID, CancelSourceAgent, "")
	if err != nil {
		t.Fatalf("CancelAppointmentWithPolicy: %v", err)
	}
	if !res.Blacklisted {
		t.Error("expected blacklist to trigger (count 2 + 1 = 3 >= threshold 3)")
	}
	if res.BlacklistReason != "late_cancel_threshold" {
		t.Errorf("BlacklistReason = %q, want late_cancel_threshold", res.BlacklistReason)
	}

	// Customer should have BLACKLIST tag
	var gotCust Customer
	DB.First(&gotCust, "id = ?", cust.ID)
	if !gotCust.IsBlacklisted() {
		t.Errorf("customer should be blacklisted, got tags=%q", gotCust.Tags)
	}

	// EventBlacklisted event should be logged
	var blEvents []EventLog
	DB.Where("customer_id = ? AND event_type = ?", cust.ID, EventBlacklisted).Find(&blEvents)
	if len(blEvents) != 1 {
		t.Errorf("expected 1 blacklist event, got %d", len(blEvents))
	}
}

// ===================== After-due cancel =====================

func TestCancel_AfterDue_ReturnsErrAfterDueCancel(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Dave", 0, 0)
	date, tm := buildApptTime(t, -1) // 1h in the past
	appt := MakeAppointment(t, "shop-1", cust.ID, "Dave", "Tony", date, tm)

	_, err := CancelAppointmentWithPolicy(WithCtx(), appt.ID, CancelSourceAgent, "")
	if !errors.Is(err, ErrAfterDueCancel) {
		t.Fatalf("expected ErrAfterDueCancel, got %v", err)
	}

	// Appointment status should remain active (noshow scanner will catch it)
	var got Appointment
	DB.First(&got, "id = ?", appt.ID)
	if got.Status != "active" {
		t.Errorf("Status = %q, want active (after-due should NOT cancel)", got.Status)
	}
}

// ===================== Admin cancel =====================

func TestCancel_Admin_NoPenalty(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Eve", 5, 5) // would normally trigger blacklist
	date, tm := buildApptTime(t, 0.5)    // late timing
	appt := MakeAppointment(t, "shop-1", cust.ID, "Eve", "Tony", date, tm)

	res, err := CancelAppointmentWithPolicy(WithCtx(), appt.ID, CancelSourceAdmin, "merchant cancel")
	if err != nil {
		t.Fatalf("CancelAppointmentWithPolicy: %v", err)
	}
	if res.CancelType != CancelTypeAdmin {
		t.Errorf("CancelType = %q, want %q", res.CancelType, CancelTypeAdmin)
	}
	if res.PenaltyApplied {
		t.Error("admin cancel should NOT apply penalty")
	}

	// Counts should not have changed
	var gotCust Customer
	DB.First(&gotCust, "id = ?", cust.ID)
	if gotCust.LateCancelCount != 5 {
		t.Errorf("LateCancelCount = %d, want 5 (unchanged)", gotCust.LateCancelCount)
	}
}

// ===================== System cancel =====================

func TestCancel_System_NoPenalty(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Frank", 0, 0)
	date, tm := buildApptTime(t, 0.5)
	appt := MakeAppointment(t, "shop-1", cust.ID, "Frank", "Tony", date, tm)

	res, err := CancelAppointmentWithPolicy(WithCtx(), appt.ID, CancelSourceSystem, "")
	if err != nil {
		t.Fatalf("CancelAppointmentWithPolicy: %v", err)
	}
	if res.CancelType != CancelTypeSystem {
		t.Errorf("CancelType = %q, want %q", res.CancelType, CancelTypeSystem)
	}
}

// ===================== Edge cases =====================

func TestCancel_EmptyAppointmentID(t *testing.T) {
	SetupTestDB(t)
	_, err := CancelAppointmentWithPolicy(WithCtx(), "", CancelSourceAgent, "")
	if err == nil {
		t.Error("expected error for empty appointment_id")
	}
}

func TestCancel_NotFound(t *testing.T) {
	SetupTestDB(t)
	_, err := CancelAppointmentWithPolicy(WithCtx(), "nonexistent", CancelSourceAgent, "")
	if !errors.Is(err, ErrAppointmentNotFound) {
		t.Errorf("expected ErrAppointmentNotFound, got %v", err)
	}
}

func TestCancel_AlreadyCancelled(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Grace", 0, 0)
	date, tm := buildApptTime(t, 4)
	appt := MakeAppointment(t, "shop-1", cust.ID, "Grace", "Tony", date, tm)

	// First cancel: OK
	if _, err := CancelAppointmentWithPolicy(WithCtx(), appt.ID, CancelSourceAgent, ""); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	// Second cancel: should fail (already cancelled)
	_, err := CancelAppointmentWithPolicy(WithCtx(), appt.ID, CancelSourceAgent, "")
	if !errors.Is(err, ErrAlreadyCancelled) {
		t.Errorf("expected ErrAlreadyCancelled, got %v", err)
	}
}

func TestCancel_ReasonRecorded(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Henry", 0, 0)
	date, tm := buildApptTime(t, 4)
	appt := MakeAppointment(t, "shop-1", cust.ID, "Henry", "Tony", date, tm)

	if _, err := CancelAppointmentWithPolicy(WithCtx(), appt.ID, CancelSourceAgent, "kids sick"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	var got Appointment
	DB.First(&got, "id = ?", appt.ID)
	if !strings.Contains(got.CancelReason, "kids sick") {
		t.Errorf("CancelReason = %q, want to contain 'kids sick'", got.CancelReason)
	}
}

// ===================== Backward compat =====================

func TestCancel_LegacyCancelAppointment(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Ivy", 0, 0)
	date, tm := buildApptTime(t, 4)
	appt := MakeAppointment(t, "shop-1", cust.ID, "Ivy", "Tony", date, tm)

	// Legacy signature: just ID, no source
	if err := CancelAppointment(appt.ID); err != nil {
		t.Fatalf("legacy CancelAppointment: %v", err)
	}
	var got Appointment
	DB.First(&got, "id = ?", appt.ID)
	if got.Status != "cancelled" {
		t.Errorf("Status = %q, want cancelled", got.Status)
	}
}

// ===================== formatDuration unit =====================

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{2 * time.Hour, "2 小时"},
		{30 * time.Minute, "30 分钟"},
		{90 * time.Minute, "1 小时"}, // 90min rounds to 1 hour
		{1 * time.Minute, "1 分钟"},
	}
	for _, c := range cases {
		if got := formatDuration(c.d); got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}