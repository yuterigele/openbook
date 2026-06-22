package api

// api_test.go
//
// Handler-level tests for the api package — no live hertz router, just direct
// handler invocation against a constructed RequestContext.
//
// Coverage:
//   - adminCancelHandler        (PRD §11.6 P3 admin cancel path)
//   - createBarberLeaveHandler  (PRD §11.7 P4 per-barber route /barber/:id/leave)
//   - cancelBarberLeaveHandler  (PRD §11.7 P4 per-barber route DELETE)
//   - listBarberLeavesHandler   (PRD §11.7 P4 per-barber route GET /leaves)
//
// Pattern: setupAPITestDB → plant fixtures (shop / barber / customer / appt) →
// build ctx via newAPIContext (with optional path-param / claims) → runHandler →
// assert on (status, body) and side-effects (DB row state).
//
// Run:
//   go test ./api/... -v

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

// decodeJSON unmarshals the response body into a map for assertion.
func decodeJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	if body == "" {
		t.Fatalf("empty body, expected JSON")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode JSON %q: %v", body, err)
	}
	return m
}

// futureDate returns today+offsetDays formatted as YYYY-MM-DD.
func futureDate(t *testing.T, offsetDays int) string {
	t.Helper()
	return time.Now().AddDate(0, 0, offsetDays).Format("2006-01-02")
}

// futureTimeRFC3339 returns an RFC3339 timestamp now+offsetHours.
// Used by P4 leave API which expects string-encoded RFC3339 in JSON.
func futureTimeRFC3339(t *testing.T, offsetHours float64) string {
	t.Helper()
	return time.Now().Add(time.Duration(offsetHours * float64(time.Hour))).Format(time.RFC3339)
}

// buildApptTime returns (date "YYYY-MM-DD", time "HH:MM") at hoursFromNow.
// Matches the same-name helper in storage package tests; duplicated here so
// the api test package doesn't need to import internal test-only files.
func buildApptTime(t *testing.T, hoursFromNow float64) (string, string) {
	t.Helper()
	at := time.Now().Add(time.Duration(hoursFromNow * float64(time.Hour)))
	return at.Format("2006-01-02"), at.Format("15:04")
}

// =====================================================================
// adminCancelHandler  — POST /api/admin/appointment/cancel  (PRD §11.6 P3)
// =====================================================================

// TestAdminCancel_NoClaims — no auth_claims in ctx → 401.
func TestAdminCancel_NoClaims(t *testing.T) {
	setupAPITestDB(t)
	body := jsonRaw(`{"appointment_id": "any", "reason": "test"}`)
	ctx := newAPIContext(t, "POST", "/api/admin/appointment/cancel", body)

	status, respBody := runHandler(t, adminCancelHandler, ctx)
	if status != statusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "no shop in session") {
		t.Errorf("body should mention 'no shop in session', got %q", respBody)
	}
}

// TestAdminCancel_BadJSON — request body is not valid JSON → 400.
func TestAdminCancel_BadJSON(t *testing.T) {
	setupAPITestDB(t)
	body := jsonRaw(`{garbage`)
	ctx := newAPIContext(t, "POST", "/api/admin/appointment/cancel", body,
		withClaims(adminClaims("shop-A")))

	status, _ := runHandler(t, adminCancelHandler, ctx)
	if status != statusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

// TestAdminCancel_AppointmentNotFound — appointment_id doesn't exist → 404.
func TestAdminCancel_AppointmentNotFound(t *testing.T) {
	setupAPITestDB(t)
	body := jsonRaw(`{"appointment_id": "missing-id", "reason": "x"}`)
	ctx := newAPIContext(t, "POST", "/api/admin/appointment/cancel", body,
		withClaims(adminClaims("shop-A")))

	status, respBody := runHandler(t, adminCancelHandler, ctx)
	if status != statusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "预约不存在") {
		t.Errorf("body should mention '预约不存在', got %q", respBody)
	}
}

