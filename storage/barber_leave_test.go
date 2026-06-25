package storage

// barber_leave_test.go
//
// P4 理发师请假单测（PRD §11.7 P4）
//
// 覆盖：
//   1. CreateLeave action=cancel → 全部取消（admin 路径，无 penalty）
//   2. CreateLeave action=reschedule 且有空闲理发师 → 改派成功
//   3. CreateLeave action=reschedule 但无空闲理发师 → 兜底取消
//   4. CreateLeave action=reschedule 但本店铺无其他理发师 → 兜底取消
//   5. CreateLeave 区间外的预约不受影响
//   6. CreateLeave 参数校验（空 ID / startAt >= endAt / 非法 action）
//   7. CancelLeave startAt 之前 → OK + 事件埋点
//   8. CancelLeave startAt 之后 → ErrLeaveNotCancellable
//   9. CancelLeave 已 cancelled → 错误
//  10. CancelLeave 不存在 → 错误
//  11. ListActiveLeaves 过滤掉 expired/cancelled
//  12. ListBarberLeaves 按 start_at DESC
//  13. sender 失败 → leave 仍创建成功（事务外通知，不阻塞主链路）
//  14. IsBarberOnLeaveAt：区间内/外/边界/cancelled/expired/其他理发师
//  15. ListBarberLeavesInRange：区间相交 / 不相交 / cancelled 过滤
//
// ID 约定：
//   - MakeBarber(t, "barber-Tony", ...) → barber.ID = "barber-Tony"
//   - MakeAppointment(t, ..., "Tony", ...) → appt.BarberID = "barber-" + "Tony" = "barber-Tony"
//   - 这样 MakeBarber 创建的 ID 与 MakeAppointment 自动生成的 BarberID 保持一致。
//
// Run:
//   go test ./storage/... -v -run "TestLeave\|TestCancelLeave\|TestListLeave\|TestCreateLeave\|TestFindAppointments\|TestIsBarberOnLeaveAt\|TestListBarberLeavesInRange"

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ===================== Helpers =====================

// futureTime 返回相对 now 的未来时间
func futureTime(hoursFromNow float64) time.Time {
	return time.Now().Add(time.Duration(hoursFromNow * float64(time.Hour)))
}

// pastTime 返回相对 now 的过去时间
func pastTime(hoursAgo float64) time.Time {
	return time.Now().Add(-time.Duration(hoursAgo * float64(time.Hour)))
}

// fakeSender 记录所有通知调用，用于断言
type fakeSender struct {
	mu       sync.Mutex
	calls    []sentCall
	failOn   string // customerID 命中则返回 error
	failWith error
}

type sentCall struct {
	CustomerID string
	Text       string
}

func (f *fakeSender) send(_ context.Context, customerID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, sentCall{CustomerID: customerID, Text: text})
	if f.failOn != "" && f.failOn == customerID {
		return f.failWith
	}
	return nil
}

// ===================== CreateLeave: action=cancel =====================

func TestCreateLeave_CancelAction_AllAppointmentsCancelled(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	// 注意：barber ID 用 "barber-Tony"，匹配 MakeAppointment 中 "barber-"+name 的约定
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust1 := MakeCustomer(t, "Alice", 0, 0)
	cust2 := MakeCustomer(t, "Bob", 0, 0)
	// 两个 Tony 的未来预约（在请假区间内）
	date1, tm1 := buildApptTime(t, 2) // 2h ahead
	date2, tm2 := buildApptTime(t, 3) // 3h ahead
	appt1 := MakeAppointment(t, shop.ID, cust1.ID, "Alice", barberA.Name, date1, tm1)
	appt2 := MakeAppointment(t, shop.ID, cust2.ID, "Bob", barberA.Name, date2, tm2)

	sender := &fakeSender{}
	leave := BarberLeave{
		ShopID:    shop.ID,
		BarberID:  barberA.ID,
		StartAt:   futureTime(1),
		EndAt:     futureTime(4),
		Reason:    "生病",
		Action:    LeaveActionCancel,
		CreatedBy: "test_admin",
	}
	res, err := CreateBarberLeave(WithCtx(), leave, sender.send)
	if err != nil {
		t.Fatalf("CreateBarberLeave: %v", err)
	}
	if res.AffectedCount != 2 {
		t.Errorf("AffectedCount = %d, want 2", res.AffectedCount)
	}
	if res.CancelledCount != 2 {
		t.Errorf("CancelledCount = %d, want 2", res.CancelledCount)
	}
	if res.RescheduledCount != 0 {
		t.Errorf("RescheduledCount = %d, want 0", res.RescheduledCount)
	}
	if len(res.NotifiedCustomers) != 2 {
		t.Errorf("NotifiedCustomers = %d, want 2", len(res.NotifiedCustomers))
	}

	// 两条预约都应该是 cancelled + admin_cancel
	for _, apptID := range []string{appt1.ID, appt2.ID} {
		var got Appointment
		DB.First(&got, "id = ?", apptID)
		if got.Status != "cancelled" {
			t.Errorf("appt %s Status = %q, want cancelled", apptID, got.Status)
		}
		if got.CancelType != CancelTypeAdmin {
			t.Errorf("appt %s CancelType = %q, want %q", apptID, got.CancelType, CancelTypeAdmin)
		}
		if !strings.Contains(got.CancelReason, "理发师请假") {
			t.Errorf("appt %s CancelReason = %q, want to mention 请假", apptID, got.CancelReason)
		}
	}

	// 顾客 penalty 不应增加
	for _, c := range []*Customer{cust1, cust2} {
		var got Customer
		DB.First(&got, "id = ?", c.ID)
		if got.LateCancelCount != 0 {
			t.Errorf("customer %s LateCancelCount = %d, want 0 (admin cancel 不计 penalty)", c.ID, got.LateCancelCount)
		}
	}

	// barber_leaves 行存在 + 统计字段正确
	var gotLeave BarberLeave
	DB.First(&gotLeave, "id = ?", res.LeaveID)
	if gotLeave.Status != LeaveStatusActive {
		t.Errorf("Leave Status = %q, want active", gotLeave.Status)
	}
	if gotLeave.AffectedCount != 2 || gotLeave.CancelledCount != 2 {
		t.Errorf("Leave stats wrong: affected=%d cancelled=%d", gotLeave.AffectedCount, gotLeave.CancelledCount)
	}

	// EventBarberLeaveCreated 事件已埋点
	var evts []EventLog
	DB.Where("event_type = ? AND ref_id = ?", EventBarberLeaveCreated, res.LeaveID).Find(&evts)
	if len(evts) != 1 {
		t.Errorf("expected 1 barber_leave_created event, got %d", len(evts))
	}

	// sender 被调用 2 次
	if len(sender.calls) != 2 {
		t.Errorf("sender.calls = %d, want 2", len(sender.calls))
	}
}

func TestCreateLeave_CancelAction_OutOfRangeNotAffected(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := MakeCustomer(t, "Alice", 0, 0)
	// 一个在区间外（5h 后，超出 endAt=4h）
	dateOut, tmOut := buildApptTime(t, 5)
	MakeAppointment(t, shop.ID, cust.ID, "Alice", barberA.Name, dateOut, tmOut)

	sender := &fakeSender{}
	leave := BarberLeave{
		ShopID: shop.ID, BarberID: barberA.ID,
		StartAt: futureTime(1), EndAt: futureTime(4),
		Action: LeaveActionCancel,
	}
	res, err := CreateBarberLeave(WithCtx(), leave, sender.send)
	if err != nil {
		t.Fatalf("CreateBarberLeave: %v", err)
	}
	if res.AffectedCount != 0 {
		t.Errorf("AffectedCount = %d, want 0 (预约在区间外)", res.AffectedCount)
	}
	if len(sender.calls) != 0 {
		t.Errorf("sender.calls = %d, want 0", len(sender.calls))
	}
}

// ===================== CreateLeave: action=reschedule =====================

