package tools

// list_barbers_test.go
//
// P4 工具侧集成 v3.6 测试（PRD §11.7.10）
//
// list_barbers 现在会：
//   1. 列出本店所有 active 理发师（姓名 + 技能）
//   2. 当某理发师当天有 active leave 时，标注"今日 HH:MM-HH:MM 请假"或"今日 HH:MM 起请假"
//   3. cancelled / expired leave 不显示
//
// 本文件覆盖：
//   - Info 描述里提到请假
//   - 无请假：正常列表
//   - 当前正在请假：完整区间 + 原因
//   - 即将请假（start_at 在未来）："HH:MM 起"
//   - 已撤销 / 已过期：不再标注
//   - 其他理发师请假：不互相影响
//   - 空店：兜底文案
//
// 注：本文件焦点是 v3.6 新增的"请假感知"分支；其他既有逻辑（无参 / 跨店 / 空集）
// 不重复覆盖。
//
// Run:
//   go test ./tools/... -v -run "TestListBarbers.*Leave"

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// runListBarbers 跑一次 list_barbers 工具，shop_id 通过 ctx 注入
func runListBarbers(t *testing.T, shopID string) (string, error) {
	t.Helper()
	c := &ListBarbersTool{}
	ctx := WithShopID(context.Background(), shopID)
	return c.InvokableRun(ctx, ``)
}

// ===================== Info =====================

func TestListBarbersTool_InfoMentionsLeave(t *testing.T) {
	c := &ListBarbersTool{}
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if !strings.Contains(info.Desc, "请假") {
		t.Errorf("Info.Desc should mention '请假' (PRD §11.7.10 v3.6), got %q", info.Desc)
	}
}

// ===================== 无请假：正常列表 =====================

func TestListBarbers_NoLeave_NormalList(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	storage.MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")

	out, err := runListBarbers(t, shop.ID)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	if !strings.Contains(out, "Tony") || !strings.Contains(out, "Kevin") {
		t.Errorf("output should list both barbers, got %q", out)
	}
	if strings.Contains(out, "请假") {
		t.Errorf("output should NOT mention '请假' when no leave, got %q", out)
	}
}

// ===================== 当前正在请假：完整区间 + 原因 =====================

func TestListBarbers_OngoingLeave_ShowsFullRange(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	// 当前时间 -30min 到 +30min：now 落在 leave 区间内
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	leave := storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		now.Add(-30*time.Minute), now.Add(30*time.Minute), storage.LeaveActionCancel)
	storage.DB.Model(&storage.BarberLeave{}).
		Where("id = ?", leave.ID).
		Update("reason", "体检")

	out, err := runListBarbers(t, shop.ID)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	if !strings.Contains(out, "今日") {
		t.Errorf("output should mention '今日' (ongoing leave tag), got %q", out)
	}
	if !strings.Contains(out, "请假") {
		t.Errorf("output should mention '请假', got %q", out)
	}
	if !strings.Contains(out, "体检") {
		t.Errorf("output should include reason '体检', got %q", out)
	}
	// 区间显示：now 之前 30min HH:MM → now 之后 30min HH:MM
	if !strings.Contains(out, "-") {
		t.Errorf("ongoing leave should show HH:MM-HH:MM range (with -), got %q", out)
	}
}

// ===================== 即将请假：HH:MM 起 =====================

func TestListBarbers_UpcomingLeave_ShowsStartOnly(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	// 2h 后开始 → 4h 后结束（now 落在 [start, end] 之前 → "起"）
	leave := storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		now.Add(2*time.Hour), now.Add(4*time.Hour), storage.LeaveActionCancel)
	_ = leave
	storage.DB.Model(&storage.BarberLeave{}).
		Where("barber_id = ?", "barber-Tony").
		Update("reason", "私事")

	out, err := runListBarbers(t, shop.ID)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	if !strings.Contains(out, "起请假") {
		t.Errorf("upcoming leave should say '起请假', got %q", out)
	}
	if !strings.Contains(out, "私事") {
		t.Errorf("output should include reason '私事', got %q", out)
	}
	if strings.Contains(out, "起请假（原因：私事）-") {
		t.Errorf("upcoming leave should NOT show '-HH:MM' end, got %q", out)
	}
}

// ===================== 已撤销请假：不再标注 =====================

func TestListBarbers_CancelledLeave_NoTag(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	leave := storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		now.Add(2*time.Hour), now.Add(4*time.Hour), storage.LeaveActionCancel)
	if _, err := storage.CancelBarberLeave(context.Background(), leave.ID, "admin"); err != nil {
		t.Fatalf("CancelBarberLeave: %v", err)
	}

	out, err := runListBarbers(t, shop.ID)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	if strings.Contains(out, "请假") {
		t.Errorf("cancelled leave should not show '请假' tag, got %q", out)
	}
}

// ===================== 已过期请假：不再标注 =====================

func TestListBarbers_ExpiredLeave_NoTag(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	// 已过期：end_at 远在过去 → 触发 LeaveExpirer 标记 expired
	_ = storage.MakeBarberLeave(t, shop.ID, "barber-Tony",
		now.Add(-4*time.Hour), now.Add(-2*time.Hour), storage.LeaveActionCancel)
	if _, err := storage.ExpireOverdueLeaves(context.Background(), now); err != nil {
		t.Fatalf("ExpireOverdueLeaves: %v", err)
	}

	out, err := runListBarbers(t, shop.ID)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	if strings.Contains(out, "请假") {
		t.Errorf("expired leave should not show '请假' tag, got %q", out)
	}
}

// ===================== 其他理发师请假：不互相影响 =====================

func TestListBarbers_OtherBarberLeave_OnlyAffectsThatBarber(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	storage.MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	// 只给 Kevin 请假
	storage.MakeBarberLeave(t, shop.ID, "barber-Kevin",
		now.Add(2*time.Hour), now.Add(4*time.Hour), storage.LeaveActionCancel)

	out, err := runListBarbers(t, shop.ID)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	// 解析：找到 "Tony" 那行，看是否含 "请假"；找到 "Kevin" 那行，看是否含 "请假"
	lines := strings.Split(out, "\n")
	var tonyLine, kevinLine string
	for _, ln := range lines {
		if strings.Contains(ln, "Tony") {
			tonyLine = ln
		}
		if strings.Contains(ln, "Kevin") {
			kevinLine = ln
		}
	}
	if tonyLine == "" || kevinLine == "" {
		t.Fatalf("output missing Tony/Kevin lines: %q", out)
	}
	if strings.Contains(tonyLine, "请假") {
		t.Errorf("Tony line should NOT contain '请假' (only Kevin is on leave), got %q", tonyLine)
	}
	if !strings.Contains(kevinLine, "请假") {
		t.Errorf("Kevin line should contain '请假', got %q", kevinLine)
	}
}

// ===================== 空店：兜底文案 =====================

func TestListBarbers_NoBarbers_FallbackMessage(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	_ = shop // 空店不需要 barber

	out, err := runListBarbers(t, shop.ID)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	if !strings.Contains(out, "没有") {
		t.Errorf("empty shop should give fallback message, got %q", out)
	}
}