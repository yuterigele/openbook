package storage

// usage_report_test.go
//
// BuildD15UsageReport 的单测覆盖（PRD §11.11 v4.2 D+15 使用报告）。
//
// 覆盖维度：
//   - 基础聚合：总览字段（total/completed/noshow/cancelled/active）+ 率
//   - 服务排行：多服务时按 count DESC，同 count 按 name 字典序
//   - 顾客排行：多顾客时按 total DESC
//   - 日趋势：缺失日期补 0；连续日期按 ASC 排
//   - 阶段对比：前 3 天 vs 后 12 天；增长率为正 / 零基线
//   - 边界：DB 未初始化 / shop 不存在 / 窗口为 0 / 跨时区
//
// 时间口径：所有测试 firstAppt / now 用固定 UTC 日期，避免真实 time.Now() 漂移。

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 固定测试时间窗：firstAppt = 2026-06-07 00:00 UTC, now = 2026-06-22 00:00 UTC
//   - 实际窗口 [2026-06-07, 2026-06-22) = 15 天
//   - 用 UTC 让所有断言可预测
var (
	d15TestFirstAppt = time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)
	d15TestNow       = time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
)

func TestBuildD15UsageReport_BasicAggregates(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "shop-basic"

	MakeShop(t, shopID, "")

	// 12 笔：6 completed + 3 noshow + 2 cancelled + 1 active
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-07", "10:00", "completed")
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-08", "10:00", "completed")
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-09", "10:00", "completed")
	mkAppt(t, shopID, "C2", "Bob", "Tony", "2026-06-10", "10:00", "completed")
	mkAppt(t, shopID, "C2", "Bob", "Tony", "2026-06-11", "10:00", "completed")
	mkAppt(t, shopID, "C3", "Cara", "Tony", "2026-06-12", "10:00", "completed")
	mkAppt(t, shopID, "C4", "Dan", "Tony", "2026-06-13", "10:00", "noshow")
	mkAppt(t, shopID, "C5", "Eve", "Tony", "2026-06-14", "10:00", "noshow")
	mkAppt(t, shopID, "C6", "Finn", "Tony", "2026-06-15", "10:00", "noshow")
	mkAppt(t, shopID, "C7", "Gina", "Tony", "2026-06-16", "10:00", "cancelled")
	mkAppt(t, shopID, "C8", "Hugo", "Tony", "2026-06-17", "10:00", "cancelled")
	mkAppt(t, shopID, "C9", "Ivy", "Tony", "2026-06-18", "10:00", "active")

	rep, err := BuildD15UsageReport(ctx, shopID, d15TestFirstAppt, d15TestNow)
	if err != nil {
		t.Fatalf("BuildD15UsageReport failed: %v", err)
	}

	if rep.TotalAppointments != 12 {
		t.Errorf("TotalAppointments: want 12, got %d", rep.TotalAppointments)
	}
	if rep.CompletedAppointments != 6 {
		t.Errorf("CompletedAppointments: want 6, got %d", rep.CompletedAppointments)
	}
	if rep.NoShowAppointments != 3 {
		t.Errorf("NoShowAppointments: want 3, got %d", rep.NoShowAppointments)
	}
	if rep.CancelledAppointments != 2 {
		t.Errorf("CancelledAppointments: want 2, got %d", rep.CancelledAppointments)
	}
	if rep.ActiveAppointments != 1 {
		t.Errorf("ActiveAppointments: want 1, got %d", rep.ActiveAppointments)
	}
	// completion_rate = 6 / (6+3) = 0.6667
	if !floatNear(rep.CompletionRate, 2.0/3.0, 0.001) {
		t.Errorf("CompletionRate: want 0.667, got %f", rep.CompletionRate)
	}
	// no_show_rate = 3 / (6+3) = 0.3333
	if !floatNear(rep.NoShowRate, 1.0/3.0, 0.001) {
		t.Errorf("NoShowRate: want 0.333, got %f", rep.NoShowRate)
	}
	if rep.WindowDays != 15 {
		t.Errorf("WindowDays: want 15, got %d", rep.WindowDays)
	}
}

