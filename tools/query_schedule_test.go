package tools

// query_schedule_test.go
//
// P4 工具侧集成 v3.6 测试（PRD §11.7.10 query_schedule 视觉区分）
//
// query_schedule 现在会把"可约 / 请假占用 / 已约满"三类视觉区分开：
//   - 可约 slot   → 顶部 "09:00, 09:30, ..." 一行
//   - 请假占用   → "师傅请假占用：14:00-16:00（体检）" 一段
//   - 已约满     → "其余 X 个时段均已被预约" 一段
//
// 本文件覆盖：
//   - Info 描述里提到请假
//   - 部分请假 → 可约时段扣除 + "师傅请假占用" 段出现
//   - 已撤销请假 → 不计入
//   - 别的理发师请假 → 不影响
//   - 节假日 vs 请假优先级（节假日先报）
//
// 注：本文件焦点是 v3.6 新增的"视觉区分"分支；其他既有逻辑（节假日 / booked /
// barber 不存在 等）不重复覆盖。整天请假的"全天请假"特殊路径需要 leave 跨午夜，
// 当前 toBarberLeaves 在跨午夜时会丢失日期信息（已知小坑），留给后续 v3.7。
//
// Run:
//   go test ./tools/... -v -run "TestQuerySchedule"

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

// runQuerySchedule 跑一次 query_schedule 工具，shop_id 通过 ctx 注入
func runQuerySchedule(t *testing.T, shopID, barberName, date string) (string, error) {
	t.Helper()
	ctx := WithShopID(context.Background(), shopID)
	argsJSON := `{"barber_name":"` + barberName + `","date":"` + date + `"}`
	return (&QueryScheduleTool{}).InvokableRun(ctx, argsJSON)
}

// ===================== Info =====================

func TestQueryScheduleTool_InfoMentionsLeave(t *testing.T) {
	info, err := (&QueryScheduleTool{}).Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if !strings.Contains(info.Desc, "请假") {
		t.Errorf("Info.Desc should mention '请假' (PRD §11.7.10 v3.6), got %q", info.Desc)
	}
	if !strings.Contains(info.Desc, "师傅请假占用") {
		t.Errorf("Info.Desc should mention '师傅请假占用' (v3.6 视觉区分), got %q", info.Desc)
	}
}

// ===================== 部分请假 → 可约时段扣除 + leave note =====================

func TestQuerySchedule_PartialLeave_SlotsFilteredAndLeaveNoteShown(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1)
	tomorrow = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, loc)
	dateStr := tomorrow.Format("2006-01-02")

	// 部分请假：14:00 - 16:00
	leaveStart := tomorrow.Add(14 * time.Hour)
	leaveEnd := tomorrow.Add(16 * time.Hour)
	storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		leaveStart, leaveEnd, storage.LeaveActionCancel)
	storage.DB.Model(&storage.BarberLeave{}).
		Where("barber_id = ?", "barber-Tony").
		Update("reason", "陪老婆产检") // v4.13.0 隐私测试

	out, err := runQuerySchedule(t, shop.ID, "Tony", dateStr)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	// 顶部应该有"可预约时段"列表
	if !strings.Contains(out, "可预约时段") {
		t.Errorf("output should list available slots, got %q", out)
	}
	// "师傅请假占用" 段必须出现
	if !strings.Contains(out, "师傅请假占用") {
		t.Errorf("output should have '师傅请假占用' section, got %q", out)
	}
	if !strings.Contains(out, "14:00") || !strings.Contains(out, "16:00") {
		t.Errorf("leave note should include 14:00 and 16:00, got %q", out)
	}
	if strings.Contains(out, "产检") || strings.Contains(out, "老婆") {
		t.Errorf("v4.13.0 隐私：leave note 不应泄漏内部 reason, got %q", out)
	}
	_ = "" // marker
	// "师傅请假占用" 段必须在"可预约时段"段之后
	if idxList := strings.Index(out, "可预约时段"); idxList >= 0 {
		if idxLeave := strings.Index(out, "师傅请假占用"); idxLeave >= 0 && idxLeave < idxList {
			t.Errorf("leave note should come AFTER available slots, got %q", out)
		}
	}
}