// TestAdminCancel_CrossShopForbidden — appointment belongs to another shop → 403.
func TestAdminCancel_CrossShopForbidden(t *testing.T) {
	setupAPITestDB(t)
	shopA := storage.MakeShop(t, "shop-A", "")
	shopB := storage.MakeShop(t, "shop-B", "")
	cust := storage.MakeCustomer(t, "Alice", 0, 0)
	// Appointment belongs to shop-B but claims say shop-A.
	appt := storage.MakeAppointment(t, shopB.ID, cust.ID, "Alice", "Tony", futureDate(t, 1), "14:00")

	body := jsonRaw(`{"appointment_id": "` + appt.ID + `", "reason": "x"}`)
	ctx := newAPIContext(t, "POST", "/api/admin/appointment/cancel", body,
		withClaims(adminClaims(shopA.ID)))

	status, respBody := runHandler(t, adminCancelHandler, ctx)
	if status != statusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "无权操作其他店铺") {
		t.Errorf("body should mention '无权操作其他店铺', got %q", respBody)
	}
}

// TestAdminCancel_HappyPath — valid request → 200 + cancel_type=admin_cancel + DB row updated.
//
// Far-future appointment (3 days ahead) → early-cancel window is 2h, so this is well outside
// the late-cancel threshold. But because source=admin, cancel_type is admin_cancel regardless
// of timing (P3 admin path is exempt from penalty).
func TestAdminCancel_HappyPath(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	cust := storage.MakeCustomer(t, "Alice", 0, 0)
	appt := storage.MakeAppointment(t, shop.ID, cust.ID, "Alice", "Tony", futureDate(t, 3), "10:00")

	body := jsonRaw(`{"appointment_id": "` + appt.ID + `", "reason": "merchant asked"}`)
	ctx := newAPIContext(t, "POST", "/api/admin/appointment/cancel", body,
		withClaims(adminClaims(shop.ID)))

	status, respBody := runHandler(t, adminCancelHandler, ctx)
	if status != statusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, respBody)
	}

	m := decodeJSON(t, respBody)
	if m["status"] != "cancelled" {
		t.Errorf("status field = %v, want cancelled", m["status"])
	}
	if m["cancel_type"] != storage.CancelTypeAdmin {
		t.Errorf("cancel_type = %v, want %q", m["cancel_type"], storage.CancelTypeAdmin)
	}

	// Verify DB side-effect.
	got, err := storage.GetAppointment(appt.ID)
	if err != nil {
		t.Fatalf("GetAppointment: %v", err)
	}
	if got.Status != "cancelled" {
		t.Errorf("appt.Status = %q, want cancelled", got.Status)
	}
	if got.CancelType != storage.CancelTypeAdmin {
		t.Errorf("appt.CancelType = %q, want %q", got.CancelType, storage.CancelTypeAdmin)
	}
	if got.CancelReason != "merchant asked" {
		t.Errorf("appt.CancelReason = %q, want %q", got.CancelReason, "merchant asked")
	}
	if got.CancelledAt == nil {
		t.Error("appt.CancelledAt should be set after admin cancel")
	}
}

// TestAdminCancel_AlreadyCancelled — second admin cancel returns 500 (CancelAppointmentWithPolicy
// returns the "appointment already in non-active status" error).
//
// We don't enforce idempotency in adminCancelHandler; we just bubble up the storage error.
// This test pins that behavior so a future refactor doesn't silently swallow it.
func TestAdminCancel_AlreadyCancelled(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	cust := storage.MakeCustomer(t, "Alice", 0, 0)
	appt := storage.MakeAppointment(t, shop.ID, cust.ID, "Alice", "Tony", futureDate(t, 3), "10:00")

	body := jsonRaw(`{"appointment_id": "` + appt.ID + `"}`)
	cl := adminClaims(shop.ID)

	// First cancel → 200
	ctx1 := newAPIContext(t, "POST", "/api/admin/appointment/cancel", body, withClaims(cl))
	status1, _ := runHandler(t, adminCancelHandler, ctx1)
	if status1 != statusOK {
		t.Fatalf("first cancel: status=%d, want 200", status1)
	}

	// Second cancel → 500 (storage error bubbles up).
	ctx2 := newAPIContext(t, "POST", "/api/admin/appointment/cancel", body, withClaims(cl))
	status2, _ := runHandler(t, adminCancelHandler, ctx2)
	if status2 != 500 {
		t.Errorf("second cancel: status=%d, want 500", status2)
	}
}