func TestBuildD15UsageReport_ServiceRanking(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "shop-svc"

	MakeShop(t, shopID, "")

	// 染发 x4, 剪发 x3, 烫发 x2, 护理 x1
	for i := 0; i < 4; i++ {
		mkApptWithService(t, shopID, "C1", "Alice", "Tony", "2026-06-07", "10:00", "completed", "染发")
	}
	for i := 0; i < 3; i++ {
		mkApptWithService(t, shopID, "C1", "Alice", "Tony", "2026-06-08", "11:00", "completed", "剪发")
	}
	for i := 0; i < 2; i++ {
		mkApptWithService(t, shopID, "C1", "Alice", "Tony", "2026-06-09", "12:00", "completed", "烫发")
	}
	mkApptWithService(t, shopID, "C1", "Alice", "Tony", "2026-06-10", "13:00", "completed", "护理")

	rep, err := BuildD15UsageReport(ctx, shopID, d15TestFirstAppt, d15TestNow)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if rep.UniqueServices != 4 {
		t.Errorf("UniqueServices: want 4, got %d", rep.UniqueServices)
	}
	if len(rep.ServiceRank) != 4 {
		t.Fatalf("ServiceRank len: want 4, got %d", len(rep.ServiceRank))
	}
	want := []struct {
		name  string
		count int
	}{
		{"染发", 4},
		{"剪发", 3},
		{"烫发", 2},
		{"护理", 1},
	}
	for i, w := range want {
		if rep.ServiceRank[i].Service != w.name || rep.ServiceRank[i].Count != w.count {
			t.Errorf("ServiceRank[%d]: want %s/%d, got %s/%d",
				i, w.name, w.count, rep.ServiceRank[i].Service, rep.ServiceRank[i].Count)
		}
	}
}

func TestBuildD15UsageReport_CustomerRanking(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "shop-cust"

	MakeShop(t, shopID, "")

	// Alice 5 笔，Bob 3 笔，Cara 2 笔，Dan 1 笔
	for i := 0; i < 5; i++ {
		mkAppt(t, shopID, "C-alice", "Alice", "Tony", "2026-06-07", "10:00", "completed")
	}
	for i := 0; i < 3; i++ {
		mkAppt(t, shopID, "C-bob", "Bob", "Tony", "2026-06-08", "11:00", "completed")
	}
	for i := 0; i < 2; i++ {
		mkAppt(t, shopID, "C-cara", "Cara", "Tony", "2026-06-09", "12:00", "completed")
	}
	mkAppt(t, shopID, "C-dan", "Dan", "Tony", "2026-06-10", "13:00", "completed")

	rep, err := BuildD15UsageReport(ctx, shopID, d15TestFirstAppt, d15TestNow)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if rep.UniqueCustomers != 4 {
		t.Errorf("UniqueCustomers: want 4, got %d", rep.UniqueCustomers)
	}
	if len(rep.TopCustomers) != 4 {
		t.Fatalf("TopCustomers len: want 4, got %d", len(rep.TopCustomers))
	}
	if rep.TopCustomers[0].Name != "Alice" || rep.TopCustomers[0].Total != 5 {
		t.Errorf("TopCustomers[0]: want Alice/5, got %s/%d",
			rep.TopCustomers[0].Name, rep.TopCustomers[0].Total)
	}
	if rep.TopCustomers[1].Name != "Bob" || rep.TopCustomers[1].Total != 3 {
		t.Errorf("TopCustomers[1]: want Bob/3, got %s/%d",
			rep.TopCustomers[1].Name, rep.TopCustomers[1].Total)
	}
}

func TestBuildD15UsageReport_DailyTrend_FillsGaps(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "shop-daily"

	MakeShop(t, shopID, "")

	// 只在 6/7 和 6/9 有预约，6/8 应该有 0 行（补 0）
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-07", "10:00", "completed")
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-09", "10:00", "completed")

	rep, err := BuildD15UsageReport(ctx, shopID, d15TestFirstAppt, d15TestNow)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// firstAt=6/7, now=6/22 实际 15 天趋势
	if len(rep.DailyTrend) != 15 {
		t.Fatalf("DailyTrend len: want 15, got %d", len(rep.DailyTrend))
	}
	// 第一天应有数据
	if rep.DailyTrend[0].Date != "2026-06-07" || rep.DailyTrend[0].Total != 1 {
		t.Errorf("DailyTrend[0]: want 2026-06-07/1, got %s/%d",
			rep.DailyTrend[0].Date, rep.DailyTrend[0].Total)
	}
	// 第二天（6/8）应补 0
	if rep.DailyTrend[1].Date != "2026-06-08" || rep.DailyTrend[1].Total != 0 {
		t.Errorf("DailyTrend[1]: want 2026-06-08/0, got %s/%d",
			rep.DailyTrend[1].Date, rep.DailyTrend[1].Total)
	}
	// 第三天（6/9）有数据
	if rep.DailyTrend[2].Date != "2026-06-09" || rep.DailyTrend[2].Total != 1 {
		t.Errorf("DailyTrend[2]: want 2026-06-09/1, got %s/%d",
			rep.DailyTrend[2].Date, rep.DailyTrend[2].Total)
	}
}

