package tools

// cancel_appointment_test.go
//
// Tests for the CancelAppointmentTool (Agent-callable surface for P3 cancel policy):
//   - Info() returns a valid ToolInfo
//   - InvokableRun: parameter validation (empty appointment_id, invalid JSON)
//   - InvokableRun: early cancel → friendly success message, no warning
//   - InvokableRun: late cancel → success + warning text
//   - InvokableRun: late cancel with threshold breach → + blacklist message
//   - InvokableRun: after-due cancel → friendly redirect to mark_no_show (NOT error)
//   - InvokableRun: not-found → error
//   - InvokableRun: already-cancelled → error
//   - InvokableRun: reason field is persisted
//
// Run:
//   go test ./tools/... -v -run TestCancel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// buildApptTimeStr returns a (date, "HH:MM") pair at hoursFromNow from current time.
func buildApptTimeStr(t *testing.T, hoursFromNow float64) (string, string) {
	t.Helper()
	apptTime := time.Now().Add(time.Duration(hoursFromNow * float64(time.Hour)))
	return apptTime.Format("2006-01-02"), apptTime.Format("15:04")
}

// runCancel runs the cancel tool with the given JSON arguments.
func runCancel(t *testing.T, c *CancelAppointmentTool, argsJSON string) (string, error) {
	t.Helper()
	return c.InvokableRun(context.Background(), argsJSON)
}

// ===================== Info =====================

func TestCancelAppointmentTool_Info(t *testing.T) {
	c := &CancelAppointmentTool{}
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "cancel_appointment" {
		t.Errorf("Name = %q, want cancel_appointment", info.Name)
	}
	if !strings.Contains(info.Desc, "2 小时") {
		t.Errorf("Desc should mention 2h policy, got %q", info.Desc)
	}
}

// ===================== Param validation =====================

func TestCancelAppointmentTool_EmptyAppointmentID(t *testing.T) {
	c := &CancelAppointmentTool{}
	out, err := runCancel(t, c, `{"appointment_id": ""}`)
	if err == nil {
		t.Error("expected error for empty appointment_id")
	}
	if out != "" {
		t.Errorf("output should be empty on error, got %q", out)
	}
}

func TestCancelAppointmentTool_InvalidJSON(t *testing.T) {
	c := &CancelAppointmentTool{}
	_, err := runCancel(t, c, `not-json`)
	if err == nil {
		t.Error("expected error for invalid JSON args")
	}
}

// ===================== Behavior =====================

func TestCancelAppointmentTool_NotFound(t *testing.T) {
	setupToolsTestDB(t)
	c := &CancelAppointmentTool{}
	_, err := runCancel(t, c, `{"appointment_id": "nonexistent-id"}`)
	if err == nil {
		t.Error("expected error for non-existent appointment")
	}
}