func TestCreateLeave_RescheduleAction_FindsAlternativeBarber(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	barberB := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")
	cust := MakeCustomer(t, "Alice", 0, 0)

	date, tm := buildApptTime(t, 2) // 区间内
	appt := MakeAppointment(t, shop.ID, cust.ID, "Alice", barberA.Name, date, tm)

	sender := &fakeSender{}
	leave := BarberLeave{
		ShopID: shop.ID, BarberID: barberA.ID,
		StartAt: futureTime(1), EndAt: futureTime(4),
		Action: LeaveActionReschedule,
	}
	res, err := CreateBarberLeave(WithCtx(), leave, sender.send)
	if err != nil {
		t.Fatalf("CreateBarberLeave: %v", err)
	}
	if res.RescheduledCount != 1 {
		t.Errorf("RescheduledCount = %d, want 1", res.RescheduledCount)
	}
	if res.CancelledCount != 0 {
		t.Errorf("CancelledCount = %d, want 0 (成功改派)", res.CancelledCount)
	}

	// appt 状态仍为 active，但 barber_id / barber_name 改了
	var got Appointment
	DB.First(&got, "id = ?", appt.ID)
	if got.Status != "active" {
		t.Errorf("Status = %q, want active (reschedule 不取消)", got.Status)
	}
	if got.BarberID != barberB.ID {
		t.Errorf("BarberID = %q, want %q (改派到 Kevin)", got.BarberID, barberB.ID)
	}
	if got.BarberName != barberB.Name {
		t.Errorf("BarberName = %q, want %q", got.BarberName, barberB.Name)
	}

	// EventAppointmentRescheduled 已埋点
	var evts []EventLog
	DB.Where("event_type = ? AND ref_id = ?", EventAppointmentRescheduled, appt.ID).Find(&evts)
	if len(evts) != 1 {
		t.Errorf("expected 1 appointment_rescheduled event, got %d", len(evts))
	}
}

func TestCreateLeave_RescheduleAction_AllAlternatesBusy_FallbackCancel(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	barberB := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")
	custA := MakeCustomer(t, "Alice", 0, 0)
	custB := MakeCustomer(t, "Bob", 0, 0)

	// 同一天同时段：Kevin 已被 Bob 占了
	date, tm := buildApptTime(t, 2)
	apptA := MakeAppointment(t, shop.ID, custA.ID, "Alice", barberA.Name, date, tm) // 待改派
	_ = MakeAppointment(t, shop.ID, custB.ID, "Bob", barberB.Name, date, tm)        // 占用 Kevin

	sender := &fakeSender{}
	leave := BarberLeave{
		ShopID: shop.ID, BarberID: barberA.ID,
		StartAt: futureTime(1), EndAt: futureTime(4),
		Action: LeaveActionReschedule,
	}
	res, err := CreateBarberLeave(WithCtx(), leave, sender.send)
	if err != nil {
		t.Fatalf("CreateBarberLeave: %v", err)
	}
	if res.RescheduledCount != 0 {
		t.Errorf("RescheduledCount = %d, want 0 (改派失败)", res.RescheduledCount)
	}
	if res.CancelledCount != 1 {
		t.Errorf("CancelledCount = %d, want 1 (兜底取消)", res.CancelledCount)
	}

	// apptA 应被取消（admin 路径）
	var got Appointment
	DB.First(&got, "id = ?", apptA.ID)
	if got.Status != "cancelled" {
		t.Errorf("Status = %q, want cancelled (fallback)", got.Status)
	}
	if got.CancelType != CancelTypeAdmin {
		t.Errorf("CancelType = %q, want %q", got.CancelType, CancelTypeAdmin)
	}
}

func TestCreateLeave_RescheduleAction_NoOtherBarber_FallbackCancel(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	// 本店只有 Tony
	cust := MakeCustomer(t, "Alice", 0, 0)
	date, tm := buildApptTime(t, 2)
	appt := MakeAppointment(t, shop.ID, cust.ID, "Alice", barberA.Name, date, tm)

	sender := &fakeSender{}
	leave := BarberLeave{
		ShopID: shop.ID, BarberID: barberA.ID,
		StartAt: futureTime(1), EndAt: futureTime(4),
		Action: LeaveActionReschedule,
	}
	res, err := CreateBarberLeave(WithCtx(), leave, sender.send)
	if err != nil {
		t.Fatalf("CreateBarberLeave: %v", err)
	}
	if res.CancelledCount != 1 {
		t.Errorf("CancelledCount = %d, want 1", res.CancelledCount)
	}
	_ = appt
}

// ===================== CreateLeave: parameter validation =====================

func TestCreateLeave_InvalidParams(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	cases := []struct {
		name  string
		leave BarberLeave
		want  string
	}{
		{
			name: "empty barber_id",
			leave: BarberLeave{ShopID: shop.ID, StartAt: futureTime(1), EndAt: futureTime(2), Action: LeaveActionCancel},
			want: "barber_id",
		},
		{
			name: "empty shop_id",
			leave: BarberLeave{BarberID: barberA.ID, StartAt: futureTime(1), EndAt: futureTime(2), Action: LeaveActionCancel},
			want: "shop_id",
		},
		{
			name: "start_at >= end_at",
			leave: BarberLeave{ShopID: shop.ID, BarberID: barberA.ID, StartAt: futureTime(3), EndAt: futureTime(1), Action: LeaveActionCancel},
			want: "start_at",
		},
		{
			name: "invalid action",
			leave: BarberLeave{ShopID: shop.ID, BarberID: barberA.ID, StartAt: futureTime(1), EndAt: futureTime(2), Action: "unknown"},
			want: "action",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CreateBarberLeave(WithCtx(), tc.leave, nil)
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want contains %q", err.Error(), tc.want)
			}
		})
	}
}

func TestCreateLeave_BarberNotFound(t *testing.T) {
	SetupTestDB(t)
	leave := BarberLeave{
		ShopID: "shop-1", BarberID: "nonexistent",
		StartAt: futureTime(1), EndAt: futureTime(2),
		Action: LeaveActionCancel,
	}
	_, err := CreateBarberLeave(WithCtx(), leave, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent barber")
	}
	if !strings.Contains(err.Error(), "理发师") {
		t.Errorf("error should mention 理发师, got %q", err.Error())
	}
}

// ===================== CancelLeave =====================

func TestCancelLeave_BeforeStart_OK(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	leave := MakeBarberLeave(t, shop.ID, barberA.ID, futureTime(2), futureTime(4), LeaveActionCancel)

	got, err := CancelBarberLeave(WithCtx(), leave.ID, "test_admin")
	if err != nil {
		t.Fatalf("CancelBarberLeave: %v", err)
	}
	if got.Status != LeaveStatusCancelled {
		t.Errorf("Status = %q, want cancelled", got.Status)
	}

	// 事件埋点
	var evts []EventLog
	DB.Where("event_type = ? AND ref_id = ?", EventBarberLeaveCancelled, leave.ID).Find(&evts)
	if len(evts) != 1 {
		t.Errorf("expected 1 barber_leave_cancelled event, got %d", len(evts))
	}
}

func TestCancelLeave_AfterStart_Fails(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	// startAt 已过 1 小时
	leave := MakeBarberLeave(t, shop.ID, barberA.ID, pastTime(1), futureTime(2), LeaveActionCancel)

	_, err := CancelBarberLeave(WithCtx(), leave.ID, "test_admin")
	if !errors.Is(err, ErrLeaveNotCancellable) {
		t.Fatalf("expected ErrLeaveNotCancellable, got %v", err)
	}
}

func TestCancelLeave_AlreadyCancelled_Fails(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	leave := MakeBarberLeave(t, shop.ID, barberA.ID, futureTime(2), futureTime(4), LeaveActionCancel)

	if _, err := CancelBarberLeave(WithCtx(), leave.ID, "op"); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	_, err := CancelBarberLeave(WithCtx(), leave.ID, "op")
	if err == nil {
		t.Fatal("expected error on second cancel")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("error should mention cancelled, got %q", err.Error())
	}
}