// =====================================================================
// createBarberLeaveHandler  — POST /api/admin/barber/:id/leave  (PRD §11.7 P4)
//
// Uses c.Param("id") for barber_id; we set the path param via withPathParam.
// =====================================================================

// TestCreateBarberLeave_NoClaims → 401.
func TestCreateBarberLeave_NoClaims(t *testing.T) {
	body := jsonRaw(`{"start_at":"` + futureTimeRFC3339(t, 1) + `","end_at":"` + futureTimeRFC3339(t, 4) + `","action":"cancel"}`)
	ctx := newAPIContext(t, "POST", "/api/admin/barber/b-1/leave", body)

	status, _ := runHandler(t, createBarberLeaveHandler, ctx)
	if status != statusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

// TestCreateBarberLeave_BadRFC3339 — start_at not in RFC3339 → 400.
func TestCreateBarberLeave_BadRFC3339(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	barber := storage.MakeBarber(t, "b-1", shop.ID, "Tony")

	body := jsonRaw(`{"start_at":"not-a-date","end_at":"` + futureTimeRFC3339(t, 4) + `","action":"cancel"}`)
	ctx := newAPIContext(t, "POST", "/api/admin/barber/b-1/leave", body,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", barber.ID),
	)

	status, respBody := runHandler(t, createBarberLeaveHandler, ctx)
	if status != statusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "start_at") {
		t.Errorf("body should mention start_at, got %q", respBody)
	}
}

// TestCreateBarberLeave_EndBadRFC3339 — end_at not in RFC3339 → 400.
func TestCreateBarberLeave_EndBadRFC3339(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	barber := storage.MakeBarber(t, "b-1", shop.ID, "Tony")

	body := jsonRaw(`{"start_at":"` + futureTimeRFC3339(t, 1) + `","end_at":"not-a-date","action":"cancel"}`)
	ctx := newAPIContext(t, "POST", "/api/admin/barber/b-1/leave", body,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", barber.ID),
	)

	status, respBody := runHandler(t, createBarberLeaveHandler, ctx)
	if status != statusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "end_at") {
		t.Errorf("body should mention end_at, got %q", respBody)
	}
}

// TestCreateBarberLeave_StartAfterEnd — start_at >= end_at → 400.
func TestCreateBarberLeave_StartAfterEnd(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	barber := storage.MakeBarber(t, "b-1", shop.ID, "Tony")

	start := futureTimeRFC3339(t, 4)
	end := futureTimeRFC3339(t, 1) // end < start

	body := jsonRaw(`{"start_at":"` + start + `","end_at":"` + end + `","action":"cancel"}`)
	ctx := newAPIContext(t, "POST", "/api/admin/barber/b-1/leave", body,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", barber.ID),
	)

	status, respBody := runHandler(t, createBarberLeaveHandler, ctx)
	if status != statusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "start_at") {
		t.Errorf("body should mention start_at ordering, got %q", respBody)
	}
}

