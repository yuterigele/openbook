package storage

// repo_test.go
//
// Pure-logic tests for the storage package (no DB required):
//   - IsValidSlot: DefaultSlots validation
//   - ParseDate:   YYYY-MM-DD parsing (with whitespace trimming)
//   - IsShopHoliday: single-day match
//   - AllShopHolidays: multi-day parse into map
//   - VerifyAdminPassword: bcrypt verification
//
// Run:
//   go test ./storage/... -v -run "TestIsValidSlot|TestParseDate|TestIsShopHoliday|TestAllShopHolidays|TestVerifyAdminPassword"

import (
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ===================== IsValidSlot =====================

func TestIsValidSlot_Valid(t *testing.T) {
	// DefaultSlots covers 09-18 with a lunch break 12-13:30.
	cases := []struct {
		t    string
		want bool
	}{
		{"09:00", true},
		{"10:30", true},
		{"11:30", true},
		{"12:00", false}, // lunch
		{"12:30", false},
		{"13:00", false},
		{"13:30", true},
		{"17:00", true},
		{"17:30", true},
		{"18:00", true},
		{"18:30", false}, // after close
		{"08:59", false}, // before open
		{"", false},
		{"25:00", false}, // invalid format
		{"9:00", false},  // missing leading zero
	}
	for _, c := range cases {
		t.Run(c.t, func(t *testing.T) {
			if got := IsValidSlot(c.t); got != c.want {
				t.Errorf("IsValidSlot(%q) = %v, want %v", c.t, got, c.want)
			}
		})
	}
}

// ===================== ParseDate =====================

func TestParseDate_OK(t *testing.T) {
	got, err := ParseDate("2026-06-21")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}
	if got.Year() != 2026 || got.Month() != 6 || got.Day() != 21 {
		t.Errorf("ParseDate got %v, want 2026-06-21", got.Format("2006-01-02"))
	}
}

func TestParseDate_TrimsWhitespace(t *testing.T) {
	got, err := ParseDate("  2026-06-21\n")
	if err != nil {
		t.Fatalf("ParseDate with whitespace: %v", err)
	}
	if got.Day() != 21 {
		t.Errorf("got day %d, want 21", got.Day())
	}
}

func TestParseDate_BadFormat(t *testing.T) {
	cases := []string{
		"2026/06/21",
		"21-06-2026",
		"not-a-date",
		"2026-13-01", // invalid month — time.Parse is strict
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := ParseDate(c); err == nil {
				t.Errorf("ParseDate(%q) expected error, got nil", c)
			}
		})
	}
}

// ===================== IsShopHoliday =====================

func TestIsShopHoliday_SingleMatch(t *testing.T) {
	s := &Shop{Holidays: "2026-10-01,2026-10-02,2026-10-03"}

	if !IsShopHoliday(s, "2026-10-01") {
		t.Error("expected 2026-10-01 to be a holiday")
	}
	if !IsShopHoliday(s, "2026-10-03") {
		t.Error("expected 2026-10-03 to be a holiday")
	}
	if IsShopHoliday(s, "2026-10-04") {
		t.Error("expected 2026-10-04 to NOT be a holiday")
	}
}

func TestIsShopHoliday_NilShopOrEmpty(t *testing.T) {
	if IsShopHoliday(nil, "2026-10-01") {
		t.Error("nil shop should not be a holiday")
	}
	s := &Shop{Holidays: ""}
	if IsShopHoliday(s, "2026-10-01") {
		t.Error("empty holidays should not match")
	}
}

// ===================== AllShopHolidays =====================

func TestAllShopHolidays_ParsesAll(t *testing.T) {
	s := &Shop{Holidays: "2026-10-01, 2026-10-02 ,2026-10-03"}
	got := AllShopHolidays(s)

	want := map[string]bool{
		"2026-10-01": true,
		"2026-10-02": true,
		"2026-10-03": true,
	}
	if len(got) != len(want) {
		t.Errorf("got %d holidays, want %d", len(got), len(want))
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing %s", k)
		}
	}
}

func TestAllShopHolidays_NilOrEmpty(t *testing.T) {
	if got := AllShopHolidays(nil); len(got) != 0 {
		t.Errorf("nil shop: got %d holidays, want 0", len(got))
	}
	if got := AllShopHolidays(&Shop{}); len(got) != 0 {
		t.Errorf("empty shop: got %d holidays, want 0", len(got))
	}
}

// ===================== VerifyAdminPassword =====================