func TestCancelLeave_NotFound(t *testing.T) {
	SetupTestDB(t)
	_, err := CancelBarberLeave(WithCtx(), "nonexistent", "op")
	if err == nil {
		t.Fatal("expected error for nonexistent leave")
	}
}

// ===================== ListLeaves =====================

func TestListActiveLeaves_FiltersExpiredAndCancelled(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	barberB := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")

	// 1) 未来 active（应被列出）
	MakeBarberLeave(t, shop.ID, barberA.ID, futureTime(2), futureTime(4), LeaveActionCancel)
	// 2) 已过期（endAt 在过去 → 应被过滤）
	MakeBarberLeave(t, shop.ID, barberA.ID, pastTime(3), pastTime(1), LeaveActionCancel)
	// 3) 已 cancelled（应被过滤）
	cancelled := MakeBarberLeave(t, shop.ID, barberB.ID, futureTime(2), futureTime(4), LeaveActionCancel)
	cancelled.Status = LeaveStatusCancelled
	DB.Save(cancelled)

	active, err := ListActiveLeaves(WithCtx(), shop.ID)
	if err != nil {
		t.Fatalf("ListActiveLeaves: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("len(active) = %d, want 1 (only future+active)", len(active))
	}
}

func TestListBarberLeaves_OrdersByStartDesc(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	l1 := MakeBarberLeave(t, shop.ID, barberA.ID, futureTime(10), futureTime(11), LeaveActionCancel)
	l2 := MakeBarberLeave(t, shop.ID, barberA.ID, futureTime(2), futureTime(3), LeaveActionCancel)
	l3 := MakeBarberLeave(t, shop.ID, barberA.ID, futureTime(20), futureTime(21), LeaveActionCancel)

	leaves, err := ListBarberLeaves(WithCtx(), barberA.ID, 0)
	if err != nil {
		t.Fatalf("ListBarberLeaves: %v", err)
	}
	if len(leaves) != 3 {
		t.Fatalf("len = %d, want 3", len(leaves))
	}
	// 按 start_at DESC：l3 > l1 > l2
	if leaves[0].ID != l3.ID || leaves[1].ID != l1.ID || leaves[2].ID != l2.ID {
		t.Errorf("order wrong: got [%s, %s, %s], want [%s, %s, %s]",
			leaves[0].ID, leaves[1].ID, leaves[2].ID,
			l3.ID, l1.ID, l2.ID)
	}
}

// ===================== Sender failure handling =====================

func TestCreateLeave_SenderFailure_LeaveStillCreated(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := MakeCustomer(t, "Alice", 0, 0)
	date, tm := buildApptTime(t, 2)
	MakeAppointment(t, shop.ID, cust.ID, "Alice", barberA.Name, date, tm)

	// sender 对该顾客通知失败
	sender := &fakeSender{
		failOn:   cust.ID,
		failWith: errors.New("wechat api down"),
	}
	leave := BarberLeave{
		ShopID: shop.ID, BarberID: barberA.ID,
		StartAt: futureTime(1), EndAt: futureTime(4),
		Action: LeaveActionCancel,
	}
	res, err := CreateBarberLeave(WithCtx(), leave, sender.send)
	if err != nil {
		t.Fatalf("CreateBarberLeave should not fail when sender errors: %v", err)
	}
	if res.CancelledCount != 1 {
		t.Errorf("CancelledCount = %d, want 1", res.CancelledCount)
	}
	if len(res.NotifiedCustomers) != 0 {
		t.Errorf("NotifiedCustomers = %d, want 0 (sender 全失败)", len(res.NotifiedCustomers))
	}

	// leave row 仍然存在
	var gotLeave BarberLeave
	DB.First(&gotLeave, "id = ?", res.LeaveID)
	if gotLeave.CancelledCount != 1 {
		t.Errorf("leave.CancelledCount = %d, want 1", gotLeave.CancelledCount)
	}
}

// ===================== FindAppointmentsInRange =====================

func TestFindAppointmentsInRange(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := MakeCustomer(t, "Alice", 0, 0)

	// 区间 [1h, 4h] 内一个，外面两个
	dateIn, tmIn := buildApptTime(t, 2)
	dateBefore, tmBefore := buildApptTime(t, 0.5)
	dateAfter, tmAfter := buildApptTime(t, 6)
	inAppt := MakeAppointment(t, shop.ID, cust.ID, "Alice", barberA.Name, dateIn, tmIn)
	MakeAppointment(t, shop.ID, cust.ID, "Alice", barberA.Name, dateBefore, tmBefore)
	MakeAppointment(t, shop.ID, cust.ID, "Alice", barberA.Name, dateAfter, tmAfter)

	appts, err := FindAppointmentsInRange(WithCtx(), barberA.ID, futureTime(1), futureTime(4))
	if err != nil {
		t.Fatalf("FindAppointmentsInRange: %v", err)
	}
	if len(appts) != 1 || appts[0].ID != inAppt.ID {
		t.Errorf("got %d appts, want 1 with id=%s", len(appts), inAppt.ID)
	}
}

// ===================== ID generation =====================

func TestCreateLeave_GeneratesIDAndBarberName(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	leave := BarberLeave{
		ShopID: shop.ID, BarberID: barberA.ID,
		StartAt: futureTime(1), EndAt: futureTime(2),
		Action: LeaveActionCancel,
	}
	res, err := CreateBarberLeave(WithCtx(), leave, nil)
	if err != nil {
		t.Fatalf("CreateBarberLeave: %v", err)
	}
	if res.LeaveID == "" {
		t.Error("LeaveID should be generated")
	}
	if _, err := uuid.Parse(res.LeaveID); err != nil {
		t.Errorf("LeaveID should be a valid UUID, got %q", res.LeaveID)
	}

	var got BarberLeave
	DB.First(&got, "id = ?", res.LeaveID)
	if got.BarberName != barberA.Name {
		t.Errorf("BarberName = %q, want %q", got.BarberName, barberA.Name)
	}
}

// ===================== IsBarberOnLeaveAt =====================
//
// 用于 tools/create_appointment.go 在顾客下单前检查"理发师今天是否有事"。

func TestIsBarberOnLeaveAt_InsideWindow(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	start := futureTime(1) // +1h
	end := futureTime(3)   // +3h
	mid := futureTime(2)   // +2h

	MakeBarberLeave(t, shop.ID, barber.ID, start, end, LeaveActionCancel)

	onLeave, got, err := IsBarberOnLeaveAt(WithCtx(), barber.ID, mid)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !onLeave {
		t.Fatal("should be on leave at mid time")
	}
	if got == nil || got.BarberID != barber.ID {
		t.Errorf("got leave = %+v, want BarberID=%s", got, barber.ID)
	}
}

func TestIsBarberOnLeaveAt_BoundaryStart(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	start := futureTime(1)
	end := futureTime(3)
	// 边界：start_at 正好命中 → 应判为请假中（含端点）
	MakeBarberLeave(t, shop.ID, barber.ID, start, end, LeaveActionCancel)

	onLeave, _, err := IsBarberOnLeaveAt(WithCtx(), barber.ID, start)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !onLeave {
		t.Error("at start_at boundary should be on leave (inclusive)")
	}
}

func TestIsBarberOnLeaveAt_BoundaryEnd(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	start := futureTime(1)
	end := futureTime(3)
	// 边界：end_at 正好命中 → 应判为请假中（含端点）
	MakeBarberLeave(t, shop.ID, barber.ID, start, end, LeaveActionCancel)

	onLeave, _, err := IsBarberOnLeaveAt(WithCtx(), barber.ID, end)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !onLeave {
		t.Error("at end_at boundary should be on leave (inclusive)")
	}
}

func TestIsBarberOnLeaveAt_BeforeWindow(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	start := futureTime(2)
	end := futureTime(4)
	before := futureTime(1)

	MakeBarberLeave(t, shop.ID, barber.ID, start, end, LeaveActionCancel)

	onLeave, _, err := IsBarberOnLeaveAt(WithCtx(), barber.ID, before)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if onLeave {
		t.Error("before start_at should NOT be on leave")
	}
}

func TestIsBarberOnLeaveAt_AfterWindow(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	start := futureTime(1)
	end := futureTime(2)
	after := futureTime(3)

	MakeBarberLeave(t, shop.ID, barber.ID, start, end, LeaveActionCancel)

	onLeave, _, err := IsBarberOnLeaveAt(WithCtx(), barber.ID, after)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if onLeave {
		t.Error("after end_at should NOT be on leave")
	}
}

func TestIsBarberOnLeaveAt_CancelledLeaveIgnored(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	start := futureTime(1)
	end := futureTime(3)

	leave := MakeBarberLeave(t, shop.ID, barber.ID, start, end, LeaveActionCancel)
	// 商户主动撤销
	if _, err := CancelBarberLeave(WithCtx(), leave.ID, "admin"); err != nil {
		t.Fatalf("CancelBarberLeave: %v", err)
	}

	onLeave, _, err := IsBarberOnLeaveAt(WithCtx(), barber.ID, futureTime(2))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if onLeave {
		t.Error("cancelled leave should NOT count")
	}
}

func TestIsBarberOnLeaveAt_OtherBarberNotAffected(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	barberB := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")

	start := futureTime(1)
	end := futureTime(3)

	MakeBarberLeave(t, shop.ID, barberA.ID, start, end, LeaveActionCancel)

	onLeave, _, err := IsBarberOnLeaveAt(WithCtx(), barberB.ID, futureTime(2))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if onLeave {
		t.Error("Kevin should not be on leave when only Tony is")
	}
}

func TestIsBarberOnLeaveAt_NoLeave(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	onLeave, got, err := IsBarberOnLeaveAt(WithCtx(), barber.ID, time.Now())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if onLeave || got != nil {
		t.Errorf("no leave should return (false, nil, nil), got (%v, %v)", onLeave, got)
	}
}

// ===================== ListBarberLeavesInRange =====================

func TestListBarberLeavesInRange_OverlapOnly(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	// 三段请假：
	//   #1  [-3h, -2h]  → 不重叠（查询范围 [0, +10h]）
	//   #2  [+1h, +2h]  → 重叠
	//   #3  [+5h, +6h]  → 重叠
	MakeBarberLeave(t, shop.ID, barber.ID, futureTime(-3), futureTime(-2), LeaveActionCancel)
	MakeBarberLeave(t, shop.ID, barber.ID, futureTime(1), futureTime(2), LeaveActionCancel)
	MakeBarberLeave(t, shop.ID, barber.ID, futureTime(5), futureTime(6), LeaveActionCancel)

	got, err := ListBarberLeavesInRange(WithCtx(), barber.ID, futureTime(0), futureTime(10))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d leaves, want 2 (#2 + #3)", len(got))
	}
}

func TestListBarberLeavesInRange_EmptyRange(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	MakeBarberLeave(t, shop.ID, barber.ID, futureTime(1), futureTime(2), LeaveActionCancel)

	// 查询过去区间
	got, err := ListBarberLeavesInRange(WithCtx(), barber.ID, futureTime(-10), futureTime(-5))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}

func TestListBarberLeavesInRange_FilterCancelled(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	l1 := MakeBarberLeave(t, shop.ID, barber.ID, futureTime(1), futureTime(2), LeaveActionCancel)
	l2 := MakeBarberLeave(t, shop.ID, barber.ID, futureTime(3), futureTime(4), LeaveActionCancel)
	if _, err := CancelBarberLeave(WithCtx(), l1.ID, "admin"); err != nil {
		t.Fatalf("CancelBarberLeave: %v", err)
	}
	_ = l2

	got, err := ListBarberLeavesInRange(WithCtx(), barber.ID, futureTime(0), futureTime(10))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d, want 1 (only l2)", len(got))
	}
}