// TestCreateBarberLeave_BarberNotFound → 404.
func TestCreateBarberLeave_BarberNotFound(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")

	body := jsonRaw(`{"start_at":"` + futureTimeRFC3339(t, 1) + `","end_at":"` + futureTimeRFC3339(t, 4) + `","action":"cancel"}`)
	ctx := newAPIContext(t, "POST", "/api/admin/barber/no-such-barber/leave", body,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", "no-such-barber"),
	)

	status, _ := runHandler(t, createBarberLeaveHandler, ctx)
	if status != statusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

// TestCreateBarberLeave_CrossShopForbidden — barber belongs to a different shop → 403.
func TestCreateBarberLeave_CrossShopForbidden(t *testing.T) {
	setupAPITestDB(t)
	shopA := storage.MakeShop(t, "shop-A", "")
	shopB := storage.MakeShop(t, "shop-B", "")
	barberB := storage.MakeBarber(t, "b-1", shopB.ID, "Tony")

	body := jsonRaw(`{"start_at":"` + futureTimeRFC3339(t, 1) + `","end_at":"` + futureTimeRFC3339(t, 4) + `","action":"cancel"}`)
	ctx := newAPIContext(t, "POST", "/api/admin/barber/b-1/leave", body,
		withClaims(adminClaims(shopA.ID)),
		withPathParam("id", barberB.ID),
	)

	status, respBody := runHandler(t, createBarberLeaveHandler, ctx)
	if status != statusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "无权操作其他店铺") {
		t.Errorf("body should mention '无权操作其他店铺', got %q", respBody)
	}
}

// TestCreateBarberLeave_HappyPath_CancelAction — full happy path; verifies side-effect on appt.
//
// The defaultLeaveSender (assigned by RegisterRoutes) is NOT set in this test, so
// notifSender is nil and CreateBarberLeave skips the WeChat notification step.
func TestCreateBarberLeave_HappyPath_CancelAction(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	// Barber ID must match the "barber-Tony" convention used by MakeAppointment
	// so the appointment's barber_id matches the leave's barber_id.
	barber := storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := storage.MakeCustomer(t, "Alice", 0, 0)
	// Appt at 2h ahead (inside the [now+1h, now+5h] leave window).
	apptDate, apptTime := buildApptTime(t, 2)
	appt := storage.MakeAppointment(t, shop.ID, cust.ID, "Alice", barber.Name, apptDate, apptTime)

	body := jsonRaw(`{"start_at":"` + futureTimeRFC3339(t, 1) + `","end_at":"` + futureTimeRFC3339(t, 5) + `","reason":"病假","action":"cancel"}`)
	ctx := newAPIContext(t, "POST", "/api/admin/barber/b-1/leave", body,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", barber.ID),
	)

	status, respBody := runHandler(t, createBarberLeaveHandler, ctx)
	if status != statusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, respBody)
	}
	m := decodeJSON(t, respBody)
	if m["leave_id"] == nil || m["leave_id"] == "" {
		t.Error("response missing leave_id")
	}
	if m["affected_count"].(float64) < 1 {
		t.Errorf("affected_count = %v, want >=1", m["affected_count"])
	}
	if m["cancelled_count"].(float64) < 1 {
		t.Errorf("cancelled_count = %v, want >=1", m["cancelled_count"])
	}

	// Verify: appointment was cancelled with admin_cancel (P3 admin path = no penalty).
	got, err := storage.GetAppointment(appt.ID)
	if err != nil {
		t.Fatalf("GetAppointment: %v", err)
	}
	if got.Status != "cancelled" {
		t.Errorf("appt.Status = %q, want cancelled", got.Status)
	}
	if got.CancelType != storage.CancelTypeAdmin {
		t.Errorf("appt.CancelType = %q, want %q", got.CancelType, storage.CancelTypeAdmin)
	}
}

