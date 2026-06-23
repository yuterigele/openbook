package tools

// create_appointment_test.go
//
// P4 理发师请假集成测试（PRD §11.7.4）
//
// 覆盖 create_appointment 工具在 P4 启用后的新分支：
//   1. 理发师没请假 → 正常下单成功
//   2. 理发师在所选时段请假 → 友好错误（带请假时段 + 原因）
//   3. 理发师请假在别的时间段 → 不影响下单
//   4. 已撤销的请假 → 不影响下单
//   5. 别的理发师请假 → 不影响当前下单
//   6. 工具 Info 描述里应提到请假拦截
//
// 注：本测试不验证 redis lock / 节假日 / 排他性等已有逻辑（那些由其他测试覆盖）；
// 焦点是新增的"请假拦截"分支。
//
// Run:
//   go test ./tools/... -v -run "TestCreateAppointment.*Leave\|TestCreateAppointmentTool_Info"

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

// runCreate 跑一次 create_appointment 工具，shop_id 通过 ctx 注入
func runCreate(t *testing.T, shopID, argsJSON string) (string, error) {
	t.Helper()
	c := &CreateAppointmentTool{}
	ctx := WithShopID(context.Background(), shopID)
	return c.InvokableRun(ctx, argsJSON)
}

// buildApptArgs 在 hoursFromNow 时刻构造一组 create_appointment JSON 参数
//
// IsValidSlot 只接受 DefaultSlots 列表里的整点/半点（避开 12:00-13:30 午休）。
// 这里我们 snap 到下一个合法 slot，避免在午休时段或非整点报"时间无效"。
func buildApptArgs(customer, barberName string, hoursFromNow float64) (date, timeStr, argsJSON string) {
	at := time.Now().Add(time.Duration(hoursFromNow * float64(time.Hour)))
	at = snapUpToValidSlot(at)
	date = at.Format("2006-01-02")
	timeStr = at.Format("15:04")
	// v4.9.3: phone 必填（11 位、1 开头）
	phone := "138" + fmt.Sprintf("%08d", rand.Intn(100000000))
	argsJSON = `{"customer":"` + customer + `","barber_name":"` + barberName +
		`","phone":"` + phone + `","date":"` + date + `","time":"` + timeStr + `","service":"剪发"}`
	return
}

// snapUpToValidSlot 把给定时刻 snap 到下一个合法 slot（在 DefaultSlots 列表里、>= t）
func snapUpToValidSlot(t time.Time) time.Time {
	hhmm := t.Format("15:04")
	for _, v := range storage.DefaultSlots {
		if v >= hhmm {
			// 把 v 解析回 time，并继承 t 的日期
			parsed, err := time.ParseInLocation("15:04", v, t.Location())
			if err != nil {
				continue
			}
			return time.Date(t.Year(), t.Month(), t.Day(), parsed.Hour(), parsed.Minute(), 0, 0, t.Location())
		}
	}
	// t 已超过当天最后一个 slot → 跨天到 09:00
	tomorrow := t.AddDate(0, 0, 1)
	parsed, _ := time.ParseInLocation("15:04", "09:00", t.Location())
	return time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), parsed.Hour(), parsed.Minute(), 0, 0, t.Location())
}

func isValidHHMM(s string) bool {
	for _, v := range storage.DefaultSlots {
		if v == s {
			return true
		}
	}
	return false
}

// ===================== Info =====================

func TestCreateAppointmentTool_InfoMentionsLeave(t *testing.T) {
	c := &CreateAppointmentTool{}
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if !strings.Contains(info.Desc, "请假") {
		t.Errorf("Info.Desc should mention '请假' (P4), got %q", info.Desc)
	}
}

// ===================== Happy path: no leave =====================

func TestCreateAppointment_NoLeave_Succeeds(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	_ = storage.MakeCustomer(t, "Alice", 0, 0)

	// 2h 后预约，没有 leave
	_, _, args := buildApptArgs("Alice", "Tony", 2)

	out, err := runCreate(t, shop.ID, args)
	if err != nil {
		t.Fatalf("expected success, got err: %v, out=%q", err, out)
	}
	if !strings.Contains(out, "预约创建成功") {
		t.Errorf("output should confirm success, got %q", out)
	}
}

// ===================== Leave covers appointment → reject =====================

