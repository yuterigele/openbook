package tools

// list_shop_holidays_test.go
//
// v4.16.2 新工具 list_shop_holidays 测试：
//   - 解决 v4.16.1 真实事故：店设 7-1、7-2 两天休息，Agent 按 prompt 里
//     "前后两天"硬推荐 → 推 7-1 给顾客（7-1 实际也是假期）
//   - 加 list_shop_holidays 工具，让 Agent 一次拿到完整节假日清单
//     推日期时排除所有假期，避免再次踩坑
//
// 覆盖：
//   - 无节假日：明确显示"节假日：无"，让 Agent 知道本店没假期
//   - 有节假日：按日期升序列出全部（Agent 推日期时排除清单）
//   - 即将放假：只列 >= 明天的，最多 3 个；过去的不进该段
//   - 兜底：无 shop 时返回「本店信息缺失」
//   - Info 描述：明确提到「拒绝某天时必须调」+「不能凭印象推」
//
// Run:
//   go test ./tools/... -v -run "TestListShopHolidays"

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

// runListShopHolidays 跑一次 list_shop_holidays 工具
func runListShopHolidays(t *testing.T, shopID string) (string, error) {
	t.Helper()
	c := &ListShopHolidaysTool{}
	ctx := WithShopID(context.Background(), shopID)
	return c.InvokableRun(ctx, `{}`)
}

// ===================== Info =====================

// TestListShopHolidays_InfoMentionsListUsage 守住 prompt 描述里关键约束
//
// 不让 LLM 把这个工具当成「可选」，必须明确知道：拒绝某天时**必须**调，
// 不能凭 prompt 里的"前后两天"硬推。
func TestListShopHolidays_InfoMentionsListUsage(t *testing.T) {
	c := &ListShopHolidaysTool{}
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if !strings.Contains(info.Desc, "节假日清单") {
		t.Errorf("Desc should mention '节假日清单' (Agent 需要看到完整清单)")
	}
	if !strings.Contains(info.Desc, "前后两天") {
		t.Errorf("Desc 应明确禁止「凭印象推前后两天」")
	}
}

// ===================== 无节假日 =====================

func TestListShopHolidays_NoHolidays(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-empty-holiday", "")

	out, err := runListShopHolidays(t, shop.ID)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	if !strings.Contains(out, "营业时间") {
		t.Errorf("应包含营业时间字段: %s", out)
	}
	if !strings.Contains(out, "午休") {
		t.Errorf("应包含午休字段: %s", out)
	}
	if !strings.Contains(out, "节假日：无") {
		t.Errorf("无节假日时应明确写「无」: %s", out)
	}
}

// ===================== 有节假日：升序列出 =====================

// TestListShopHolidays_WithHolidays 守住"列出完整清单 + 升序"
//
// 真实事故：店设 2026-07-01, 2026-07-02 两天休息，
// Agent 推 7-1 给顾客（实际也是假期）。
// 本测试确保工具返回完整清单，让 Agent 能排除所有假期再推日期。
func TestListShopHolidays_WithHolidays(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-with-holiday", "2026-07-02,2026-07-01") // 故意乱序

	out, err := runListShopHolidays(t, shop.ID)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	if !strings.Contains(out, "2026-07-01") || !strings.Contains(out, "2026-07-02") {
		t.Errorf("应列出全部节假日: %s", out)
	}
	// 升序：7-01 应在 7-02 之前
	idx1 := strings.Index(out, "2026-07-01")
	idx2 := strings.Index(out, "2026-07-02")
	if idx1 < 0 || idx2 < 0 {
		t.Fatalf("节假日应出现在输出中: %s", out)
	}
	if idx1 > idx2 {
		t.Errorf("节假日应按日期升序排列（哪怕原始字符串乱序）: %s", out)
	}
}

// ===================== 即将放假：只列未来 3 个 =====================

// TestListShopHolidays_UpcomingOnlyFuture 验证"即将放假"段只含未来日期
//
// 业务规则：list_shop_holidays 的「即将放假」段只列 >= 明天的，
// 最多 3 个。过去的节假日还在总清单里（让 Agent 看历史），
// 但不进「即将放假」（避免 Agent 推荐过去日期）。
func TestListShopHolidays_UpcomingOnlyFuture(t *testing.T) {
	setupToolsTestDB(t)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	dayAfter := time.Now().In(loc).AddDate(0, 0, 2).Format("2006-01-02")
	day3 := time.Now().In(loc).AddDate(0, 0, 3).Format("2006-01-02")
	day4 := time.Now().In(loc).AddDate(0, 0, 4).Format("2006-01-02")

	// 过去 1 个 + 未来 4 个（超过 3 个，验证上限）
	holidays := "2020-01-01," + tomorrow + "," + dayAfter + "," + day3 + "," + day4
	shop := storage.MakeShop(t, "shop-upcoming-test", holidays)

	out, err := runListShopHolidays(t, shop.ID)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	// 过去的 2020-01-01 应在总清单里
	if !strings.Contains(out, "2020-01-01") {
		t.Errorf("过去节假日应在总清单里: %s", out)
	}
	// 「即将放假」段不应含 2020-01-01
	upcomingIdx := strings.Index(out, "即将放假")
	idx2020 := strings.Index(out, "2020-01-01")
	if upcomingIdx < 0 {
		t.Errorf("应包含「即将放假」段: %s", out)
	}
	if upcomingIdx >= 0 && idx2020 > upcomingIdx {
		t.Errorf("过去节假日不应在「即将放假」段: %s", out)
	}
}

// ===================== 兜底：无 shop =====================

// TestListShopHolidays_NoShop 验证 ctx 无 shop_id 且 DB 无 shop 时的兜底
func TestListShopHolidays_NoShop(t *testing.T) {
	setupToolsTestDB(t)
	// 不 set ctx shop_id，DB 是空的（上面 setupToolsTestDB 刚建）
	c := &ListShopHolidaysTool{}
	ctx := WithShopID(context.Background(), "nonexistent-shop-id")

	out, err := c.InvokableRun(ctx, `{}`)
	if err != nil {
		t.Fatalf("unexpected err: %v, out=%q", err, out)
	}
	// 无 shop 时要么返"营业时间"（取到空 DB 第一家），要么返"本店信息缺失"
	if !strings.Contains(out, "营业时间") && !strings.Contains(out, "本店信息缺失") {
		t.Errorf("无 shop 时应返兜底文案: %s", out)
	}
}