// TestCreateBarberLeave_HappyPath_RescheduleAction — reschedule action with available alternate barber.
//
// Setup: barber-1 (going on leave), barber-2 (alternate, free at the same slot).
// Expectation: appt at barber-1 is reassigned to barber-2.
func TestCreateBarberLeave_HappyPath_RescheduleAction(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	barber1 := storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	barber2 := storage.MakeBarber(t, "barber-Leo", shop.ID, "Leo") // alternate, no appts
	cust := storage.MakeCustomer(t, "Alice", 0, 0)

	// Appt at 3h ahead (inside the [now+1h, now+5h] leave window).
	apptDate, apptTime := buildApptTime(t, 3)
	appt := storage.MakeAppointment(t, shop.ID, cust.ID, "Alice", barber1.Name, apptDate, apptTime)

	body := jsonRaw(`{"start_at":"` + futureTimeRFC3339(t, 1) + `","end_at":"` + futureTimeRFC3339(t, 5) + `","reason":"家里有事","action":"reschedule"}`)
	ctx := newAPIContext(t, "POST", "/api/admin/barber/b-1/leave", body,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", barber1.ID),
	)

	status, respBody := runHandler(t, createBarberLeaveHandler, ctx)
	if status != statusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, respBody)
	}
	m := decodeJSON(t, respBody)
	if m["rescheduled_count"].(float64) < 1 {
		t.Errorf("rescheduled_count = %v, want >=1", m["rescheduled_count"])
	}

	// Verify: appt's barber_id should now be barber2's.
	got, err := storage.GetAppointment(appt.ID)
	if err != nil {
		t.Fatalf("GetAppointment: %v", err)
	}
	if got.BarberID != barber2.ID {
		t.Errorf("appt.BarberID = %q, want reassigned to %q", got.BarberID, barber2.ID)
	}
	if got.Status != "active" {
		t.Errorf("appt.Status = %q, want active (reschedule should not cancel)", got.Status)
	}
}

// =====================================================================
// cancelBarberLeaveHandler  — DELETE /api/admin/barber/:id/leave/:leaveID  (PRD §11.7 P4)
// =====================================================================

// TestCancelBarberLeave_NoClaims → 401.
func TestCancelBarberLeave_NoClaims(t *testing.T) {
	ctx := newAPIContext(t, "DELETE", "/api/admin/barber/b-1/leave/leave-1", nil,
		withPathParam("id", "b-1"),
		withPathParam("leaveID", "leave-1"),
	)

	status, _ := runHandler(t, cancelBarberLeaveHandler, ctx)
	if status != statusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

// TestCancelBarberLeave_LeaveNotFound → 404.
func TestCancelBarberLeave_LeaveNotFound(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	barber := storage.MakeBarber(t, "b-1", shop.ID, "Tony")

	ctx := newAPIContext(t, "DELETE", "/api/admin/barber/b-1/leave/no-such-leave", nil,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", barber.ID),
		withPathParam("leaveID", "no-such-leave"),
	)

	status, _ := runHandler(t, cancelBarberLeaveHandler, ctx)
	if status != statusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

// TestCancelBarberLeave_CrossShopForbidden → 403.
func TestCancelBarberLeave_CrossShopForbidden(t *testing.T) {
	setupAPITestDB(t)
	shopA := storage.MakeShop(t, "shop-A", "")
	shopB := storage.MakeShop(t, "shop-B", "")
	barberB := storage.MakeBarber(t, "b-1", shopB.ID, "Tony")

	start := time.Now().Add(2 * time.Hour)
	end := time.Now().Add(8 * time.Hour)
	leave := storage.MakeBarberLeave(t, shopB.ID, barberB.ID, start, end, storage.LeaveActionCancel)

	ctx := newAPIContext(t, "DELETE", "/api/admin/barber/b-1/leave/"+leave.ID, nil,
		withClaims(adminClaims(shopA.ID)),
		withPathParam("id", barberB.ID),
		withPathParam("leaveID", leave.ID),
	)

	status, respBody := runHandler(t, cancelBarberLeaveHandler, ctx)
	if status != statusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "无权操作其他店铺") {
		t.Errorf("body should mention '无权操作其他店铺', got %q", respBody)
	}
}

// TestCancelBarberLeave_BarberMismatch — leave exists but barber_id doesn't match path param → 400.
func TestCancelBarberLeave_BarberMismatch(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	barberA := storage.MakeBarber(t, "b-A", shop.ID, "Tony")

	start := time.Now().Add(2 * time.Hour)
	end := time.Now().Add(8 * time.Hour)
	leave := storage.MakeBarberLeave(t, shop.ID, barberA.ID, start, end, storage.LeaveActionCancel)

	// Path param says "b-B" but leave belongs to barberA → 400.
	ctx := newAPIContext(t, "DELETE", "/api/admin/barber/b-B/leave/"+leave.ID, nil,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", "b-B"),
		withPathParam("leaveID", leave.ID),
	)

	status, respBody := runHandler(t, cancelBarberLeaveHandler, ctx)
	if status != statusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "不匹配") {
		t.Errorf("body should mention '不匹配', got %q", respBody)
	}
}

