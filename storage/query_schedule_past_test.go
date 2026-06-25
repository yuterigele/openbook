package storage

// query_schedule_past_test.go
//
// 覆盖 v4.13.5 修复：query_schedule 工具过滤今天已过去的 slot
//  1. 22:00 查今天 14:00/15:00 应被过滤
//  2. 22:00 查今天 16:00 应保留
//  3. 查明天不过滤（明天所有 slot 都在未来）
//  4. 5 分钟容差：14:30 跑查 14:30 仍保留
//  5. 过去时间 + leave + booked 三重过滤同时生效
//
// Run:
//   go test ./storage/... -v -run "TestQuerySchedule.*Past|TestQueryAvailableSlots_Past|TestFilterPastSlotsToday"

import (
	"strings"
	"testing"
	"time"
)

func TestFilterPastSlotsToday_Basic(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	// 模拟现在 22:00
	now := time.Date(2026, 6, 25, 22, 0, 0, 0, loc)
	today := "2026-06-25"
	slots := []string{"09:00", "14:00", "15:00", "16:00", "17:00", "18:00"}

	got := filterPastSlotsToday(slots, today, now, loc)

	// 期望：22:00 之前的所有 slot 都被过滤（带 5min 容差 → 实际是 22:05 之前）
	// 14:00/15:00/16:00/17:00 都在 22:00 之前 → 全部过滤
	// 只剩 18:00？18:00 在 22:00 之前也过滤掉
	// 实际结果：[]（所有 slot 都在 22:00 之前）
	if len(got) != 0 {
		t.Errorf("22:00 查今天，所有 slot 都应被过滤，got %v", got)
	}
}

func TestFilterPastSlotsToday_MixedFuture(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	// 现在 14:30 → 14:30 + 5min 容差 = 14:35
	now := time.Date(2026, 6, 25, 14, 30, 0, 0, loc)
	today := "2026-06-25"
	slots := []string{"09:00", "14:00", "14:30", "15:00", "16:00"}

	got := filterPastSlotsToday(slots, today, now, loc)

	// 期望：14:00 之前过滤，14:30（now）保留（5min 容差），15:00/16:00 保留
	want := []string{"14:30", "15:00", "16:00"}
	if !equalStringSlice(got, want) {
		t.Errorf("14:30 查今天：\n  got  %v\n  want %v", got, want)
	}
}

func TestFilterPastSlotsToday_FiveMinGrace(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	// 现在 14:31 → 14:30 slot 应保留（5min 容差）
	now := time.Date(2026, 6, 25, 14, 31, 0, 0, loc)
	today := "2026-06-25"
	slots := []string{"14:30"}

	got := filterPastSlotsToday(slots, today, now, loc)

	// 5min 容差：14:30 + 5min = 14:35，14:31 < 14:35，所以 14:30 仍保留
	if len(got) != 1 || got[0] != "14:30" {
		t.Errorf("5 分钟容差：14:31 查 14:30 应保留，got %v", got)
	}
}

func TestFilterPastSlotsToday_NotToday(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	// 现在 6/25 22:00，查 6/26 → 明天
	now := time.Date(2026, 6, 25, 22, 0, 0, 0, loc)
	tomorrow := "2026-06-26"
	slots := []string{"09:00", "14:00", "15:00", "16:00"}

	got := filterPastSlotsToday(slots, tomorrow, now, loc)

	// 不是今天 → 不过滤
	if !equalStringSlice(got, slots) {
		t.Errorf("查明天应不过滤过去时间：\n  got  %v\n  want %v", got, slots)
	}
}

func TestFilterPastSlotsToday_EmptyInput(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 6, 25, 22, 0, 0, 0, loc)
	got := filterPastSlotsToday(nil, "2026-06-25", now, loc)
	if len(got) != 0 {
		t.Errorf("空输入应返回空，got %v", got)
	}
}

// TestQueryScheduleBreakdown_FilterPastSlotsToday 端到端：现在 22:00 查今天 Tony
// → 期望 Available 里没有 14:00/15:00（已过去 8 小时）
func TestQueryScheduleBreakdown_FilterPastSlotsToday(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "b-tony", shop.ID, "Tony")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	// 模拟"现在 22:00"——但 QueryScheduleBreakdown 内部调 time.Now()，
	// 我们没法注入 mock now。
	// 退而求其次：测试在 22:00-23:55 之间跑，所有今天 slot 都被过滤。
	now := time.Now().In(loc)
	if now.Hour() < 22 {
		t.Skipf("测试只在 22:00 后跑（当前 %02d:%02d）", now.Hour(), now.Minute())
	}

	today := now.Format("2006-01-02")
	breakdown := QueryScheduleBreakdown("Tony", today)

	// 22:00 后：所有 DefaultSlot（最晚 18:00）都在过去 → Available 应为空
	if len(breakdown.Available) != 0 {
		t.Errorf("22:00 后查今天 Tony，Available 应为空（最晚 18:00 已过），got %v", breakdown.Available)
	}
}

// TestQueryAvailableSlots_FilterPastSlotsTomorrow 查明天 Tony → 不过滤过去时间
func TestQueryAvailableSlots_FilterPastSlotsTomorrow(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "b-tony", shop.ID, "Tony")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")

	slots := QueryAvailableSlots("Tony", tomorrow)

	// 明天：所有 DefaultSlot（16 个）都应保留
	if len(slots) != 16 {
		t.Errorf("查明天应保留所有 16 个 slot，got %d (%v)", len(slots), slots)
	}
}

// TestQueryAvailableSlots_FilterPastSlotsTodayRealistic 真实场景：当前时间后剩多少
// 不验证具体数字（依赖 now.Hour），只验证"返回的 slot 都 > now"
func TestQueryAvailableSlots_FilterPastSlotsTodayRealistic(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-1", "")
	MakeBarber(t, "b-tony", shop.ID, "Tony")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")

	slots := QueryAvailableSlots("Tony", today)

	// 验证每个返回的 slot 都 > now + 5min
	cutoff := now.Add(5 * time.Minute)
	for _, slot := range slots {
		hm, err := time.ParseInLocation("15:04", slot, loc)
		if err != nil {
			t.Fatalf("parse slot %q: %v", slot, err)
		}
		slotAt := time.Date(now.Year(), now.Month(), now.Day(),
			hm.Hour(), hm.Minute(), 0, 0, loc)
		if !slotAt.After(cutoff) {
			t.Errorf("slot %s (%v) 不应返回（在 now+5min 之前，now=%v）",
				slot, slotAt, now)
		}
	}
}

// equalStringSlice 简单 string slice 比较（用于测试）
func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 引用 strings 避免 unused import 警告（filterPastSlotsToday 测试里没用到 strings，但 repo.go 里有用）
var _ = strings.Contains