func TestCancelAppointmentTool_EarlyCancel(t *testing.T) {
	setupToolsTestDB(t)
	cust := makeToolsCustomer(t, "Alice", 0)
	date, tm := buildApptTimeStr(t, 4) // 4h ahead → early
	appt := makeToolsAppointment(t, "shop-1", cust.ID, "Alice", "Tony", date, tm)

	c := &CancelAppointmentTool{}
	out, err := runCancel(t, c, `{"appointment_id": "`+appt.ID+`"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(out, "成功取消") {
		t.Errorf("output should mention '成功取消', got %q", out)
	}
	if strings.Contains(out, "晚退订") {
		t.Errorf("early cancel should NOT mention late-cancel warning, got %q", out)
	}
	if strings.Contains(out, "黑名单") {
		t.Errorf("early cancel should NOT mention blacklist, got %q", out)
	}

	var got storage.Appointment
	storage.DB.First(&got, "id = ?", appt.ID)
	if got.Status != "cancelled" {
		t.Errorf("Status = %q, want cancelled", got.Status)
	}
}

func TestCancelAppointmentTool_LateCancel_AddsWarning(t *testing.T) {
	setupToolsTestDB(t)
	cust := makeToolsCustomer(t, "Bob", 0)
	date, tm := buildApptTimeStr(t, 0.5) // 30 min ahead → late
	appt := makeToolsAppointment(t, "shop-1", cust.ID, "Bob", "Tony", date, tm)

	c := &CancelAppointmentTool{}
	out, err := runCancel(t, c, `{"appointment_id": "`+appt.ID+`"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(out, "成功取消") {
		t.Errorf("output should mention '成功取消', got %q", out)
	}
	if !strings.Contains(out, "晚退订") {
		t.Errorf("late cancel should mention '晚退订', got %q", out)
	}
	if !strings.Contains(out, "2 小时") {
		t.Errorf("late cancel warning should mention 2h, got %q", out)
	}
}

func TestCancelAppointmentTool_LateCancel_BlacklistWarning(t *testing.T) {
	setupToolsTestDB(t)
	// 2 prior late cancels → +1 makes 3 = threshold
	cust := makeToolsCustomer(t, "Carol", 2)
	date, tm := buildApptTimeStr(t, 0.5)
	appt := makeToolsAppointment(t, "shop-1", cust.ID, "Carol", "Tony", date, tm)

	c := &CancelAppointmentTool{}
	out, err := runCancel(t, c, `{"appointment_id": "`+appt.ID+`"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(out, "黑名单") {
		t.Errorf("blacklist message should be appended, got %q", out)
	}

	var got storage.Customer
	storage.DB.First(&got, "id = ?", cust.ID)
	if !got.IsBlacklisted() {
		t.Errorf("customer should be blacklisted after threshold breach, got tags=%q", got.Tags)
	}
}

func TestCancelAppointmentTool_AfterDue_FriendlyRedirect(t *testing.T) {
	setupToolsTestDB(t)
	cust := makeToolsCustomer(t, "Dave", 0)
	date, tm := buildApptTimeStr(t, -1) // 1h in the past
	appt := makeToolsAppointment(t, "shop-1", cust.ID, "Dave", "Tony", date, tm)

	c := &CancelAppointmentTool{}
	out, err := runCancel(t, c, `{"appointment_id": "`+appt.ID+`"}`)
	// After-due returns (msg, nil) — the tool converts the error to a friendly redirect
	if err != nil {
		t.Fatalf("InvokableRun should NOT error on after-due, got %v", err)
	}
	if !strings.Contains(out, "过预约时间") {
		t.Errorf("output should mention '过预约时间', got %q", out)
	}
	if !strings.Contains(out, "mark_no_show") {
		t.Errorf("output should redirect to mark_no_show tool, got %q", out)
	}

	// Status should remain active (not cancelled)
	var got storage.Appointment
	storage.DB.First(&got, "id = ?", appt.ID)
	if got.Status != "active" {
		t.Errorf("Status = %q, want active (after-due should not cancel)", got.Status)
	}
}

func TestCancelAppointmentTool_AlreadyCancelled(t *testing.T) {
	setupToolsTestDB(t)
	cust := makeToolsCustomer(t, "Eve", 0)
	date, tm := buildApptTimeStr(t, 4)
	appt := makeToolsAppointment(t, "shop-1", cust.ID, "Eve", "Tony", date, tm)

	c := &CancelAppointmentTool{}
	if _, err := runCancel(t, c, `{"appointment_id": "`+appt.ID+`"}`); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	if _, err := runCancel(t, c, `{"appointment_id": "`+appt.ID+`"}`); err == nil {
		t.Error("expected error when re-cancelling")
	}
}

func TestCancelAppointmentTool_WithReason(t *testing.T) {
	setupToolsTestDB(t)
	cust := makeToolsCustomer(t, "Frank", 0)
	date, tm := buildApptTimeStr(t, 4)
	appt := makeToolsAppointment(t, "shop-1", cust.ID, "Frank", "Tony", date, tm)

	c := &CancelAppointmentTool{}
	out, err := runCancel(t, c, `{"appointment_id": "`+appt.ID+`", "reason": "kids sick"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(out, "成功取消") {
		t.Errorf("output should mention success, got %q", out)
	}

	var got storage.Appointment
	storage.DB.First(&got, "id = ?", appt.ID)
	if !strings.Contains(got.CancelReason, "kids sick") {
		t.Errorf("CancelReason = %q, want to contain 'kids sick'", got.CancelReason)
	}
}