// TestCancelBarberLeave_BeforeStart_OK — leave starts in the future → 200 + status=cancelled.
func TestCancelBarberLeave_BeforeStart_OK(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	barber := storage.MakeBarber(t, "b-1", shop.ID, "Tony")

	start := time.Now().Add(2 * time.Hour)
	end := time.Now().Add(8 * time.Hour)
	leave := storage.MakeBarberLeave(t, shop.ID, barber.ID, start, end, storage.LeaveActionCancel)

	ctx := newAPIContext(t, "DELETE", "/api/admin/barber/b-1/leave/"+leave.ID, nil,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", barber.ID),
		withPathParam("leaveID", leave.ID),
	)

	status, respBody := runHandler(t, cancelBarberLeaveHandler, ctx)
	if status != statusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "cancelled") {
		t.Errorf("body should mention 'cancelled', got %q", respBody)
	}

	// Verify DB.
	var got storage.BarberLeave
	if err := storage.DB.First(&got, "id = ?", leave.ID).Error; err != nil {
		t.Fatalf("Find leave: %v", err)
	}
	if got.Status != storage.LeaveStatusCancelled {
		t.Errorf("DB status = %q, want %q", got.Status, storage.LeaveStatusCancelled)
	}
}

// TestCancelBarberLeave_AfterStart_Fails — leave has already started → 500 with the
// "leave has already started" message. (handler doesn't special-case ErrLeaveNotCancellable
// to 409 like the other cancel handler does — that's a future improvement.)
func TestCancelBarberLeave_AfterStart_Fails(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	barber := storage.MakeBarber(t, "b-1", shop.ID, "Tony")

	start := time.Now().Add(-2 * time.Hour) // already started
	end := time.Now().Add(8 * time.Hour)
	leave := storage.MakeBarberLeave(t, shop.ID, barber.ID, start, end, storage.LeaveActionCancel)

	ctx := newAPIContext(t, "DELETE", "/api/admin/barber/b-1/leave/"+leave.ID, nil,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", barber.ID),
		withPathParam("leaveID", leave.ID),
	)

	status, respBody := runHandler(t, cancelBarberLeaveHandler, ctx)
	if status != 500 {
		t.Fatalf("status = %d, want 500; body=%s", status, respBody)
	}
	if !strings.Contains(respBody, "cannot cancel") && !strings.Contains(respBody, "不能撤销") {
		t.Errorf("body should mention 'cannot cancel' / '不能撤销', got %q", respBody)
	}
}

// =====================================================================
// listBarberLeavesHandler  — GET /api/admin/barber/:id/leaves  (PRD §11.7 P4)
//
// Filters out leaves that don't belong to the claims shop — even if storage returns them.
// =====================================================================

