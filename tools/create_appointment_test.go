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
	// v4.13.0 隐私保护：填一个敏感 reason，验证 LLM 拿不到
	storage.DB.Model(&storage.BarberLeave{}).
		Where("id = ?", leave.ID).
		Update("reason", "感冒发烧")

	out, err := runCreate(t, shop.ID, args)
	if err == nil {
		t.Fatalf("expected error when barber on leave, got out=%q", out)
	}
	if !strings.Contains(err.Error(), "临时有事") {
		t.Errorf("error should use neutral '临时有事' phrase, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "Tony") {
		t.Errorf("error should mention barber name, got %q", err.Error())
	}
	// v4.13.0 隐私：reason 字眼绝不能出现在错误消息里
	if strings.Contains(err.Error(), "感冒") || strings.Contains(err.Error(), "发烧") {
		t.Errorf("error should NOT leak internal leave reason, got %q", err.Error())
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

// ===================== v4.13.6：跨日 leave → 错误消息裁到当天，不带"明天上午"尾巴 =====================
//
// 业务背景：prod 复现 — leave 跨日（如 10:15 今天 → 11:15 明天），
//   v4.13.5 之前错误消息直接拼 "06-26 10:15 至 06-27 11:15"，LLM 口语化成"从今天上午一直
//   到明天上午"。顾客只关心今天的 14:00，看到"明天上午"一脸懵。
//   v4.13.6 裁到 params.Date 当天 [00:00, 23:59:59]；同日显示 HH:MM，跨日才带 MM-DD。
func TestCreateAppointment_LeaveCrossDay_ErrorClipsToDate(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	_ = storage.MakeCustomer(t, "Alice", 0, 0)

	// 顾客问今天 14:00
	date, _, args := buildApptArgs("Alice", "Tony", 2)

	// Tony 从今天 10:15 请假到明天 11:15（跨日，模拟 prod 那个 25h leave）
	dayStart, _ := time.ParseInLocation("2006-01-02", date, time.Local)
	storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		dayStart.Add(10*time.Hour+15*time.Minute), dayStart.Add(33*time.Hour+15*time.Minute),
		storage.LeaveActionCancel)

	out, err := runCreate(t, shop.ID, args)
	if err == nil {
		t.Fatalf("expected error when barber on leave, got out=%q", out)
	}
	// 关键断言：错误消息里不能再出现明天的日期（"明天" / "06-27" 都不行）
	if strings.Contains(err.Error(), "明天") {
		t.Errorf("error should NOT mention '明天' (cross-day clipped), got %q", err.Error())
	}
	// 关键断言：必须出现今天 10:15 起头（说明裁到当天了）
	if !strings.Contains(err.Error(), "10:15") {
		t.Errorf("error should show today's clipped start HH:MM, got %q", err.Error())
	}
	// 同日裁剪后只显示 HH:MM（不带日期前缀），end 是 23:59
	if !strings.Contains(err.Error(), "10:15 至 23:59") {
		t.Errorf("error should show '10:15 至 23:59' (same-day HH:MM, no date prefix), got %q", err.Error())
	}
	_ = out
}

// 验证：leave 已经在今天范围内（10:15 → 18:00），错误消息直接显示 HH:MM 不变
func TestCreateAppointment_LeaveSameDay_ErrorShowsHHMM(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	_ = storage.MakeCustomer(t, "Alice", 0, 0)

	date, _, args := buildApptArgs("Alice", "Tony", 2)

	// 当天 10:00 → 18:00 请假，覆盖 14:00
	dayStart, _ := time.ParseInLocation("2006-01-02", date, time.Local)
	storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		dayStart.Add(10*time.Hour), dayStart.Add(18*time.Hour), storage.LeaveActionCancel)

	_, err := runCreate(t, shop.ID, args)
	if err == nil {
		t.Fatalf("expected error when barber on leave, got nil")
	}
	if !strings.Contains(err.Error(), "10:00 至 18:00") {
		t.Errorf("error should show '10:00 至 18:00' (no date prefix for same-day), got %q", err.Error())
	}
	// 不该出现日期前缀
	if strings.Contains(err.Error(), "06-") {
		t.Errorf("error should NOT show date prefix for same-day, got %q", err.Error())
	}
}