func TestListBarberLeavesInRange_OtherBarberNotIncluded(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	barberB := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")

	MakeBarberLeave(t, shop.ID, barberA.ID, futureTime(1), futureTime(3), LeaveActionCancel)
	MakeBarberLeave(t, shop.ID, barberB.ID, futureTime(1), futureTime(3), LeaveActionCancel)

	got, err := ListBarberLeavesInRange(WithCtx(), barberA.ID, futureTime(0), futureTime(10))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d, want 1 (only Tony's leave)", len(got))
	}
}

// ===================== ExpireOverdueLeaves（PRD §11.7.8 cron 兜底） =====================

// TestExpireOverdueLeaves_ExpiresPastEndAt 校验 happy path：
//   - 2 条已过期 active → expire
//   - 1 条未来 active → 保持
//   - 1 条 cancelled → 不动
func TestExpireOverdueLeaves_ExpiresPastEndAt(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	// 2 条已过期
	past1 := MakeBarberLeave(t, shop.ID, barber.ID, pastTime(2), pastTime(1), LeaveActionCancel)
	past2 := MakeBarberLeave(t, shop.ID, barber.ID, pastTime(5), pastTime(3), LeaveActionReschedule)
	// 1 条未来
	future := MakeBarberLeave(t, shop.ID, barber.ID, futureTime(1), futureTime(2), LeaveActionCancel)
	// 1 条 cancelled（手动改 status）
	cancelled := MakeBarberLeave(t, shop.ID, barber.ID, pastTime(1), pastTime(0.5), LeaveActionCancel)
	if err := DB.Model(cancelled).Update("status", LeaveStatusCancelled).Error; err != nil {
		t.Fatalf("update cancelled: %v", err)
	}

	n, err := ExpireOverdueLeaves(WithCtx(), time.Now())
	if err != nil {
		t.Fatalf("ExpireOverdueLeaves: %v", err)
	}
	if n != 2 {
		t.Errorf("expired count = %d, want 2", n)
	}

	// 校验每条状态
	checkStatus := func(leaveID, want string) {
		var got BarberLeave
		if err := DB.Where("id = ?", leaveID).First(&got).Error; err != nil {
			t.Fatalf("query %s: %v", leaveID, err)
		}
		if got.Status != want {
			t.Errorf("leave %s status = %q, want %q", leaveID, got.Status, want)
		}
	}
	checkStatus(past1.ID, LeaveStatusExpired)
	checkStatus(past2.ID, LeaveStatusExpired)
	checkStatus(future.ID, LeaveStatusActive)
	checkStatus(cancelled.ID, LeaveStatusCancelled)
}

// TestExpireOverdueLeaves_NoOpWhenNothingPast 全部未来 → 0 expired
func TestExpireOverdueLeaves_NoOpWhenNothingPast(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	MakeBarberLeave(t, shop.ID, barber.ID, futureTime(1), futureTime(2), LeaveActionCancel)
	MakeBarberLeave(t, shop.ID, barber.ID, futureTime(3), futureTime(5), LeaveActionReschedule)

	n, err := ExpireOverdueLeaves(WithCtx(), time.Now())
	if err != nil {
		t.Fatalf("ExpireOverdueLeaves: %v", err)
	}
	if n != 0 {
		t.Errorf("expired count = %d, want 0", n)
	}
}

// TestExpireOverdueLeaves_Idempotent 同一 now 跑两次：第二次应该 no-op（全部已 expired）
func TestExpireOverdueLeaves_Idempotent(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	MakeBarberLeave(t, shop.ID, barber.ID, pastTime(2), pastTime(1), LeaveActionCancel)

	n1, err := ExpireOverdueLeaves(WithCtx(), time.Now())
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if n1 != 1 {
		t.Errorf("first expired = %d, want 1", n1)
	}

	n2, err := ExpireOverdueLeaves(WithCtx(), time.Now())
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second expired = %d, want 0 (idempotent)", n2)
	}
}