// TestListBarberLeaves_NoClaims → 401.
func TestListBarberLeaves_NoClaims(t *testing.T) {
	ctx := newAPIContext(t, "GET", "/api/admin/barber/b-1/leaves", nil,
		withPathParam("id", "b-1"),
	)

	status, _ := runHandler(t, listBarberLeavesHandler, ctx)
	if status != statusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

// TestListBarberLeaves_FiltersByShopAndLimit — only leaves matching claims shop appear,
// and ?limit=N is respected.
func TestListBarberLeaves_FiltersByShopAndLimit(t *testing.T) {
	setupAPITestDB(t)
	shopA := storage.MakeShop(t, "shop-A", "")
	shopB := storage.MakeShop(t, "shop-B", "")
	barberA := storage.MakeBarber(t, "b-A", shopA.ID, "Tony")

	start := time.Now().Add(2 * time.Hour)
	end := time.Now().Add(6 * time.Hour)

	// Shop A: 3 leaves for barberA
	_ = storage.MakeBarberLeave(t, shopA.ID, barberA.ID, start, end, storage.LeaveActionCancel)
	_ = storage.MakeBarberLeave(t, shopA.ID, barberA.ID, start.AddDate(0, 0, 1), end.AddDate(0, 0, 1), storage.LeaveActionCancel)
	_ = storage.MakeBarberLeave(t, shopA.ID, barberA.ID, start.AddDate(0, 0, 2), end.AddDate(0, 0, 2), storage.LeaveActionCancel)
	// Shop B: 1 leave for barberA (storage.ListBarberLeaves returns by barber, not by shop)
	_ = storage.MakeBarberLeave(t, shopB.ID, barberA.ID, start.AddDate(0, 0, 3), end.AddDate(0, 0, 3), storage.LeaveActionCancel)

	// Claims=shop-A should see only 3 leaves for barberA.
	ctx := newAPIContext(t, "GET", "/api/admin/barber/b-A/leaves", nil,
		withClaims(adminClaims(shopA.ID)),
		withPathParam("id", barberA.ID),
	)
	status, body := runHandler(t, listBarberLeavesHandler, ctx)
	if status != statusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var list []storage.BarberLeave
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	if len(list) != 3 {
		t.Errorf("len(list) = %d, want 3 (3 shop-A leaves for barberA); body=%s", len(list), body)
	}
	for i, l := range list {
		if l.ShopID != shopA.ID {
			t.Errorf("list[%d].ShopID = %q, want %q (cross-shop leak)", i, l.ShopID, shopA.ID)
		}
	}
}

// TestListBarberLeaves_LimitQuery — ?limit=N caps the result count.
func TestListBarberLeaves_LimitQuery(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	barber := storage.MakeBarber(t, "b-1", shop.ID, "Tony")

	start := time.Now().Add(2 * time.Hour)
	end := time.Now().Add(6 * time.Hour)
	for i := 0; i < 5; i++ {
		_ = storage.MakeBarberLeave(t, shop.ID, barber.ID,
			start.AddDate(0, 0, i), end.AddDate(0, 0, i), storage.LeaveActionCancel)
	}

	ctx := newAPIContext(t, "GET", "/api/admin/barber/b-1/leaves", nil,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", barber.ID),
		withQuery("limit", "2"),
	)
	status, body := runHandler(t, listBarberLeavesHandler, ctx)
	if status != statusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var list []storage.BarberLeave
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len(list) = %d, want 2 (limit=2)", len(list))
	}
}

// TestListBarberLeaves_InvalidLimitFallsBackToDefault — garbage limit falls back to default 50.
func TestListBarberLeaves_InvalidLimitFallsBackToDefault(t *testing.T) {
	setupAPITestDB(t)
	shop := storage.MakeShop(t, "shop-A", "")
	barber := storage.MakeBarber(t, "b-1", shop.ID, "Tony")

	start := time.Now().Add(2 * time.Hour)
	end := time.Now().Add(6 * time.Hour)
	_ = storage.MakeBarberLeave(t, shop.ID, barber.ID, start, end, storage.LeaveActionCancel)

	ctx := newAPIContext(t, "GET", "/api/admin/barber/b-1/leaves", nil,
		withClaims(adminClaims(shop.ID)),
		withPathParam("id", barber.ID),
		withQuery("limit", "garbage"),
	)
	status, body := runHandler(t, listBarberLeavesHandler, ctx)
	if status != statusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var list []storage.BarberLeave
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("len(list) = %d, want 1 (we created 1, default limit is 50 so no truncation)", len(list))
	}
}