// ===================== v4.13.1：顾客纠正姓名必须同步到 customers.name =====================
//
// 业务背景：之前 upsertCustomerInTx 命中现有顾客时只回填 phone / openID / external_user_id，
// 不回填 name。后果：顾客第二次来纠正"我上次说错了，我叫 XXX"，customers.name 仍是老名字，
// leave notify / admin 详情 / 黑名单判定全用错名（v4.13.1 修复）。
//
// 这些测试走完整工具链路：CreateAppointmentTool.InvokableRun → storage.CreateAppointmentFull
// → upsertCustomerInTx，覆盖 4 个查重分支的姓名更新。

// TestCreateAppointment_NameCorrection_PhoneHit 同一 phone 第二次预约，顾客纠正姓名
func TestCreateAppointment_NameCorrection_PhoneHit(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-name1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	// 第一次：张三 / phone="13800000099" → 新建顾客档案，customer.Name="张三"
	// 用 daysFromNow=1 + time=10:00，避免 hoursFromNow 在跨天后被 snap 到同一 slot
	phone := "13800000099"
	_, _, args1 := buildApptArgsAt("张三", "Tony", 1, "10:00", phone)
	out1, err := runCreate(t, shop.ID, args1)
	if err != nil {
		t.Fatalf("首次预约失败：%v / %s", err, out1)
	}

	// 验证 customer.Name="张三"
	var cust1 storage.Customer
	if err := storage.DB.Where("phone = ?", phone).First(&cust1).Error; err != nil {
		t.Fatalf("查首次顾客：%v", err)
	}
	if cust1.Name != "张三" {
		t.Fatalf("首次 customer.Name 应为'张三'，got=%q", cust1.Name)
	}
	firstCustID := cust1.ID

	// 第二次：同 phone，顾客说"我上次名字记错了，我叫张三丰" → customer.Name 应更新
	// 走 daysFromNow=2 + time=10:00，确保不撞同一 slot
	_, _, args2 := buildApptArgsAt("张三丰", "Tony", 2, "10:00", phone)
	out2, err := runCreate(t, shop.ID, args2)
	if err != nil {
		t.Fatalf("第二次预约失败：%v / %s", err, out2)
	}

	// 验证：customer.ID 应保持（合并档案），customer.Name 应更新成"张三丰"
	var cust2 storage.Customer
	if err := storage.DB.Where("phone = ?", phone).First(&cust2).Error; err != nil {
		t.Fatalf("查第二次顾客：%v", err)
	}
	if cust2.ID != firstCustID {
		t.Errorf("phone 命中应复用同一顾客：first=%q second=%q", firstCustID, cust2.ID)
	}
	if cust2.Name != "张三丰" {
		t.Errorf("customer.Name 未更新：got=%q want='张三丰'（这就是 bug 报告的现象）", cust2.Name)
	}

	// 验证：第二次预约的 Appointment.Customer 字段也是新名
	var appts []storage.Appointment
	storage.DB.Where("customer_id = ?", cust2.ID).Order("created_at ASC").Find(&appts)
	if len(appts) != 2 {
		t.Fatalf("应有 2 条预约，实际 %d", len(appts))
	}
	if appts[0].Customer != "张三" {
		t.Errorf("第 1 条预约 Customer 应为'张三'，got=%q", appts[0].Customer)
	}
	if appts[1].Customer != "张三丰" {
		t.Errorf("第 2 条预约 Customer 应为'张三丰'，got=%q", appts[1].Customer)
	}
}