// TestExpireOverdueLeaves_Boundary 边界：end_at == now 应被过期（end_at < now 不含等号，但下一分钟 cron 就过期了）
//
// 这个 case 用来证明我们用 < 不会过早；now 向前推 1ms 时，end_at > now 仍是 active。
func TestExpireOverdueLeaves_Boundary(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	now := time.Now()
	// end_at 恰好等于 now → not expired（用 < 过滤）
	l := MakeBarberLeave(t, shop.ID, barber.ID, now.Add(-time.Hour), now, LeaveActionCancel)

	n, err := ExpireOverdueLeaves(WithCtx(), now)
	if err != nil {
		t.Fatalf("ExpireOverdueLeaves: %v", err)
	}
	if n != 0 {
		t.Errorf("expired = %d, want 0 (end_at == now, not < now)", n)
	}

	// now 推后 1ms → end_at < now 成立
	n2, err := ExpireOverdueLeaves(WithCtx(), now.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("ExpireOverdueLeaves (now+1ms): %v", err)
	}
	if n2 != 1 {
		t.Errorf("expired (now+1ms) = %d, want 1", n2)
	}
	_ = l
}

// TestExpireOverdueLeaves_WritesExpiredEvent 校验 barber_leave_expired 事件被正确写入
func TestExpireOverdueLeaves_WritesExpiredEvent(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	leave := MakeBarberLeave(t, shop.ID, barber.ID, pastTime(2), pastTime(1), LeaveActionCancel)

	if _, err := ExpireOverdueLeaves(WithCtx(), time.Now()); err != nil {
		t.Fatalf("ExpireOverdueLeaves: %v", err)
	}

	var ev EventLog
	if err := DB.Where("ref_id = ? AND event_type = ?", leave.ID, EventBarberLeaveExpired).
		First(&ev).Error; err != nil {
		t.Fatalf("query event: %v", err)
	}
	if ev.ShopID != shop.ID {
		t.Errorf("event shop_id = %q, want %q", ev.ShopID, shop.ID)
	}
	if ev.EventType != EventBarberLeaveExpired {
		t.Errorf("event type = %q, want %q", ev.EventType, EventBarberLeaveExpired)
	}
	if !strings.Contains(ev.Meta, "barber-Tony") {
		t.Errorf("event meta missing barber_id: %s", ev.Meta)
	}
	if !strings.Contains(ev.Meta, LeaveStatusExpired) && !strings.Contains(ev.Meta, "expired_at") {
		// meta 至少应包含 expired_at 字段
		t.Errorf("event meta missing expired_at: %s", ev.Meta)
	}
}

// TestExpireOverdueLeaves_DBNotInitialized DB=nil 时 no-op
func TestExpireOverdueLeaves_DBNotInitialized(t *testing.T) {
	DB = nil
	defer func() { DB = nil }() // 保证不污染其他测试
	n, err := ExpireOverdueLeaves(WithCtx(), time.Now())
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if n != 0 {
		t.Errorf("expired = %d, want 0", n)
	}
}

// ===================== v3.7 改派策略升级：findAlternateBarber 三档分级 =====================
//
// PRD §11.7.11 v3.7：
//   - 第一档：Skills 包含 appt.Service（真会这门手艺）
//   - 第二档：Skills 为空（未填写）—— 视作"全能"兜底
//   - 第三档：忽略 Skills 匹配，取任何 active 且时段空闲（保底可用性）
//
// 本组测试直击 findAlternateBarber 内部逻辑（绕过 CreateLeave），断言三档优先级
// 和边界（busy / 排除原 barber / name asc 同档排序）。

// setBarberSkills 直接更新 Barber.Skills（MakeBarber 没暴露该字段）
func setBarberSkills(t *testing.T, barberID, skills string) {
	t.Helper()
	if err := DB.Model(&Barber{}).Where("id = ?", barberID).Update("skills", skills).Error; err != nil {
		t.Fatalf("update skills: %v", err)
	}
}

// ---- skillContains 纯函数测试（不需要 DB）----

func TestSkillContains_ExactMatch(t *testing.T) {
	if !skillContains("剪发,染发", "染发") {
		t.Error("expected match")
	}
	if !skillContains("剪发,染发", "剪发") {
		t.Error("expected match for first item")
	}
}

func TestSkillContains_TrimSpace(t *testing.T) {
	// skills 字符串容忍空格
	if !skillContains("剪发, 染发 , 烫发", "染发") {
		t.Error("expected match even with spaces around items")
	}
	if !skillContains("剪发,染发,烫发", "烫发") {
		t.Error("last item without trailing space should match")
	}
	// 注意：当前实现只 TrimSpace skills 侧的单项，不 TrimSpace needle 侧
	// 这是设计选择：调用方负责传干净的 needle（appointment.Service 是 DB 里存的字面值）
	if skillContains("剪发,染发", "  染发  ") {
		t.Error("needle is NOT trimmed (callers should pass clean needle)")
	}
}

func TestSkillContains_NoPartialMatch(t *testing.T) {
	// "染" 是 "染发" 的子串，但不应匹配（精确匹配单项）
	if skillContains("剪发,染发", "染") {
		t.Error("partial substring match should NOT count")
	}
	if skillContains("剪发,染发", "烫") {
		t.Error("non-existent skill should NOT match")
	}
}

func TestSkillContains_EmptyNeedleReturnsFalse(t *testing.T) {
	if skillContains("剪发,染发", "") {
		t.Error("empty needle should return false (avoid matching all)")
	}
}

func TestSkillContains_EmptySkills(t *testing.T) {
	if skillContains("", "染发") {
		t.Error("empty skills + non-empty needle should return false")
	}
	if skillContains("", "") {
		t.Error("both empty should return false")
	}
}

func TestSkillContains_SingleSkill(t *testing.T) {
	if !skillContains("剪发", "剪发") {
		t.Error("single skill exact match should return true")
	}
}

// ---- findAlternateBarber 三档分级测试（需要 DB）----

func TestFindAlternateBarber_Tier1_SkillsMatch_PreferredOverEmpty(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	// Kevin 会染发 → 第一档命中
	kevin := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")
	setBarberSkills(t, kevin.ID, "剪发,染发")
	// Bob Skills 为空 → 第二档兜底
	bob := MakeBarber(t, "barber-Bob", shop.ID, "Bob")
	setBarberSkills(t, bob.ID, "")

	date, tm := buildApptTime(t, 2)
	appt := &Appointment{
		ID: "appt-1", ShopID: shop.ID,
		BarberID: "barber-Tony", BarberName: "Tony",
		Date: date, Time: tm, Service: "染发", Status: "active",
	}

	got, found, err := findAlternateBarber(WithCtx(), DB, appt)
	if err != nil {
		t.Fatalf("findAlternateBarber: %v", err)
	}
	if !found {
		t.Fatal("expected to find alternate")
	}
	if got != kevin.ID {
		t.Errorf("got = %q, want %q (Tier1 Skills 匹配优先)", got, kevin.ID)
	}
	if got == bob.ID {
		t.Errorf("不应该选 Bob (Tier2 空 Skills 兜底被 Tier1 压制)")
	}
}

func TestFindAlternateBarber_Tier2_EmptySkills_WhenNoMatch(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	// Kevin 不会染发 → 跳过 Tier1
	kevin := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")
	setBarberSkills(t, kevin.ID, "剪发,烫发")
	// Bob Skills 为空 → Tier2 兜底命中
	bob := MakeBarber(t, "barber-Bob", shop.ID, "Bob")
	setBarberSkills(t, bob.ID, "")

	date, tm := buildApptTime(t, 2)
	appt := &Appointment{
		ID: "appt-1", ShopID: shop.ID,
		BarberID: "barber-Tony", BarberName: "Tony",
		Date: date, Time: tm, Service: "染发", Status: "active",
	}

	got, found, err := findAlternateBarber(WithCtx(), DB, appt)
	if err != nil {
		t.Fatalf("findAlternateBarber: %v", err)
	}
	if !found {
		t.Fatal("expected to find alternate")
	}
	if got != bob.ID {
		t.Errorf("got = %q, want %q (Tier2 空 Skills 兜底)", got, bob.ID)
	}
}