func TestVerifyAdminPassword_OK(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	admin := &ShopAdmin{Username: "owner", PasswordHash: string(hash)}
	if !VerifyAdminPassword(admin, "hunter2") {
		t.Error("correct password should verify")
	}
}

func TestVerifyAdminPassword_WrongPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	admin := &ShopAdmin{Username: "owner", PasswordHash: string(hash)}
	if VerifyAdminPassword(admin, "wrong") {
		t.Error("wrong password should NOT verify")
	}
	if VerifyAdminPassword(admin, "") {
		t.Error("empty password should NOT verify")
	}
}

// ===================== string utility smoke =====================

func TestTrimSpaceUsedByParseDate(t *testing.T) {
	// documents that ParseDate trims whitespace
	in := "  2026-06-21  "
	trimmed := strings.TrimSpace(in)
	if trimmed != "2026-06-21" {
		t.Errorf("TrimSpace: got %q, want %q", trimmed, "2026-06-21")
	}
}

// ===================== QueryAvailableSlots：leave 感知（PRD §11.7.9 v3.6） =====================
//
// 之前 QueryAvailableSlots 只过滤已预约的时段；v3.6 起再过滤被请假区间覆盖的时段，
// 避免顾客在 query_schedule 里看到 14:00 可约、create_appointment 时却被 leave 拒。

// TestQueryAvailableSlots_FiltersLeaveCoveredSlots：leave 覆盖 14:00-16:00，
// 这些 slot 不应出现在 available 列表里。
func TestQueryAvailableSlots_FiltersLeaveCoveredSlots(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	date := "2026-06-21"
	loc, _ := time.LoadLocation("Asia/Shanghai")
	dayStart, _ := time.ParseInLocation("2006-01-02", date, loc)
	// Tony 在 14:00-16:00 请假
	MakeBarberLeave(t, shop.ID, barber.ID,
		dayStart.Add(14*time.Hour), dayStart.Add(16*time.Hour),
		LeaveActionCancel)

	got := QueryAvailableSlots("Tony", date)
	for _, s := range got {
		// 14:00 / 14:30 / 15:00 / 15:30 / 16:00 都应被排除（区间含端点）
		if s == "14:00" || s == "14:30" || s == "15:00" || s == "15:30" || s == "16:00" {
			t.Errorf("slot %s should be excluded by leave, but appeared in %v", s, got)
		}
	}
	// 至少应包含未受影响的 slot（09:00 / 17:30 等）
	if len(got) == 0 {
		t.Errorf("expected some non-leave slots, got none")
	}
}

// TestQueryAvailableSlots_FullDayLeave：整天请假应返回空
func TestQueryAvailableSlots_FullDayLeave(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	date := "2026-06-21"
	loc, _ := time.LoadLocation("Asia/Shanghai")
	dayStart, _ := time.ParseInLocation("2006-01-02", date, loc)
	MakeBarberLeave(t, shop.ID, barber.ID,
		dayStart, dayStart.Add(24*time.Hour), LeaveActionCancel)

	got := QueryAvailableSlots("Tony", date)
	if len(got) != 0 {
		t.Errorf("full-day leave should yield empty slots, got %v", got)
	}
}