func TestBuildD15UsageReport_PhaseComparison_PositiveGrowth(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "shop-phase"

	MakeShop(t, shopID, "")

	// 前 3 天（6/7~6/9）共 3 笔（基线 1/天）
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-07", "10:00", "completed")
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-08", "10:00", "completed")
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-09", "10:00", "completed")
	// 后 12 天（6/10~6/21）共 24 笔（增长 2/天）
	for i := 10; i <= 21; i++ {
		mkAppt(t, shopID, "C1", "Alice", "Tony", dayStr(i), "10:00", "completed")
		mkAppt(t, shopID, "C1", "Alice", "Tony", dayStr(i), "14:00", "completed")
	}

	rep, err := BuildD15UsageReport(ctx, shopID, d15TestFirstAppt, d15TestNow)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if rep.BaselineBaseline.Total != 3 {
		t.Errorf("Baseline.Total: want 3, got %d", rep.BaselineBaseline.Total)
	}
	if !floatNear(rep.BaselineBaseline.AvgPerDay, 1.0, 0.001) {
		t.Errorf("Baseline.AvgPerDay: want 1.0, got %f", rep.BaselineBaseline.AvgPerDay)
	}
	if rep.GrowthPhase.Total != 24 {
		t.Errorf("Growth.Total: want 24, got %d", rep.GrowthPhase.Total)
	}
	if !floatNear(rep.GrowthPhase.AvgPerDay, 2.0, 0.001) {
		t.Errorf("Growth.AvgPerDay: want 2.0, got %f", rep.GrowthPhase.AvgPerDay)
	}
	// delta avg = 1.0; growth rate = 1.0 / 1.0 = 1.0
	if !floatNear(rep.GrowthDelta.AvgPerDayDelta, 1.0, 0.001) {
		t.Errorf("Delta.AvgPerDayDelta: want 1.0, got %f", rep.GrowthDelta.AvgPerDayDelta)
	}
	if !floatNear(rep.GrowthDelta.GrowthRate, 1.0, 0.001) {
		t.Errorf("Delta.GrowthRate: want 1.0, got %f", rep.GrowthDelta.GrowthRate)
	}
}

func TestBuildD15UsageReport_PhaseComparison_ZeroBaseline(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "shop-zero-baseline"

	MakeShop(t, shopID, "")

	// 基线 0 笔；增长期 6 笔
	for i := 10; i <= 15; i++ {
		mkAppt(t, shopID, "C1", "Alice", "Tony", dayStr(i), "10:00", "completed")
	}

	rep, err := BuildD15UsageReport(ctx, shopID, d15TestFirstAppt, d15TestNow)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if rep.BaselineBaseline.Total != 0 {
		t.Errorf("Baseline.Total: want 0, got %d", rep.BaselineBaseline.Total)
	}
	if rep.GrowthPhase.Total != 6 {
		t.Errorf("Growth.Total: want 6, got %d", rep.GrowthPhase.Total)
	}
	// 基线为 0 时增长率无定义（应为 0，不应除零崩）
	if rep.GrowthDelta.GrowthRate != 0 {
		t.Errorf("Delta.GrowthRate with zero baseline: want 0, got %f", rep.GrowthDelta.GrowthRate)
	}
}

func TestBuildD15UsageReport_EmptyShop(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "shop-empty"

	MakeShop(t, shopID, "")

	rep, err := BuildD15UsageReport(ctx, shopID, d15TestFirstAppt, d15TestNow)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if rep.TotalAppointments != 0 {
		t.Errorf("TotalAppointments: want 0, got %d", rep.TotalAppointments)
	}
	if rep.CompletionRate != 0 || rep.NoShowRate != 0 {
		t.Errorf("rates: want 0/0, got %f/%f", rep.CompletionRate, rep.NoShowRate)
	}
	if rep.UniqueServices != 0 || rep.UniqueCustomers != 0 {
		t.Errorf("unique: want 0/0, got %d/%d", rep.UniqueServices, rep.UniqueCustomers)
	}
	if len(rep.DailyTrend) != 15 {
		t.Errorf("DailyTrend len: want 15, got %d", len(rep.DailyTrend))
	}
}