func TestFindAlternateBarber_Tier3_AnyActive_WhenNoMatch_NoEmpty(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	// 全员 Skills 都不匹配，也无空 Skills → 走 Tier3 保底
	// 期望选 Kevin（name asc 靠前于 Tom）
	kevin := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")
	setBarberSkills(t, kevin.ID, "剪发,烫发")
	tom := MakeBarber(t, "barber-Tom", shop.ID, "Tom")
	setBarberSkills(t, tom.ID, "剪发")

	date, tm := buildApptTime(t, 2)
	appt := &Appointment{
		ID: "appt-1", ShopID: shop.ID,
		BarberID: "barber-Tony", BarberName: "Tony",
		Date: date, Time: tm, Service: "染发", Status: "active",
	}

	got, found, err := findAlternateBarber(WithCtx(), DB, appt)
	if err != nil {
		t.Fatalf("findAlternateBarber: %v", err)
	}
	if !found {
		t.Fatal("expected to find alternate via Tier3 fallback")
	}
	if got != kevin.ID {
		t.Errorf("got = %q, want %q (Tier3 任意 active + name asc)", got, kevin.ID)
	}
	if got == tom.ID {
		t.Errorf("name asc 应先 Kevin 后 Tom")
	}
}

func TestFindAlternateBarber_BusyExcluded_AcrossTiers(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := MakeCustomer(t, "Carol", 0, 0)

	date, tm := buildApptTime(t, 2)
	// Kevin 会染发（Tier1），但同时段已被占
	kevin := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")
	setBarberSkills(t, kevin.ID, "剪发,染发")
	_ = MakeAppointment(t, shop.ID, cust.ID, "Carol", "Kevin", date, tm)
	// Bob Skills 为空（Tier2），时段空闲 → 应被选
	bob := MakeBarber(t, "barber-Bob", shop.ID, "Bob")
	setBarberSkills(t, bob.ID, "")

	appt := &Appointment{
		ID: "appt-1", ShopID: shop.ID,
		BarberID: "barber-Tony", BarberName: "Tony",
		Date: date, Time: tm, Service: "染发", Status: "active",
	}

	got, found, err := findAlternateBarber(WithCtx(), DB, appt)
	if err != nil {
		t.Fatalf("findAlternateBarber: %v", err)
	}
	if !found {
		t.Fatal("expected to find alternate")
	}
	if got != bob.ID {
		t.Errorf("got = %q, want %q (Kevin 忙 → 跳过 Tier1 → Bob Tier2)", got, bob.ID)
	}
}

func TestFindAlternateBarber_AllBusy_ReturnsFalse(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	custA := MakeCustomer(t, "Alice", 0, 0)
	custB := MakeCustomer(t, "Bob", 0, 0)

	date, tm := buildApptTime(t, 2)
	kevin := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")
	setBarberSkills(t, kevin.ID, "剪发,染发")
	bob := MakeBarber(t, "barber-Bob", shop.ID, "Bob")
	setBarberSkills(t, bob.ID, "")
	_ = MakeAppointment(t, shop.ID, custA.ID, "Alice", "Kevin", date, tm)
	_ = MakeAppointment(t, shop.ID, custB.ID, "Bob", "Bob", date, tm)

	appt := &Appointment{
		ID: "appt-1", ShopID: shop.ID,
		BarberID: "barber-Tony", BarberName: "Tony",
		Date: date, Time: tm, Service: "染发", Status: "active",
	}

	got, found, err := findAlternateBarber(WithCtx(), DB, appt)
	if err != nil {
		t.Fatalf("findAlternateBarber: %v", err)
	}
	if found {
		t.Errorf("expected not found (all busy), got %q", got)
	}
	if got != "" {
		t.Errorf("got = %q, want empty", got)
	}
}

func TestFindAlternateBarber_ExcludesOriginalBarber(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	// 场景：店铺只有 Tony + Bob 两位，Tony 请假，appt 是 Tony 的
	// 不能选回 Tony
	tony := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	setBarberSkills(t, tony.ID, "剪发,染发")
	bob := MakeBarber(t, "barber-Bob", shop.ID, "Bob")
	setBarberSkills(t, bob.ID, "剪发,染发")

	date, tm := buildApptTime(t, 2)
	appt := &Appointment{
		ID: "appt-1", ShopID: shop.ID,
		BarberID: "barber-Tony", BarberName: "Tony",
		Date: date, Time: tm, Service: "染发", Status: "active",
	}

	got, found, err := findAlternateBarber(WithCtx(), DB, appt)
	if err != nil {
		t.Fatalf("findAlternateBarber: %v", err)
	}
	if !found {
		t.Fatal("expected to find alternate")
	}
	if got == tony.ID {
		t.Errorf("must NOT return the original barber (id=%q)", tony.ID)
	}
	if got != bob.ID {
		t.Errorf("got = %q, want %q", got, bob.ID)
	}
}

func TestFindAlternateBarber_NoOtherBarber_ReturnsFalse(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	date, tm := buildApptTime(t, 2)
	appt := &Appointment{
		ID: "appt-1", ShopID: shop.ID,
		BarberID: "barber-Tony", BarberName: "Tony",
		Date: date, Time: tm, Service: "染发", Status: "active",
	}

	got, found, err := findAlternateBarber(WithCtx(), DB, appt)
	if err != nil {
		t.Fatalf("findAlternateBarber: %v", err)
	}
	if found {
		t.Errorf("expected not found (no other barber), got %q", got)
	}
}

func TestFindAlternateBarber_Tier1_OrderByName(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	// 多个 Tier1 命中（都含"染发"），按 name ASC 选 Adam
	adam := MakeBarber(t, "barber-Adam", shop.ID, "Adam")
	setBarberSkills(t, adam.ID, "剪发,染发")
	zoe := MakeBarber(t, "barber-Zoe", shop.ID, "Zoe")
	setBarberSkills(t, zoe.ID, "剪发,染发")

	date, tm := buildApptTime(t, 2)
	appt := &Appointment{
		ID: "appt-1", ShopID: shop.ID,
		BarberID: "barber-Tony", BarberName: "Tony",
		Date: date, Time: tm, Service: "染发", Status: "active",
	}

	got, found, err := findAlternateBarber(WithCtx(), DB, appt)
	if err != nil {
		t.Fatalf("findAlternateBarber: %v", err)
	}
	if !found {
		t.Fatal("expected to find alternate")
	}
	if got != adam.ID {
		t.Errorf("got = %q, want %q (name asc: Adam 在 Zoe 前)", got, adam.ID)
	}
}

func TestFindAlternateBarber_ServiceEmpty_AllTiersSkipped(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	kevin := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")
	setBarberSkills(t, kevin.ID, "剪发,染发")
	bob := MakeBarber(t, "barber-Bob", shop.ID, "Bob")
	setBarberSkills(t, bob.ID, "")

	date, tm := buildApptTime(t, 2)
	// Service 为空 → skillContains 永远 false → 跳过 Tier1，但 Tier2 (空 Skills) 仍能命中
	appt := &Appointment{
		ID: "appt-1", ShopID: shop.ID,
		BarberID: "barber-Tony", BarberName: "Tony",
		Date: date, Time: tm, Service: "", Status: "active",
	}

	got, found, err := findAlternateBarber(WithCtx(), DB, appt)
	if err != nil {
		t.Fatalf("findAlternateBarber: %v", err)
	}
	if !found {
		t.Fatal("expected Tier2 (empty Skills) to be a fallback")
	}
	if got != bob.ID {
		t.Errorf("got = %q, want %q (Service 空 → Tier2 兜底 Bob)", got, bob.ID)
	}
}// ===================== v4.10 leave notify 升级测试 =====================
//   - 覆盖文案兜底（customer.Name 反查 / CustomerFacingReason 隐私脱敏）
//   - 覆盖 customer_notification 持久化（sent / failed / skipped 三种状态）
//   - 覆盖 text_preview 记录 + leave_no_contact 类型
//
// 注意：这些测试用新签名 LeaveNotificationSender 而不是老 sender.send，
// 走 sendLeaveNotificationsV2 路径。