// ===================== 整天请假 → "没有可约时段" + "师傅请假占用" =====================
//
// 已知小坑：当前 isFullDayLeave 通过 toBarberLeaves 还原 BarberLeave，跨午夜的 leave
// 会丢失日期（end 解析回 start 时刻）。所以"全天请假"特殊路径目前不可达。
// 但整体输出（无可约时段 + leave note）依然是可执行的，下面验证这个回退路径。

func TestQuerySchedule_FullDayLeave_FallbackShowsLeaveNote(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1)
	tomorrow = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, loc)
	dateStr := tomorrow.Format("2006-01-02")

	// 整天请假（00:00 到次日 00:00）
	storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		tomorrow, tomorrow.Add(24*time.Hour), storage.LeaveActionCancel)
	storage.DB.Model(&storage.BarberLeave{}).
		Where("barber_id = ?", "barber-Tony").
		Update("reason", "体检")

	out, err := runQuerySchedule(t, shop.ID, "Tony", dateStr)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	// 没有可约 slot（被 leave 全占）
	if !strings.Contains(out, "没有可预约的时段") {
		t.Errorf("full-day-leave output should say '没有可预约的时段', got %q", out)
	}
	// "师傅请假占用" 段必须出现（带原因）
	if !strings.Contains(out, "师傅请假占用") {
		t.Errorf("full-day-leave output should have '师傅请假占用' section, got %q", out)
	}
	// v4.13.0 隐私：敏感 reason 绝不能出现在输出
	if strings.Contains(out, "体检") {
		t.Errorf("v4.13.0 隐私: leave note 不应泄漏内部 reason '体检', got %q", out)
	}
}

// ===================== 已撤销请假 → 不影响 =====================

func TestQuerySchedule_CancelledLeave_NotCounted(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1)
	tomorrow = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, loc)
	dateStr := tomorrow.Format("2006-01-02")

	leave := storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		tomorrow.Add(14*time.Hour), tomorrow.Add(16*time.Hour),
		storage.LeaveActionCancel)
	if _, err := storage.CancelBarberLeave(context.Background(), leave.ID, "admin"); err != nil {
		t.Fatalf("CancelBarberLeave: %v", err)
	}

	out, err := runQuerySchedule(t, shop.ID, "Tony", dateStr)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	if strings.Contains(out, "师傅请假占用") {
		t.Errorf("cancelled leave should not show '师傅请假占用' section, got %q", out)
	}
	if strings.Contains(out, "私事") || strings.Contains(out, "test leave") {
		t.Errorf("cancelled leave should not leak reason, got %q", out)
	}
}

// ===================== 别的理发师请假 → 不影响 Tony =====================

func TestQuerySchedule_OtherBarberLeave_NotAffected(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	storage.MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1)
	tomorrow = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, loc)
	dateStr := tomorrow.Format("2006-01-02")

	storage.MakeBarberLeave(t, shop.ID, "barber-Kevin",
		tomorrow.Add(14*time.Hour), tomorrow.Add(16*time.Hour),
		storage.LeaveActionCancel)

	out, err := runQuerySchedule(t, shop.ID, "Tony", dateStr)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	if strings.Contains(out, "师傅请假占用") {
		t.Errorf("other barber's leave should not affect Tony's output, got %q", out)
	}
}