// TestCreateAppointment_NameCorrection_SameNameNoOp 同 phone 同 name（不纠正），DB 不应重复刷
func TestCreateAppointment_NameCorrection_SameNameNoOp(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-name2", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	phone := "13800000098"
	_, _, args1 := buildApptArgsAt("Alice", "Tony", 1, "10:00", phone)
	if _, err := runCreate(t, shop.ID, args1); err != nil {
		t.Fatalf("首次失败：%v", err)
	}
	var cust1 storage.Customer
	storage.DB.Where("phone = ?", phone).First(&cust1)
	updatedAtBefore := cust1.UpdatedAt

	// 第二次：完全一样的参数（顾客没纠正）
	time.Sleep(10 * time.Millisecond) // 确保如果 Updates 触发，UpdatedAt 会变
	_, _, args2 := buildApptArgsAt("Alice", "Tony", 2, "10:00", phone)
	if _, err := runCreate(t, shop.ID, args2); err != nil {
		t.Fatalf("第二次失败：%v", err)
	}

	var cust2 storage.Customer
	storage.DB.Where("phone = ?", phone).First(&cust2)
	if cust2.Name != "Alice" {
		t.Errorf("Name 应保持 Alice，got=%q", cust2.Name)
	}
	if !cust2.UpdatedAt.Equal(updatedAtBefore) {
		t.Errorf("同 name 不应刷 UpdatedAt：before=%v after=%v", updatedAtBefore, cust2.UpdatedAt)
	}
}

// TestCreateAppointment_NameCorrection_OpenIDHit openID 命中分支
//
// 模拟场景：顾客 A 第一次通过微信进店，openID="wx-old"，传"李四"，建档。
// 第二次 LLM 拿到正确名字"李四哥"（顾客纠正），但 openID 仍是同一个（同一微信）。
// → 应按 openID 命中现有顾客，customer.Name 更新。
func TestCreateAppointment_NameCorrection_OpenIDHit(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-name3", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	// 现有顾客：只有 openID，没 phone
	existing := storage.MakeCustomer(t, "李四", 0, 0)
	existing.WechatOpenID = "wx-fix-test"
	existing.Phone = ""
	if err := storage.DB.Save(existing).Error; err != nil {
		t.Fatalf("save: %v", err)
	}
	custID := existing.ID

	// 模拟"第二次预约"：ctx 注入 openID + 新 phone + 新 name
	phone := "13800000097"
	_, _, args := buildApptArgsAt("李四哥", "Tony", 1, "10:00", phone)
	ctx := WithShopID(WithOpenID(context.Background(), "wx-fix-test"), shop.ID)
	out, err := (&CreateAppointmentTool{}).InvokableRun(ctx, args)
	if err != nil {
		t.Fatalf("create_appointment 失败：%v / %s", err, out)
	}

	// 验证：openID 命中，customer.Name 更新
	var cust storage.Customer
	storage.DB.Where("id = ?", custID).First(&cust)
	if cust.Name != "李四哥" {
		t.Errorf("openID 命中但 Name 未更新：got=%q want='李四哥'", cust.Name)
	}
	if cust.Phone != phone {
		t.Errorf("Phone 未回填：got=%q", cust.Phone)
	}
}

// buildApptArgsAt 构造指定 daysFromNow + 固定 HH:MM 的 args JSON（phone 由调用方指定）
//
// 用于"同一 phone 多次预约"测试，避免 buildApptArgs 随机 phone / snap 撞同一 slot。
//   - daysFromNow=1 是明天，daysFromNow=2 是后天
//   - timeStr 必须在 DefaultSlots 里（如 10:00 / 14:00），否则 IsValidSlot 拒
func buildApptArgsAt(customer, barberName string, daysFromNow int, timeStr, phone string) (date, tm, argsJSON string) {
	at := time.Now().AddDate(0, 0, daysFromNow)
	date = at.Format("2006-01-02")
	argsJSON = `{"customer":"` + customer + `","barber_name":"` + barberName +
		`","phone":"` + phone + `","date":"` + date + `","time":"` + timeStr + `","service":"剪发"}`
	return
}