func TestBuildD15UsageReport_ShopNotFound(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	_, err := BuildD15UsageReport(ctx, "nonexistent", d15TestFirstAppt, d15TestNow)
	if err == nil {
		t.Fatal("want error for nonexistent shop, got nil")
	}
}

func TestBuildD15UsageReport_OutOfWindowExcluded(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "shop-window"

	MakeShop(t, shopID, "")

	// 在窗口内 1 笔
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-10", "10:00", "completed")
	// 在窗口外（firstAppt 之前）1 笔
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-06", "10:00", "completed")
	// 在窗口外（now 之后）1 笔
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-22", "10:00", "completed")

	rep, err := BuildD15UsageReport(ctx, shopID, d15TestFirstAppt, d15TestNow)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if rep.TotalAppointments != 1 {
		t.Errorf("TotalAppointments: want 1 (only in-window), got %d", rep.TotalAppointments)
	}
}

// ---- helpers ----

// mkAppt 创建一条 appointment（指定 shop/customer/barber/date/time/status）
func mkAppt(t *testing.T, shopID, custID, custName, barberName, date, timeStr, status string) {
	mkApptWithService(t, shopID, custID, custName, barberName, date, timeStr, status, "")
}

// mkApptWithService 创建一条 appointment + 指定 service
func mkApptWithService(t *testing.T, shopID, custID, custName, barberName, date, timeStr, status, service string) {
	t.Helper()
	a := &Appointment{
		ID:         uuid.NewString(),
		ShopID:     shopID,
		BarberID:   "barber-" + barberName,
		BarberName: barberName,
		CustomerID: custID,
		Customer:   custName,
		Date:       date,
		Time:       timeStr,
		Status:     status,
		Source:     "test",
		Service:    service,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := DB.Create(a).Error; err != nil {
		t.Fatalf("create appt: %v", err)
	}
}

// dayStr 构造 YYYY-MM-DD：2026-06-{day:02d}
//   - 用 day = 1..30
func dayStr(day int) string {
	return time.Date(2026, 6, day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

// floatNear 浮点比较容差
func floatNear(a, b, eps float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < eps
}

// ---- 周报（v4.3 PRD §11.12） ----
//
// 覆盖维度：
//   - 基础聚合：window=[now-7d, now)，总览字段 + 率
//   - 服务 / 顾客排行（同 D+15 口径）
//   - 日趋势：长度恒为 7；缺失日期补 0
//   - 边界：DB 未初始化 / shop 不存在 / 空店 / 窗口外预约不计入
//   - ListAllShopIDs：列全部店铺
//
// 测试时间窗：now = 2026-06-22 00:00 UTC → window = [2026-06-15, 2026-06-22)

var weeklyTestNow = time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)

func TestBuildWeeklyUsageReport_BasicAggregates(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "weekly-basic"

	MakeShop(t, shopID, "")

	// 7 笔覆盖上周：3 completed + 1 noshow + 1 cancelled + 2 active
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-15", "10:00", "completed")
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-16", "10:00", "completed")
	mkAppt(t, shopID, "C2", "Bob", "Tony", "2026-06-17", "10:00", "completed")
	mkAppt(t, shopID, "C3", "Cara", "Tony", "2026-06-18", "10:00", "noshow")
	mkAppt(t, shopID, "C4", "Dan", "Tony", "2026-06-19", "10:00", "cancelled")
	mkAppt(t, shopID, "C5", "Eve", "Tony", "2026-06-20", "10:00", "active")
	mkAppt(t, shopID, "C6", "Finn", "Tony", "2026-06-21", "10:00", "active")

	rep, err := BuildWeeklyUsageReport(ctx, shopID, weeklyTestNow)
	if err != nil {
		t.Fatalf("BuildWeeklyUsageReport failed: %v", err)
	}

	if rep.TotalAppointments != 7 {
		t.Errorf("TotalAppointments: want 7, got %d", rep.TotalAppointments)
	}
	if rep.CompletedAppointments != 3 {
		t.Errorf("CompletedAppointments: want 3, got %d", rep.CompletedAppointments)
	}
	if rep.NoShowAppointments != 1 {
		t.Errorf("NoShowAppointments: want 1, got %d", rep.NoShowAppointments)
	}
	if rep.CancelledAppointments != 1 {
		t.Errorf("CancelledAppointments: want 1, got %d", rep.CancelledAppointments)
	}
	if rep.ActiveAppointments != 2 {
		t.Errorf("ActiveAppointments: want 2, got %d", rep.ActiveAppointments)
	}
	// completion_rate = 3 / (3+1) = 0.75
	if !floatNear(rep.CompletionRate, 0.75, 0.001) {
		t.Errorf("CompletionRate: want 0.75, got %f", rep.CompletionRate)
	}
	// no_show_rate = 1 / (3+1) = 0.25
	if !floatNear(rep.NoShowRate, 0.25, 0.001) {
		t.Errorf("NoShowRate: want 0.25, got %f", rep.NoShowRate)
	}
	if rep.WindowDays != 7 {
		t.Errorf("WindowDays: want 7, got %d", rep.WindowDays)
	}
	// windowStart = now - 7d
	wantStart := weeklyTestNow.AddDate(0, 0, -7)
	if !rep.WindowStart.Equal(wantStart) {
		t.Errorf("WindowStart: want %v, got %v", wantStart, rep.WindowStart)
	}
	if !rep.WindowEnd.Equal(weeklyTestNow) {
		t.Errorf("WindowEnd: want %v, got %v", weeklyTestNow, rep.WindowEnd)
	}
}

func TestBuildWeeklyUsageReport_ServiceAndCustomerRanking(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "weekly-rank"

	MakeShop(t, shopID, "")

	// 服务分布：剪发 x4、染发 x2、烫发 x1
	for i := 15; i <= 18; i++ {
		mkApptWithService(t, shopID, "C1", "Alice", "Tony", dayStr(i), "10:00", "completed", "剪发")
	}
	mkApptWithService(t, shopID, "C2", "Bob", "Tony", "2026-06-19", "10:00", "completed", "染发")
	mkApptWithService(t, shopID, "C2", "Bob", "Tony", "2026-06-20", "10:00", "completed", "染发")
	mkApptWithService(t, shopID, "C3", "Cara", "Tony", "2026-06-21", "10:00", "completed", "烫发")

	rep, err := BuildWeeklyUsageReport(ctx, shopID, weeklyTestNow)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if rep.UniqueServices != 3 {
		t.Errorf("UniqueServices: want 3, got %d", rep.UniqueServices)
	}
	if len(rep.ServiceRank) != 3 {
		t.Fatalf("ServiceRank len: want 3, got %d", len(rep.ServiceRank))
	}
	if rep.ServiceRank[0].Service != "剪发" || rep.ServiceRank[0].Count != 4 {
		t.Errorf("ServiceRank[0]: want 剪发/4, got %s/%d", rep.ServiceRank[0].Service, rep.ServiceRank[0].Count)
	}

	// 顾客排行：Alice 4 / Bob 2 / Cara 1
	if rep.UniqueCustomers != 3 {
		t.Errorf("UniqueCustomers: want 3, got %d", rep.UniqueCustomers)
	}
	if rep.TopCustomers[0].Name != "Alice" || rep.TopCustomers[0].Total != 4 {
		t.Errorf("TopCustomers[0]: want Alice/4, got %s/%d", rep.TopCustomers[0].Name, rep.TopCustomers[0].Total)
	}
}

func TestBuildWeeklyUsageReport_DailyTrend_FillsGaps(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "weekly-daily"

	MakeShop(t, shopID, "")

	// 只有 6/16 和 6/19 有预约，其余 5 天应补 0
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-16", "10:00", "completed")
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-16", "14:00", "completed")
	mkAppt(t, shopID, "C2", "Bob", "Tony", "2026-06-19", "11:00", "completed")

	rep, err := BuildWeeklyUsageReport(ctx, shopID, weeklyTestNow)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// 7 天趋势：now=2026-06-22, window=[06-15, 06-22)
	wantDates := []string{
		"2026-06-15", "2026-06-16", "2026-06-17", "2026-06-18",
		"2026-06-19", "2026-06-20", "2026-06-21",
	}
	if len(rep.DailyTrend) != 7 {
		t.Fatalf("DailyTrend len: want 7, got %d", len(rep.DailyTrend))
	}
	for i, want := range wantDates {
		if rep.DailyTrend[i].Date != want {
			t.Errorf("DailyTrend[%d].Date: want %s, got %s", i, want, rep.DailyTrend[i].Date)
		}
	}
	// 6/15 补 0
	if rep.DailyTrend[0].Total != 0 {
		t.Errorf("DailyTrend[0].Total: want 0, got %d", rep.DailyTrend[0].Total)
	}
	// 6/16 有 2 笔
	if rep.DailyTrend[1].Total != 2 {
		t.Errorf("DailyTrend[1].Total: want 2, got %d", rep.DailyTrend[1].Total)
	}
	// 6/17 补 0
	if rep.DailyTrend[2].Total != 0 {
		t.Errorf("DailyTrend[2].Total: want 0, got %d", rep.DailyTrend[2].Total)
	}
	// 6/19 有 1 笔
	if rep.DailyTrend[4].Total != 1 {
		t.Errorf("DailyTrend[4].Total: want 1, got %d", rep.DailyTrend[4].Total)
	}
}

func TestBuildWeeklyUsageReport_EmptyShop(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "weekly-empty"

	MakeShop(t, shopID, "")

	rep, err := BuildWeeklyUsageReport(ctx, shopID, weeklyTestNow)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if rep.TotalAppointments != 0 {
		t.Errorf("TotalAppointments: want 0, got %d", rep.TotalAppointments)
	}
	if rep.CompletionRate != 0 || rep.NoShowRate != 0 {
		t.Errorf("rates: want 0/0, got %f/%f", rep.CompletionRate, rep.NoShowRate)
	}
	if len(rep.DailyTrend) != 7 {
		t.Errorf("DailyTrend len: want 7, got %d", len(rep.DailyTrend))
	}
	// 7 天都应该是 0
	for i, ds := range rep.DailyTrend {
		if ds.Total != 0 {
			t.Errorf("DailyTrend[%d]: want 0, got %d", i, ds.Total)
		}
	}
}

func TestBuildWeeklyUsageReport_ShopNotFound(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	_, err := BuildWeeklyUsageReport(ctx, "nonexistent", weeklyTestNow)
	if err == nil {
		t.Fatal("want error for nonexistent shop, got nil")
	}
}

func TestBuildWeeklyUsageReport_DBNotInitialized(t *testing.T) {
	DB = nil
	defer func() { DB = nil }()

	_, err := BuildWeeklyUsageReport(context.Background(), "any", weeklyTestNow)
	if err == nil {
		t.Fatal("want error when DB nil, got nil")
	}
}

func TestBuildWeeklyUsageReport_OutOfWindowExcluded(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "weekly-window"

	MakeShop(t, shopID, "")

	// 窗口内 1 笔（6/16）
	mkAppt(t, shopID, "C1", "Alice", "Tony", "2026-06-16", "10:00", "completed")
	// 窗口外（now 之后 — 6/22 算今天）1 笔
	mkAppt(t, shopID, "C2", "Bob", "Tony", "2026-06-22", "10:00", "completed")
	// 窗口外（now-7d 之前）1 笔 — 6/14
	mkAppt(t, shopID, "C3", "Cara", "Tony", "2026-06-14", "10:00", "completed")

	rep, err := BuildWeeklyUsageReport(ctx, shopID, weeklyTestNow)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if rep.TotalAppointments != 1 {
		t.Errorf("TotalAppointments: want 1 (only in-window), got %d", rep.TotalAppointments)
	}
}

func TestBuildWeeklyUsageReport_ServiceRankLimitApplied(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	shopID := "weekly-limit"

	MakeShop(t, shopID, "")

	// 6 种服务各 1 笔（只 1 次 / 服务），应有 6 个不同服务；RankLimit=5 应截断
	for i, svc := range []string{"剪发", "染发", "烫发", "洗吹", "护理", "造型"} {
		mkApptWithService(t, shopID, "C1", "Alice", "Tony", dayStr(15+i%7), "10:00", "completed", svc)
	}

	rep, err := BuildWeeklyUsageReport(ctx, shopID, weeklyTestNow)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if rep.UniqueServices != 6 {
		t.Errorf("UniqueServices: want 6, got %d", rep.UniqueServices)
	}
	if len(rep.ServiceRank) != 5 {
		t.Errorf("ServiceRank len: want 5 (RankLimit), got %d", len(rep.ServiceRank))
	}
}

func TestListAllShopIDs(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()

	MakeShop(t, "shop-a", "")
	MakeShop(t, "shop-b", "")
	MakeShop(t, "shop-c", "")

	ids, err := ListAllShopIDs(ctx)
	if err != nil {
		t.Fatalf("ListAllShopIDs: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("want 3 ids, got %d", len(ids))
	}

	// DB nil 时返 nil
	DB = nil
	defer func() { DB = nil }()
	ids2, err2 := ListAllShopIDs(ctx)
	if err2 != nil {
		t.Errorf("DB nil should not error, got %v", err2)
	}
	if ids2 != nil {
		t.Errorf("DB nil should return nil, got %v", ids2)
	}
}