func TestCreateAppointment_LeaveCovering_Rejected(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	_ = storage.MakeCustomer(t, "Alice", 0, 0)

	// 2h 后预约
	date, timeStr, args := buildApptArgs("Alice", "Tony", 2)
	apptAt, _ := time.ParseInLocation("2006-01-02 15:04", date+" "+timeStr, time.Local)

	// Tony 在 [apptAt-1h, apptAt+1h] 请假（覆盖该时段）
	leave := storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		apptAt.Add(-1*time.Hour), apptAt.Add(1*time.Hour), storage.LeaveActionCancel)
	// 改一下 reason 让测试断言更明确
	storage.DB.Model(&storage.BarberLeave{}).
		Where("id = ?", leave.ID).
		Update("reason", "感冒发烧")

	out, err := runCreate(t, shop.ID, args)
	if err == nil {
		t.Fatalf("expected error when barber on leave, got out=%q", out)
	}
	if !strings.Contains(err.Error(), "请假") {
		t.Errorf("error should mention '请假', got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "Tony") {
		t.Errorf("error should mention barber name, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "感冒发烧") {
		t.Errorf("error should include leave reason '感冒发烧', got %q", err.Error())
	}
	if strings.Contains(out, "预约创建成功") {
		t.Errorf("output should NOT say success, got %q", out)
	}

	// 确认 DB 里没有新建的 active 预约
	var n int64
	storage.DB.Model(&storage.Appointment{}).
		Where("barber_id = ? AND date = ? AND time = ? AND status = ?",
			"barber-Tony", date, timeStr, "active").
		Count(&n)
	if n != 0 {
		t.Errorf("DB should have 0 active appts for that slot, got %d", n)
	}
}

// ===================== Leave ends before appointment → no reject =====================

func TestCreateAppointment_LeaveBeforeSlot_Allowed(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	_ = storage.MakeCustomer(t, "Alice", 0, 0)

	// 5h 后预约
	date, timeStr, args := buildApptArgs("Alice", "Tony", 5)
	apptAt, _ := time.ParseInLocation("2006-01-02 15:04", date+" "+timeStr, time.Local)

	// Tony 在 [apptAt-3h, apptAt-2h] 请假（1h 前就结束了）
	storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		apptAt.Add(-3*time.Hour), apptAt.Add(-2*time.Hour), storage.LeaveActionCancel)

	out, err := runCreate(t, shop.ID, args)
	if err != nil {
		t.Fatalf("expected success (leave already ended), got err=%v, out=%q", err, out)
	}
	if !strings.Contains(out, "预约创建成功") {
		t.Errorf("output should confirm success, got %q", out)
	}
}

// ===================== Cancelled leave → no reject =====================

func TestCreateAppointment_CancelledLeave_Allowed(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	_ = storage.MakeCustomer(t, "Alice", 0, 0)

	// 2h 后预约
	date, timeStr, args := buildApptArgs("Alice", "Tony", 2)
	apptAt, _ := time.ParseInLocation("2006-01-02 15:04", date+" "+timeStr, time.Local)

	// 创建后立刻撤销
	leave := storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		apptAt.Add(-1*time.Hour), apptAt.Add(1*time.Hour), storage.LeaveActionCancel)
	if _, err := storage.CancelBarberLeave(context.Background(), leave.ID, "admin"); err != nil {
		t.Fatalf("CancelBarberLeave: %v", err)
	}

	out, err := runCreate(t, shop.ID, args)
	if err != nil {
		t.Fatalf("expected success (leave cancelled), got err=%v, out=%q", err, out)
	}
	if !strings.Contains(out, "预约创建成功") {
		t.Errorf("output should confirm success, got %q", out)
	}
}

// ===================== Different barber on leave → no reject =====================

func TestCreateAppointment_OtherBarberOnLeave_Allowed(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	storage.MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")
	_ = storage.MakeCustomer(t, "Alice", 0, 0)

	// Tony 预约 2h 后
	date, timeStr, args := buildApptArgs("Alice", "Tony", 2)
	apptAt, _ := time.ParseInLocation("2006-01-02 15:04", date+" "+timeStr, time.Local)

	// Kevin 同时段请假（不影响 Tony）
	storage.MakeBarberLeave(t, shop.ID, "barber-Kevin",
		apptAt.Add(-1*time.Hour), apptAt.Add(1*time.Hour), storage.LeaveActionCancel)

	out, err := runCreate(t, shop.ID, args)
	if err != nil {
		t.Fatalf("expected success (other barber on leave), got err=%v, out=%q", err, out)
	}
	if !strings.Contains(out, "预约创建成功") {
		t.Errorf("output should confirm success, got %q", out)
	}
}