// fakeLeaveSender v4.10 新签名版本的 sender（接 appt + text）
type fakeLeaveSender struct {
	mu       sync.Mutex
	calls    []leaveSentCall
	failOn   string // customerID 命中则返回 errToRet
	failWith error
	errOn    func(appt *Appointment) error // 可选的按 appt 动态决策错误
}

type leaveSentCall struct {
	ApptID     string
	CustomerID string
	Text       string
}

func (f *fakeLeaveSender) send(ctx context.Context, appt *Appointment, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, leaveSentCall{ApptID: appt.ID, CustomerID: appt.CustomerID, Text: text})
	if f.errOn != nil {
		if err := f.errOn(appt); err != nil {
			return err
		}
	}
	if f.failOn != "" && f.failOn == appt.CustomerID {
		return f.failWith
	}
	return nil
}

// ---- buildLeaveNotification 文案兜底 ----

func TestBuildLeaveNotification_PrefersCustomerName(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	// 顾客：customers 表 Name='Alice'，appt.Customer='alice 小姐'（不一致）
	cust := MakeCustomer(t, "Alice", 0, 0)
	date, tm := buildApptTime(t, 2)
	appt := MakeAppointment(t, shop.ID, cust.ID, "alice 小姐", barber.Name, date, tm)

	leave := &BarberLeave{
		BarberName: "Tony",
		Reason:     "病假",
	}

	text := buildLeaveNotification(WithCtx(), appt, leave, LeaveActionCancel)

	if !strings.Contains(text, "亲爱的 Alice") {
		t.Errorf("文案应优先使用 customer.Name='Alice'，但 got: %s", text)
	}
	if strings.Contains(text, "亲爱的 alice 小姐") {
		t.Errorf("文案不应使用 appt.Customer='alice 小姐'，但 got: %s", text)
	}
}

func TestBuildLeaveNotification_FallbackToAppointmentCustomer(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	// customer.Name 为空（手工设置空名覆盖默认）
	cust := MakeCustomer(t, "Placeholder", 0, 0)
	DB.Model(cust).Update("name", "")
	date, tm := buildApptTime(t, 2)
	appt := MakeAppointment(t, shop.ID, cust.ID, "alice 小姐", barber.Name, date, tm)

	leave := &BarberLeave{BarberName: "Tony", Reason: "病假"}
	text := buildLeaveNotification(WithCtx(), appt, leave, LeaveActionCancel)

	if !strings.Contains(text, "亲爱的 alice 小姐") {
		t.Errorf("customer.Name 为空时应 fallback 到 appt.Customer，got: %s", text)
	}
}

func TestBuildLeaveNotification_NoNameOmitsPrefix(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := MakeCustomer(t, "", 0, 0) // name 空
	DB.Model(cust).Update("name", "")
	date, tm := buildApptTime(t, 2)
	appt := MakeAppointment(t, shop.ID, cust.ID, "", barber.Name, date, tm)

	leave := &BarberLeave{BarberName: "Tony", Reason: "病假"}
	text := buildLeaveNotification(WithCtx(), appt, leave, LeaveActionCancel)

	if strings.Contains(text, "亲爱的 ,") || strings.Contains(text, "亲爱的  ") {
		t.Errorf("顾客名为空时不应有\"亲爱的 X\"前缀，got: %s", text)
	}
	if !strings.HasPrefix(text, "抱歉地通知您") {
		t.Errorf("无名字时应直接以正文开头，got prefix: %s", text[:min(30, len(text))])
	}
}

func TestBuildLeaveNotification_NeverExposesInternalReason(t *testing.T) {
	// v4.13.0 简化后 buildLeaveNotification 永远 hardcode "师傅临时有事"
	//   不再白名单/不再 CustomerFacingReason 字段
	//   这个测试覆盖：无论 Reason 填什么敏感字眼，文案都不会泄漏
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := MakeCustomer(t, "Alice", 0, 0)
	date, tm := buildApptTime(t, 2)
	appt := MakeAppointment(t, shop.ID, cust.ID, "Alice", barber.Name, date, tm)

	sensitiveReasons := []string{
		"痔疮手术",
		"陪老婆产检",
		"感冒发烧39度",
		"和老婆吵架",
		"病假",  // 之前走白名单原样展示；现在也走兜底
		"家中有事",
		"紧急出差",
	}
	for _, r := range sensitiveReasons {
		t.Run(r, func(t *testing.T) {
			leave := &BarberLeave{BarberName: "Tony", Reason: r}
			text := buildLeaveNotification(WithCtx(), appt, leave, LeaveActionCancel)

			// 敏感字眼绝不能出现在文案里
			for _, sensitive := range []string{"痔疮", "产检", "老婆", "发烧", "吵架"} {
				if r == "家中有事" || r == "紧急出差" || r == "病假" {
					// 这三个本身含"中""紧""假"等普通字眼，不会误判
					// 跳过对它们的"老婆/痔疮"等敏感检查
					if sensitive == "老婆" || sensitive == "痔疮" || sensitive == "产检" {
						continue
					}
				}
				if strings.Contains(text, sensitive) {
					t.Errorf("reason=%q 文案泄漏敏感词 %q, got: %s", r, sensitive, text)
				}
			}
			// 必须 hardcode "师傅临时有事"
			if !strings.Contains(text, "师傅临时有事") {
				t.Errorf("reason=%q 应展示固定文案\"师傅临时有事\"，got: %s", r, text)
			}
		})
	}
}

// ---- CreateBarberLeave + 新签名 sender：持久化 ----

func TestCreateLeaveV2_NotificationPersistedAsSent(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := MakeCustomer(t, "Alice", 0, 0)
	date, tm := buildApptTime(t, 2)
	appt := MakeAppointment(t, shop.ID, cust.ID, "Alice", barber.Name, date, tm)

	sender := &fakeLeaveSender{}
	leave := BarberLeave{
		ShopID: shop.ID, BarberID: barber.ID,
		StartAt: futureTime(1), EndAt: futureTime(4),
		Reason: "病假", Action: LeaveActionCancel,
	}
	res, err := CreateBarberLeave(WithCtx(), leave, sender.send)
	if err != nil {
		t.Fatalf("CreateBarberLeave: %v", err)
	}
	if len(res.NotifiedCustomers) != 1 || res.NotifiedCustomers[0] != cust.ID {
		t.Errorf("NotifiedCustomers = %v, want [%s]", res.NotifiedCustomers, cust.ID)
	}
	if len(res.FailedCustomers) != 0 || len(res.SkippedCustomers) != 0 {
		t.Errorf("应有 0 failed / 0 skipped，got failed=%v skipped=%v", res.FailedCustomers, res.SkippedCustomers)
	}

	// 验证 customer_notification row 写入了 + status=sent
	list, err := ListNotificationsByLeave(WithCtx(), res.LeaveID)
	if err != nil {
		t.Fatalf("ListNotificationsByLeave: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 notification row, got %d", len(list))
	}
	n := list[0]
	if n.Status != NotifStatusSent {
		t.Errorf("Status = %q, want sent", n.Status)
	}
	if n.CustomerID != cust.ID {
		t.Errorf("CustomerID = %q, want %q", n.CustomerID, cust.ID)
	}
	if n.AppointmentID != appt.ID {
		t.Errorf("AppointmentID = %q, want %q", n.AppointmentID, appt.ID)
	}
	if n.Type != NotifTypeLeaveCancel {
		t.Errorf("Type = %q, want %q", n.Type, NotifTypeLeaveCancel)
	}
	if n.TextPreview == "" {
		t.Errorf("TextPreview should be populated")
	}
	if !strings.Contains(n.TextPreview, "Alice") {
		t.Errorf("TextPreview should contain customer name 'Alice', got: %s", n.TextPreview)
	}
	// v4.13.0：所有 reason 走兜底"师傅临时有事"，不再白名单展示
	if !strings.Contains(n.TextPreview, "师傅临时有事") {
		t.Errorf("TextPreview should contain fallback '师傅临时有事', got: %s", n.TextPreview)
	}
}