// TestQueryAvailableSlots_CancelledLeaveIgnored：已撤销的 leave 不影响 slots
func TestQueryAvailableSlots_CancelledLeaveIgnored(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	barber := MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	date := "2026-06-21"
	loc, _ := time.LoadLocation("Asia/Shanghai")
	dayStart, _ := time.ParseInLocation("2006-01-02", date, loc)
	l := MakeBarberLeave(t, shop.ID, barber.ID,
		dayStart.Add(14*time.Hour), dayStart.Add(16*time.Hour),
		LeaveActionCancel)
	// 标记为 cancelled
	if err := DB.Model(l).Update("status", LeaveStatusCancelled).Error; err != nil {
		t.Fatalf("update cancelled: %v", err)
	}

	got := QueryAvailableSlots("Tony", date)
	// 应包含 14:00、14:30、15:00、15:30、16:00
	expectPresent := []string{"14:00", "14:30", "15:00", "15:30", "16:00"}
	for _, s := range expectPresent {
		found := false
		for _, g := range got {
			if g == s {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("slot %s should be present after leave cancelled, got %v", s, got)
		}
	}
}

// TestQueryAvailableSlots_LeaveFromOtherBarberIgnored：其他理发师的 leave 不影响当前理发师
func TestQueryAvailableSlots_LeaveFromOtherBarberIgnored(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	kevin := MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")

	date := "2026-06-21"
	loc, _ := time.LoadLocation("Asia/Shanghai")
	dayStart, _ := time.ParseInLocation("2006-01-02", date, loc)
	// Kevin 请假 14:00-16:00
	MakeBarberLeave(t, shop.ID, kevin.ID,
		dayStart.Add(14*time.Hour), dayStart.Add(16*time.Hour),
		LeaveActionCancel)

	got := QueryAvailableSlots("Tony", date)
	// Tony 的 14:00 / 14:30 / 15:00 / 15:30 / 16:00 都应该在 got 里（不被排除）
	mustHave := []string{"14:00", "14:30", "15:00", "15:30", "16:00"}
	for _, want := range mustHave {
		found := false
		for _, s := range got {
			if s == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Tony's slot %s should be present (Kevin's leave shouldn't affect Tony), got %v", want, got)
		}
	}
}

// TestQueryAvailableSlots_BookedSlotStillExcluded：已预约的 slot 也照常排除（与 leave 解耦）
func TestQueryAvailableSlots_BookedSlotStillExcluded(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	date := "2026-06-21"
	// 已有人预约 10:00
	MakeAppointment(t, shop.ID, "cust-1", "Alice", "Tony", date, "10:00")

	got := QueryAvailableSlots("Tony", date)
	for _, s := range got {
		if s == "10:00" {
			t.Errorf("booked slot 10:00 should be excluded, got %v", got)
		}
	}
}

// ===================== QueryScheduleBreakdown (PRD §11.7.10 v3.6) =====================
//
// 一次性返回 available + leave blocks + booked count
// 关键不变量：
//   - available 里的 slot 一定不在 booked / leave 覆盖里
//   - leave blocks 按 start_at ASC（ListBarberLeavesInRange 已保证）
//   - booked count == 该天 active appt 数

// TestQueryScheduleBreakdown_Empty：空数据 → 全可约，0 booked，0 leaves
func TestQueryScheduleBreakdown_Empty(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	got := QueryScheduleBreakdown("Tony", "2026-06-21")
	if got.BookedCount != 0 {
		t.Errorf("empty: BookedCount should be 0, got %d", got.BookedCount)
	}
	if len(got.LeaveBlocks) != 0 {
		t.Errorf("empty: LeaveBlocks should be empty, got %v", got.LeaveBlocks)
	}
	if len(got.Available) != len(DefaultSlots) {
		t.Errorf("empty: Available should be all %d default slots, got %d", len(DefaultSlots), len(got.Available))
	}
}

// TestQueryScheduleBreakdown_PartialLeave_Booked：同时有 leave + booked → 三个维度都正确
func TestQueryScheduleBreakdown_PartialLeave_Booked(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := MakeCustomer(t, "Alice", 0, 0)

	date := "2026-06-21"
	// 占用 2 个 slot：09:00 / 10:00
	MakeAppointment(t, shop.ID, cust.ID, "Alice", "Tony", date, "09:00")
	MakeAppointment(t, shop.ID, cust.ID, "Alice", "Tony", date, "10:00")
	// 请假：14:00-16:00
	loc, _ := time.LoadLocation("Asia/Shanghai")
	dayStart, _ := time.ParseInLocation("2006-01-02", date, loc)
	MakeBarberLeave(t, shop.ID, "barber-Tony",
		dayStart.Add(14*time.Hour), dayStart.Add(16*time.Hour), LeaveActionCancel)
	DB.Model(&BarberLeave{}).Where("barber_id = ?", "barber-Tony").Update("reason", "体检")

	got := QueryScheduleBreakdown("Tony", date)

	if got.BookedCount != 2 {
		t.Errorf("BookedCount should be 2, got %d", got.BookedCount)
	}
	if len(got.LeaveBlocks) != 1 {
		t.Fatalf("LeaveBlocks should have 1 entry, got %d", len(got.LeaveBlocks))
	}
	lb := got.LeaveBlocks[0]
	if lb.StartHM != "14:00" || lb.EndHM != "16:00" {
		t.Errorf("LeaveBlocks[0] should be 14:00-16:00, got %s-%s", lb.StartHM, lb.EndHM)
	}
	if lb.Reason != "体检" {
		t.Errorf("LeaveBlocks[0].Reason should be '体检', got %q", lb.Reason)
	}

	// available 应该排除 09:00 / 10:00 / 14:00 / 14:30 / 15:00 / 15:30 / 16:00
	for _, slot := range got.Available {
		for _, banned := range []string{"09:00", "10:00", "14:00", "14:30", "15:00", "15:30", "16:00"} {
			if slot == banned {
				t.Errorf("slot %q should be excluded from Available, but it is in: %v", banned, got.Available)
			}
		}
	}
}

// TestQueryScheduleBreakdown_FullDayLeave_BlocksAll：整天请假 → available 为空
func TestQueryScheduleBreakdown_FullDayLeave_BlocksAll(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	date := "2026-06-21"
	loc, _ := time.LoadLocation("Asia/Shanghai")
	dayStart, _ := time.ParseInLocation("2006-01-02", date, loc)
	// 整天请假：00:00 - 次日 00:00
	MakeBarberLeave(t, shop.ID, "barber-Tony",
		dayStart, dayStart.Add(24*time.Hour), LeaveActionCancel)

	got := QueryScheduleBreakdown("Tony", date)
	if len(got.Available) != 0 {
		t.Errorf("full-day-leave: Available should be empty, got %v", got.Available)
	}
	if len(got.LeaveBlocks) != 1 {
		t.Errorf("full-day-leave: LeaveBlocks should have 1 entry, got %d", len(got.LeaveBlocks))
	}
}

// TestQueryScheduleBreakdown_CancelledLeave_NotCounted：cancelled leave 不算
func TestQueryScheduleBreakdown_CancelledLeave_NotCounted(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	// 用"明天"——确保 leave 还没开始，否则 CancelBarberLeave 会拒
	loc, _ := time.LoadLocation("Asia/Shanghai")
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1)
	tomorrow = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, loc)
	date := tomorrow.Format("2006-01-02")

	leave := MakeBarberLeave(t, shop.ID, "barber-Tony",
		tomorrow.Add(14*time.Hour), tomorrow.Add(16*time.Hour), LeaveActionCancel)
	if _, err := CancelBarberLeave(context.Background(), leave.ID, "admin"); err != nil {
		t.Fatalf("CancelBarberLeave: %v", err)
	}

	got := QueryScheduleBreakdown("Tony", date)
	if len(got.LeaveBlocks) != 0 {
		t.Errorf("cancelled leave should not appear in LeaveBlocks, got %v", got.LeaveBlocks)
	}
	if len(got.Available) != len(DefaultSlots) {
		t.Errorf("cancelled leave: Available should be all default slots, got %d/%d", len(got.Available), len(DefaultSlots))
	}
}