// ===================== v4.13.7：跨日 leave 必须显式带日期防 LLM 误读 =====================
//
// prod 复现（2026-06-26）：老王 leave 10:15 今天 → 11:15 明天 / 11:15 今天 → 12:15 明天
//   旧版 LeaveBlock 只显示 HH:MM："师傅请假占用：10:15-11:15、11:15-12:15"
//   LLM 看到 12:15 误读成"老王请假到今天 12:15"，进而推断"下午 2 点老王回来了但没档期"
//   实际 leave 跨日，end_at 是明天 12:15，老王今天全天都在请假
//
// v4.13.7 修复：跨日 leave 在 LeaveBlock 里显式带日期（"次日 HH:MM" 或 "MM-DD HH:MM"）
func TestQuerySchedule_CrossDayLeave_ShowsDatePrefix(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1)
	tomorrow = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, loc)
	dateStr := tomorrow.Format("2006-01-02")

	// 跨日 leave：明天 10:15 → 后天 11:15（覆盖 paramsDate 下午 + 次日）
	dayStart, _ := time.ParseInLocation("2006-01-02", dateStr, loc)
	storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		dayStart.Add(10*time.Hour+15*time.Minute),
		dayStart.Add(35*time.Hour+15*time.Minute), // 10h15m + 24h + 1h = 35h15m
		storage.LeaveActionCancel)

	out, err := runQuerySchedule(t, shop.ID, "Tony", dateStr)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}

	// 关键断言：跨日 leave 必须带"次日"前缀，否则 LLM 会误读 end time
	if !strings.Contains(out, "次日") {
		t.Errorf("cross-day leave must show '次日' prefix (v4.13.7), got %q", out)
	}
	if !strings.Contains(out, "10:15 至 次日 11:15") {
		t.Errorf("cross-day leave format should be '10:15 至 次日 11:15', got %q", out)
	}
	// 反向断言：不能再只显示 "10:15-11:15" 这种纯 HH:MM（不带日期）
	if strings.Contains(out, "10:15-11:15") {
		t.Errorf("cross-day leave should NOT show pure HH:MM-HH:MM (causes LLM misread), got %q", out)
	}
}

// 同日 leave 仍然显示 HH:MM-HH:MM（无日期前缀，最干净）
func TestQuerySchedule_SameDayLeave_NoDatePrefix(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1)
	tomorrow = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, loc)
	dateStr := tomorrow.Format("2006-01-02")

	// 同日 leave：明天 10:00 → 明天 18:00
	dayStart, _ := time.ParseInLocation("2006-01-02", dateStr, loc)
	storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		dayStart.Add(10*time.Hour), dayStart.Add(18*time.Hour),
		storage.LeaveActionCancel)

	out, err := runQuerySchedule(t, shop.ID, "Tony", dateStr)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}

	// 同日应该显示 "10:00-18:00"（不带日期）
	if !strings.Contains(out, "10:00-18:00") {
		t.Errorf("same-day leave should show '10:00-18:00' (no date prefix), got %q", out)
	}
	// 不该出现"次日"（同日）
	if strings.Contains(out, "次日") {
		t.Errorf("same-day leave should NOT show '次日', got %q", out)
	}
}

// ===================== 节假日优先于请假 =====================

func TestQuerySchedule_HolidayOverridesLeave(t *testing.T) {
	setupToolsTestDB(t)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1)
	tomorrow = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, loc)
	dateStr := tomorrow.Format("2006-01-02") // YYYY-MM-DD 格式

	shop := storage.MakeShop(t, "shop-1", dateStr) // 把"明天"设为节假日
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	// 节假日 + 同时段请假：节假日文案先报
	storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		tomorrow.Add(14*time.Hour), tomorrow.Add(16*time.Hour),
		storage.LeaveActionCancel)

	out, err := runQuerySchedule(t, shop.ID, "Tony", dateStr)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	if !strings.Contains(out, "本店休息日") {
		t.Errorf("holiday message should take priority, got %q", out)
	}
	if strings.Contains(out, "师傅请假占用") {
		t.Errorf("holiday path should not show leave note, got %q", out)
	}
}

func TestQuerySchedule_RejectsPastDate(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-past", "")
	storage.MakeBarber(t, "barber-past", shop.ID, "Tony")
	loc, _ := time.LoadLocation("Asia/Shanghai")
	pastDate := time.Now().In(loc).AddDate(0, 0, -1).Format("2006-01-02")

	_, err := runQuerySchedule(t, shop.ID, "Tony", pastDate)
	if err == nil || !strings.Contains(err.Error(), "过去日期") {
		t.Fatalf("past date should be rejected before returning slots, err=%v", err)
	}
}