func TestCreateLeaveV2_FailedNotificationPersisted(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := MakeCustomer(t, "Alice", 0, 0)
	date, tm := buildApptTime(t, 2)
	MakeAppointment(t, shop.ID, cust.ID, "Alice", barber.Name, date, tm)

	// sender 让这个顾客失败
	sender := &fakeLeaveSender{
		failOn:   cust.ID,
		failWith: errors.New("wechat api 95004 限流"),
	}
	leave := BarberLeave{
		ShopID: shop.ID, BarberID: barber.ID,
		StartAt: futureTime(1), EndAt: futureTime(4),
		Reason: "病假", Action: LeaveActionCancel,
	}
	res, err := CreateBarberLeave(WithCtx(), leave, sender.send)
	if err != nil {
		t.Fatalf("CreateBarberLeave: %v", err)
	}
	if len(res.FailedCustomers) != 1 || res.FailedCustomers[0] != cust.ID {
		t.Errorf("FailedCustomers = %v, want [%s]", res.FailedCustomers, cust.ID)
	}
	if len(res.NotifiedCustomers) != 0 {
		t.Errorf("应有 0 notified，got %v", res.NotifiedCustomers)
	}

	list, _ := ListNotificationsByLeave(WithCtx(), res.LeaveID)
	if len(list) != 1 || list[0].Status != NotifStatusFailed {
		t.Fatalf("expected 1 failed row, got %+v", list)
	}
	if !strings.Contains(list[0].ErrorMessage, "95004") {
		t.Errorf("ErrorMessage 应记录错误原因，got %q", list[0].ErrorMessage)
	}
}

func TestCreateLeaveV2_NoCustomerContactSkipped(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	// 顾客有 customerID 但完全无联系方式（外部用户ID/微信ID/电话都空）
	cust := MakeCustomer(t, "Alice", 0, 0)
	DB.Model(cust).Updates(map[string]interface{}{
		"wechat_open_id":   "",
		"external_user_id": "",
		"phone":            "",
	})
	date, tm := buildApptTime(t, 2)
	MakeAppointment(t, shop.ID, cust.ID, "Alice", barber.Name, date, tm)

	// sender 收到 ErrNoCustomerContact → storage 标 skipped
	sender := &fakeLeaveSender{
		failOn:   cust.ID,
		failWith: ErrNoCustomerContact,
	}
	leave := BarberLeave{
		ShopID: shop.ID, BarberID: barber.ID,
		StartAt: futureTime(1), EndAt: futureTime(4),
		Reason: "病假", Action: LeaveActionCancel,
	}
	res, err := CreateBarberLeave(WithCtx(), leave, sender.send)
	if err != nil {
		t.Fatalf("CreateBarberLeave: %v", err)
	}
	if len(res.SkippedCustomers) != 1 {
		t.Errorf("SkippedCustomers = %v, want 1", res.SkippedCustomers)
	}
	if len(res.FailedCustomers) != 0 {
		t.Errorf("ErrNoCustomerContact 不应算 failed，got %v", res.FailedCustomers)
	}

	list, _ := ListNotificationsByLeave(WithCtx(), res.LeaveID)
	if len(list) != 1 || list[0].Status != NotifStatusSkipped {
		t.Fatalf("expected 1 skipped row, got %+v", list)
	}
	if !strings.Contains(list[0].ErrorMessage, "no external_userid") {
		t.Errorf("ErrorMessage 应记录 skipped 原因，got %q", list[0].ErrorMessage)
	}
}

func TestCreateLeaveV2_NilCustomerIDSkipped(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	// 预约完全没绑 customer（CustomerID 空）
	date, tm := buildApptTime(t, 2)
	MakeAppointment(t, shop.ID, "", "Anonymous", barber.Name, date, tm)

	sender := &fakeLeaveSender{}
	leave := BarberLeave{
		ShopID: shop.ID, BarberID: barber.ID,
		StartAt: futureTime(1), EndAt: futureTime(4),
		Reason: "病假", Action: LeaveActionCancel,
	}
	res, err := CreateBarberLeave(WithCtx(), leave, sender.send)

	// leave 应能创建成功（admin 路径，事务不阻断）
	if err != nil {
		t.Fatalf("CreateBarberLeave: %v", err)
	}
	if len(res.SkippedCustomers) != 1 {
		t.Errorf("SkippedCustomers = %v, want 1 (含空 customerID)", res.SkippedCustomers)
	}

	// sender 不应被调用
	if len(sender.calls) != 0 {
		t.Errorf("customerID 空时不应调 sender，got %d calls", len(sender.calls))
	}

	// row 应该是 skipped
	list, _ := ListNotificationsByLeave(WithCtx(), res.LeaveID)
	if len(list) != 1 || list[0].Status != NotifStatusSkipped {
		t.Fatalf("expected 1 skipped row, got %+v", list)
	}
	if !strings.Contains(list[0].ErrorMessage, "customer_id 为空") {
		t.Errorf("ErrorMessage 应说明 customer_id 为空，got %q", list[0].ErrorMessage)
	}

	// 类型应是 leave_no_contact（区分手动联系 vs 其他类型）
	if list[0].Type != NotifTypeLeaveNoContact {
		t.Errorf("Type = %q, want %q", list[0].Type, NotifTypeLeaveNoContact)
	}
}

func TestCreateLeaveV2_RescheduleTypeAndSenderGetsText(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barberA := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	barberB := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")
	setBarberSkills(t, barberB.ID, "剪发,染发")
	cust := MakeCustomer(t, "Alice", 0, 0)
	date, tm := buildApptTime(t, 2)
	MakeAppointment(t, shop.ID, cust.ID, "Alice", barberA.Name, date, tm)

	sender := &fakeLeaveSender{}
	leave := BarberLeave{
		ShopID: shop.ID, BarberID: barberA.ID,
		StartAt: futureTime(1), EndAt: futureTime(4),
		Reason: "家中有事", Action: LeaveActionReschedule,
	}
	res, err := CreateBarberLeave(WithCtx(), leave, sender.send)
	if err != nil {
		t.Fatalf("CreateBarberLeave: %v", err)
	}
	if res.RescheduledCount != 1 {
		t.Errorf("RescheduledCount = %d, want 1", res.RescheduledCount)
	}

	// notification row 应该是 leave_reschedule 类型
	list, _ := ListNotificationsByLeave(WithCtx(), res.LeaveID)
	if len(list) != 1 || list[0].Type != NotifTypeLeaveReschedule {
		t.Errorf("expected leave_reschedule notification, got %+v", list)
	}
	// text 应包含新 barber 名（Kevin）
	if !strings.Contains(sender.calls[0].Text, "Kevin") {
		t.Errorf("改派文案应包含新 barber 名 'Kevin'，got: %s", sender.calls[0].Text)
	}
}

// 兼容：老测试 fakeSender.send（func(ctx, customerID, text) error）应继续走旧路径，不持久化
func TestCreateLeaveV2_OldSenderSignature_NotPersisted(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := MakeCustomer(t, "Alice", 0, 0)
	date, tm := buildApptTime(t, 2)
	MakeAppointment(t, shop.ID, cust.ID, "Alice", barber.Name, date, tm)

	oldSender := &fakeSender{} // 用老方法签名 func(ctx, customerID, text) error
	leave := BarberLeave{
		ShopID: shop.ID, BarberID: barber.ID,
		StartAt: futureTime(1), EndAt: futureTime(4),
		Reason: "病假", Action: LeaveActionCancel,
	}
	res, err := CreateBarberLeave(WithCtx(), leave, oldSender.send)
	if err != nil {
		t.Fatalf("CreateBarberLeave: %v", err)
	}
	if len(res.NotifiedCustomers) != 1 {
		t.Errorf("NotifiedCustomers = %v, want 1", res.NotifiedCustomers)
	}
	// 旧签名路径不应写 customer_notification row
	list, _ := ListNotificationsByLeave(WithCtx(), res.LeaveID)
	if len(list) != 0 {
		t.Errorf("老签名路径不应写 notification row，got %d", len(list))
	}
}