// TestQueryScheduleBreakdown_UnknownBarber_ReturnsZeros：barber 不存在时返回零值
func TestQueryScheduleBreakdown_UnknownBarber_ReturnsZeros(t *testing.T) {
	SetupTestDB(t)
	got := QueryScheduleBreakdown("Nobody", "2026-06-21")
	if got.BookedCount != 0 {
		t.Errorf("unknown barber: BookedCount should be 0, got %d", got.BookedCount)
	}
	if len(got.Available) != 0 {
		t.Errorf("unknown barber: Available should be empty, got %v", got.Available)
	}
	if len(got.LeaveBlocks) != 0 {
		t.Errorf("unknown barber: LeaveBlocks should be empty, got %v", got.LeaveBlocks)
	}
}

// TestQueryScheduleBreakdown_MultipleLeaves_PreservesOrder：多条 leave 按 start_at ASC
func TestQueryScheduleBreakdown_MultipleLeaves_PreservesOrder(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	date := "2026-06-21"
	loc, _ := time.LoadLocation("Asia/Shanghai")
	dayStart, _ := time.ParseInLocation("2006-01-02", date, loc)
	// 故意倒序插入
	MakeBarberLeave(t, shop.ID, "barber-Tony",
		dayStart.Add(16*time.Hour), dayStart.Add(17*time.Hour), LeaveActionCancel)
	MakeBarberLeave(t, shop.ID, "barber-Tony",
		dayStart.Add(11*time.Hour), dayStart.Add(12*time.Hour), LeaveActionCancel)

	got := QueryScheduleBreakdown("Tony", date)
	if len(got.LeaveBlocks) != 2 {
		t.Fatalf("LeaveBlocks should have 2 entries, got %d", len(got.LeaveBlocks))
	}
	if got.LeaveBlocks[0].StartHM != "11:00" || got.LeaveBlocks[1].StartHM != "16:00" {
		t.Errorf("LeaveBlocks should be sorted by start_at ASC, got %s then %s",
			got.LeaveBlocks[0].StartHM, got.LeaveBlocks[1].StartHM)
	}
}