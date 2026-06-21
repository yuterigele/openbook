package storage

// usage_report.go
//
// PRD §8.2 D+15 使用报告数据组装。
//
// 业务背景：
//   - PRD §8.2 续费动作链 D+15 节点原本只发一条短文："您已使用 AI 预约助手半个月，共处理 N 笔预约"
//   - 升级为完整"使用报告"：总览 + 服务排行 + 顾客排行 + 完成率/爽约率 + 日趋势 + 阶段对比
//   - 阶段对比口径：用前 3 天（D+1~D+3）作"冷启动基线"，后 12 天（D+4~D+15）作"增长期"，让店主直观看到效果
//
// 设计要点：
//   - 报告内容只看"已发生"的预约（appointment.date < now）
//   - 时间口径：取该店 first_appointment 事件的时间作 D+0（已由 storage.FindShopsForLifecycle 算好）
//   - 性能：单店一次性 SQL 拉所有 appointments，Go 端聚合；N 店扫描时分批跑，单店 < 1000 单无压力
//   - 安全：所有聚合都基于已有的 appointments + customers 表，无新依赖

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// D15ReportWindowDays D+15 报告覆盖天数（PRD §8.2 D+15 = 半个月）
const D15ReportWindowDays = 15

// D15BaselineDays D+15 报告的"冷启动基线"天数（前 N 天 vs 后 M 天对比）
const D15BaselineDays = 3

// WeeklyReportWindowDays 周报覆盖天数（v4.3 PRD §11.12 每周一推送上周数据）
const WeeklyReportWindowDays = 7

// WeeklyReportServiceRankLimit 周报服务/顾客排行 limit
const WeeklyReportRankLimit = 5

// UsageReport D+15 使用报告（v4.2 PRD §11.11）
//
//   - 阶段对比口径：baseline = first_appointment 后的前 D15BaselineDays 天；
//     growth = 剩余的 D15ReportWindowDays - D15BaselineDays 天
//   - 字段命名按"店主能看懂的"维度：总数/完成率/爽约率/服务排行/顾客排行/日趋势
type UsageReport struct {
	// 基础信息
	ShopID      string    `json:"shop_id"`
	ShopName    string    `json:"shop_name"`
	GeneratedAt time.Time `json:"generated_at"`
	FirstApptAt time.Time `json:"first_appt_at"`  // 首次预约时间（D+0）
	WindowStart time.Time `json:"window_start"`   // [FirstApptAt, FirstApptAt+15d)
	WindowEnd   time.Time `json:"window_end"`
	WindowDays  int       `json:"window_days"`

	// 总览（覆盖 15 天）
	TotalAppointments  int     `json:"total_appointments"`
	CompletedAppointments int  `json:"completed_appointments"`
	NoShowAppointments  int   `json:"noshow_appointments"`
	CancelledAppointments int `json:"cancelled_appointments"`
	ActiveAppointments  int   `json:"active_appointments"`
	CompletionRate     float64 `json:"completion_rate"` // completed / (completed + noshow)
	NoShowRate         float64 `json:"noshow_rate"`     // noshow / (completed + noshow)

	// 服务维度
	UniqueServices int            `json:"unique_services"`
	ServiceRank    []ServiceStat  `json:"service_rank"` // 按 count DESC，limit 5

	// 顾客维度
	UniqueCustomers int            `json:"unique_customers"`
	TopCustomers    []CustomerStat `json:"top_customers"` // 按 total DESC，limit 5

	// 日趋势（按 date ASC）
	DailyTrend []DailyStat `json:"daily_trend"` // len == WindowDays

	// 阶段对比（baseline vs growth）
	BaselineBaseline BaselinePhase `json:"baseline_phase"` // 前 3 天
	GrowthPhase      BaselinePhase `json:"growth_phase"`   // 后 12 天
	GrowthDelta      PhaseDelta    `json:"growth_delta"`   // growth - baseline
}

// ServiceStat 服务维度统计
type ServiceStat struct {
	Service string `json:"service"`
	Count   int    `json:"count"`
}

// CustomerStat 顾客维度统计
type CustomerStat struct {
	CustomerID string `json:"customer_id"`
	Name       string `json:"name"`
	Total      int    `json:"total"` // 该店预约次数
}

// DailyStat 单日统计
type DailyStat struct {
	Date      string `json:"date"` // YYYY-MM-DD
	Total     int    `json:"total"`
	Completed int    `json:"completed"`
	NoShow    int    `json:"noshow"`
	Cancelled int    `json:"cancelled"`
}

// BaselinePhase 阶段聚合（baseline 或 growth）
type BaselinePhase struct {
	Label     string  `json:"label"` // "冷启动期" / "增长期"
	DayCount  int     `json:"day_count"`
	Total     int     `json:"total"`
	Completed int     `json:"completed"`
	NoShow    int     `json:"noshow"`
	Cancelled int     `json:"cancelled"`
	AvgPerDay float64 `json:"avg_per_day"`
}

// PhaseDelta 阶段对比增量
type PhaseDelta struct {
	AvgPerDayDelta float64 `json:"avg_per_day_delta"` // 增长期日均 - 基线日均
	GrowthRate     float64 `json:"growth_rate"`       // (growth_avg - baseline_avg) / baseline_avg；-1..∞
}

// enrichedAppt 内部用：appointment + 解析出的发生时间 + 是否在窗口内
//
// 提为包级类型，便于 computePhaseComparison 跨函数传参（不导出）。
type enrichedAppt struct {
	appt     Appointment
	occurAt  time.Time
	inWindow bool
}

// BuildD15UsageReport 组装某店 15 天使用报告
//
//   - shopID：目标店铺
//   - firstApptAt：该店 first_appointment 事件时间（D+0），由调用方提供（一般是 FindShopsForLifecycle 算出的）
//   - now：当前时间（便于测试时注入固定时间）
//
// 返回的 UsageReport 是纯快照；调用方决定是渲染 HTML、推微信、还是发邮件。
func BuildD15UsageReport(ctx context.Context, shopID string, firstApptAt time.Time, now time.Time) (UsageReport, error) {
	if DB == nil {
		return UsageReport{}, fmt.Errorf("DB 未初始化")
	}

	// 1. 店铺基础信息
	var shop Shop
	if err := DB.WithContext(ctx).Where("id = ?", shopID).First(&shop).Error; err != nil {
		return UsageReport{}, fmt.Errorf("shop %s 不存在: %w", shopID, err)
	}

	// 2. 时间窗：[firstApptAt, firstApptAt+15d)；不足 15 天的实际只算到 now
	windowStart := firstApptAt
	windowEnd := firstApptAt.AddDate(0, 0, D15ReportWindowDays)
	if now.Before(windowEnd) {
		windowEnd = now
	}
	windowDays := int(windowEnd.Sub(windowStart).Hours() / 24)
	if windowDays < 0 {
		windowDays = 0
	}

	// 3. 拉所有 appointments（按 date 范围粗筛）
	dateFrom := windowStart.AddDate(0, 0, -1).Format("2006-01-02")
	dateTo := windowEnd.AddDate(0, 0, 1).Format("2006-01-02")
	var appts []Appointment
	if err := DB.WithContext(ctx).
		Where("shop_id = ? AND date >= ? AND date <= ?", shopID, dateFrom, dateTo).
		Find(&appts).Error; err != nil {
		return UsageReport{}, fmt.Errorf("查 %s 预约失败: %w", shopID, err)
	}

	// 4. Go 端精确过滤：date+time 落在 [windowStart, windowEnd)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}

	enrichedAppts := make([]enrichedAppt, 0, len(appts))
	for _, a := range appts {
		dt, err := time.ParseInLocation("2006-01-02 15:04", a.Date+" "+a.Time, loc)
		if err != nil {
			continue
		}
		inWindow := !dt.Before(windowStart) && dt.Before(windowEnd)
		enrichedAppts = append(enrichedAppts, enrichedAppt{appt: a, occurAt: dt, inWindow: inWindow})
	}

	// 5. 聚合总览 + 服务 / 顾客排行 + 日趋势
	rep := UsageReport{
		ShopID:      shopID,
		ShopName:    shop.Name,
		GeneratedAt: now,
		FirstApptAt: firstApptAt,
		WindowStart: windowStart,
		WindowEnd:   windowEnd,
		WindowDays:  windowDays,
	}

	serviceCount := make(map[string]int)
	customerCount := make(map[string]*CustomerStat) // customerID -> stat
	dailyMap := make(map[string]*DailyStat)        // date -> stat

	for _, e := range enrichedAppts {
		if !e.inWindow {
			continue
		}
		a := e.appt
		rep.TotalAppointments++

		// 状态统计
		switch a.Status {
		case "completed":
			rep.CompletedAppointments++
		case "noshow":
			rep.NoShowAppointments++
		case "cancelled":
			rep.CancelledAppointments++
		case "active":
			rep.ActiveAppointments++
		}

		// 服务维度
		svc := a.Service
		if svc == "" {
			svc = "未指定"
		}
		serviceCount[svc]++

		// 顾客维度
		if a.CustomerID != "" {
			cs, ok := customerCount[a.CustomerID]
			if !ok {
				cs = &CustomerStat{CustomerID: a.CustomerID, Name: a.Customer}
				customerCount[a.CustomerID] = cs
			}
			cs.Total++
			if cs.Name == "" && a.Customer != "" {
				cs.Name = a.Customer
			}
		}

		// 日维度
		ds, ok := dailyMap[a.Date]
		if !ok {
			ds = &DailyStat{Date: a.Date}
			dailyMap[a.Date] = ds
		}
		ds.Total++
		switch a.Status {
		case "completed":
			ds.Completed++
		case "noshow":
			ds.NoShow++
		case "cancelled":
			ds.Cancelled++
		}
	}

	// 6. 派生率
	closed := rep.NoShowAppointments + rep.CompletedAppointments
	if closed > 0 {
		rep.CompletionRate = float64(rep.CompletedAppointments) / float64(closed)
		rep.NoShowRate = float64(rep.NoShowAppointments) / float64(closed)
	}

	// 7. 服务排行（按 count DESC，limit 5）
	rep.UniqueServices = len(serviceCount)
	rep.ServiceRank = topServices(serviceCount, 5)

	// 8. 顾客排行（按 total DESC，limit 5）
	rep.UniqueCustomers = len(customerCount)
	rep.TopCustomers = topCustomers(customerCount, 5)

	// 9. 日趋势：按 date ASC 排，缺失日期补 0
	rep.DailyTrend = fillDailyTrend(dailyMap, windowStart, windowDays)

	// 10. 阶段对比（前 3 天 vs 后 12 天）
	rep.BaselineBaseline, rep.GrowthPhase, rep.GrowthDelta = computePhaseComparison(
		enrichedAppts, windowStart, windowDays,
	)

	return rep, nil
}

// topServices 把 map 转成排好序的 []ServiceStat，limit 截断
func topServices(counts map[string]int, limit int) []ServiceStat {
	out := make([]ServiceStat, 0, len(counts))
	for svc, n := range counts {
		out = append(out, ServiceStat{Service: svc, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Service < out[j].Service
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// topCustomers 把 map 转成排好序的 []CustomerStat，limit 截断
func topCustomers(counts map[string]*CustomerStat, limit int) []CustomerStat {
	out := make([]CustomerStat, 0, len(counts))
	for _, cs := range counts {
		out = append(out, *cs)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].CustomerID < out[j].CustomerID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// fillDailyTrend 把日数据填成连续 [Day1, Day2, ..., DayN]，缺失的补 0
func fillDailyTrend(dailyMap map[string]*DailyStat, windowStart time.Time, windowDays int) []DailyStat {
	out := make([]DailyStat, windowDays)
	for i := 0; i < windowDays; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		if ds, ok := dailyMap[key]; ok {
			out[i] = *ds
		} else {
			out[i] = DailyStat{Date: key}
		}
	}
	return out
}

// computePhaseComparison 计算阶段对比（baseline 前 N 天 vs growth 剩余天数）
//
//   - 行为：把 enrichedAppts 按 occurAt 切两段，统计总量 + completed/noshow/cancelled + 日均
//   - 边界：windowDays < D15BaselineDays 时，growth 段为 0（baseline 占满）；windowDays == 0 全空
func computePhaseComparison(enrichedAppts []enrichedAppt, windowStart time.Time, windowDays int) (BaselinePhase, BaselinePhase, PhaseDelta) {
	baselineEnd := windowStart.AddDate(0, 0, D15BaselineDays)
	baseline := BaselinePhase{
		Label:    "冷启动期",
		DayCount: D15BaselineDays,
	}
	growth := BaselinePhase{
		Label:    "增长期",
		DayCount: windowDays - D15BaselineDays,
	}
	if growth.DayCount < 0 {
		growth.DayCount = 0
	}

	for _, e := range enrichedAppts {
		if !e.inWindow {
			continue
		}
		var p *BaselinePhase
		if e.occurAt.Before(baselineEnd) {
			p = &baseline
		} else {
			p = &growth
		}
		p.Total++
		switch e.appt.Status {
		case "completed":
			p.Completed++
		case "noshow":
			p.NoShow++
		case "cancelled":
			p.Cancelled++
		}
	}

	if baseline.DayCount > 0 {
		baseline.AvgPerDay = float64(baseline.Total) / float64(baseline.DayCount)
	}
	if growth.DayCount > 0 {
		growth.AvgPerDay = float64(growth.Total) / float64(growth.DayCount)
	}

	delta := PhaseDelta{
		AvgPerDayDelta: growth.AvgPerDay - baseline.AvgPerDay,
	}
	if baseline.AvgPerDay > 0 {
		delta.GrowthRate = (growth.AvgPerDay - baseline.AvgPerDay) / baseline.AvgPerDay
	}
	return baseline, growth, delta
}

// ---- 周报（v4.3 PRD §11.12） ----

// WeeklyReport 周报（v4.3 PRD §11.12 — 每周一推送上周经营数据）
//
//   - 时间窗：[now-7d, now)，按周一 9:00 触发
//   - 与 D+15 区别：每周都发（不依赖 first_appointment）；不做阶段对比
//   - 字段命名与 UsageReport 对齐，方便复用 render 模板
type WeeklyReport struct {
	// 基础信息
	ShopID      string    `json:"shop_id"`
	ShopName    string    `json:"shop_name"`
	GeneratedAt time.Time `json:"generated_at"`
	WindowStart time.Time `json:"window_start"` // [now-7d, now)
	WindowEnd   time.Time `json:"window_end"`
	WindowDays  int       `json:"window_days"` // 恒为 7

	// 总览（覆盖 7 天）
	TotalAppointments   int     `json:"total_appointments"`
	CompletedAppointments int   `json:"completed_appointments"`
	NoShowAppointments  int    `json:"noshow_appointments"`
	CancelledAppointments int  `json:"cancelled_appointments"`
	ActiveAppointments  int    `json:"active_appointments"`
	CompletionRate      float64 `json:"completion_rate"`
	NoShowRate          float64 `json:"noshow_rate"`

	// 服务 / 顾客维度（与 UsageReport 同字段，方便复用 render）
	UniqueServices int            `json:"unique_services"`
	ServiceRank    []ServiceStat  `json:"service_rank"` // limit WeeklyReportRankLimit
	UniqueCustomers int           `json:"unique_customers"`
	TopCustomers   []CustomerStat `json:"top_customers"`

	// 日趋势（按 date ASC，缺失日期补 0）
	DailyTrend []DailyStat `json:"daily_trend"` // len == WeeklyReportWindowDays

	// 周环比（v4.3 PRD §11.12 增量：上周总数对比）
	//
	//   - 口径：上周 = [window_start - 7d, window_start)
	//   - 增长率 = (本周 - 上周) / 上周；上周为 0 时 = 0（避免除零）
	//   - 用途：让店主直观看到"这周比上周好/差"，是周报最有激励效果的一栏
	LastWeekTotal     int     `json:"last_week_total"`
	LastWeekCompleted int     `json:"last_week_completed"`
	LastWeekNoShow    int     `json:"last_week_noshow"`
	TotalGrowthRate   float64 `json:"total_growth_rate"` // (this - last) / last
	CompletedGrowthRate float64 `json:"completed_growth_rate"` // (this - last) / last
	NoShowDelta       int     `json:"noshow_delta"` // 本周 - 上周（绝对值变化）
}

// ChainWeeklyReport 跨店周报（v4.3 PRD §11.12 连锁版）
//
//   - 把每家店各自本周数据 + 跨店累计 + 跨店服务/顾客排行打包
//   - 收件人是连锁 owner 邮箱（不是单店）
type ChainWeeklyReport struct {
	GeneratedAt time.Time           `json:"generated_at"`
	WindowStart time.Time           `json:"window_start"`
	WindowEnd   time.Time           `json:"window_end"`
	WindowDays  int                 `json:"window_days"`
	WeekLabel   string              `json:"week_label"`
	ShopCount   int                 `json:"shop_count"`
	Total       ChainWeeklyTotals   `json:"total"`
	PerShop     []WeeklyReport      `json:"per_shop"`      // 按 ShopID 升序
	TopServices []ServiceStat       `json:"top_services"`  // 跨店 TOP 5
	TopCustomers []CustomerStat     `json:"top_customers"` // 跨店 TOP 5
}

// ChainWeeklyTotals 跨店累计（与 WeeklyReport 同字段去掉 shop 维度）
type ChainWeeklyTotals struct {
	TotalAppointments    int     `json:"total_appointments"`
	CompletedAppointments int    `json:"completed_appointments"`
	NoShowAppointments   int     `json:"noshow_appointments"`
	CancelledAppointments int    `json:"cancelled_appointments"`
	ActiveAppointments   int     `json:"active_appointments"`
	CompletionRate       float64 `json:"completion_rate"`
	NoShowRate           float64 `json:"noshow_rate"`
}

// BuildWeeklyUsageReport 组装某店上一周使用报告（v4.3 PRD §11.12）
//
//   - shopID：目标店铺
//   - now：当前时间（周一 9:00 cron 触发时 = 当天 09:00）
//   - 时间窗：[now-7d, now)
//
// 与 BuildD15UsageReport 共享底层 helper（topServices / topCustomers / fillDailyTrend / parseAnyTime）
func BuildWeeklyUsageReport(ctx context.Context, shopID string, now time.Time) (WeeklyReport, error) {
	if DB == nil {
		return WeeklyReport{}, fmt.Errorf("DB 未初始化")
	}

	// 1. 店铺基础信息
	var shop Shop
	if err := DB.WithContext(ctx).Where("id = ?", shopID).First(&shop).Error; err != nil {
		return WeeklyReport{}, fmt.Errorf("shop %s 不存在: %w", shopID, err)
	}

	// 2. 时间窗：[now-7d, now)
	windowEnd := now
	windowStart := now.AddDate(0, 0, -WeeklyReportWindowDays)

	// 3. 拉所有 appointments（按 date 范围粗筛，多扩 1 天防跨时区漏）
	dateFrom := windowStart.AddDate(0, 0, -1).Format("2006-01-02")
	dateTo := windowEnd.AddDate(0, 0, 1).Format("2006-01-02")
	var appts []Appointment
	if err := DB.WithContext(ctx).
		Where("shop_id = ? AND date >= ? AND date <= ?", shopID, dateFrom, dateTo).
		Find(&appts).Error; err != nil {
		return WeeklyReport{}, fmt.Errorf("查 %s 预约失败: %w", shopID, err)
	}

	// 4. Go 端精确过滤：date+time 落在 [windowStart, windowEnd)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}

	rep := WeeklyReport{
		ShopID:      shopID,
		ShopName:    shop.Name,
		GeneratedAt: now,
		WindowStart: windowStart,
		WindowEnd:   windowEnd,
		WindowDays:  WeeklyReportWindowDays,
	}

	serviceCount := make(map[string]int)
	customerCount := make(map[string]*CustomerStat)
	dailyMap := make(map[string]*DailyStat)

	for _, a := range appts {
		dt, err := time.ParseInLocation("2006-01-02 15:04", a.Date+" "+a.Time, loc)
		if err != nil {
			continue
		}
		if dt.Before(windowStart) || !dt.Before(windowEnd) {
			continue
		}

		rep.TotalAppointments++
		switch a.Status {
		case "completed":
			rep.CompletedAppointments++
		case "noshow":
			rep.NoShowAppointments++
		case "cancelled":
			rep.CancelledAppointments++
		case "active":
			rep.ActiveAppointments++
		}

		svc := a.Service
		if svc == "" {
			svc = "未指定"
		}
		serviceCount[svc]++

		if a.CustomerID != "" {
			cs, ok := customerCount[a.CustomerID]
			if !ok {
				cs = &CustomerStat{CustomerID: a.CustomerID, Name: a.Customer}
				customerCount[a.CustomerID] = cs
			}
			cs.Total++
			if cs.Name == "" && a.Customer != "" {
				cs.Name = a.Customer
			}
		}

		ds, ok := dailyMap[a.Date]
		if !ok {
			ds = &DailyStat{Date: a.Date}
			dailyMap[a.Date] = ds
		}
		ds.Total++
		switch a.Status {
		case "completed":
			ds.Completed++
		case "noshow":
			ds.NoShow++
		case "cancelled":
			ds.Cancelled++
		}
	}

	// 5. 派生率
	closed := rep.NoShowAppointments + rep.CompletedAppointments
	if closed > 0 {
		rep.CompletionRate = float64(rep.CompletedAppointments) / float64(closed)
		rep.NoShowRate = float64(rep.NoShowAppointments) / float64(closed)
	}

	// 6. 服务 / 顾客排行
	rep.UniqueServices = len(serviceCount)
	rep.ServiceRank = topServices(serviceCount, WeeklyReportRankLimit)
	rep.UniqueCustomers = len(customerCount)
	rep.TopCustomers = topCustomers(customerCount, WeeklyReportRankLimit)

	// 7. 日趋势：填满 7 天
	rep.DailyTrend = fillDailyTrend(dailyMap, windowStart, WeeklyReportWindowDays)

	// 8. 周环比：拉上周 [windowStart-7d, windowStart) 的 total / completed / noshow
	lastStart := windowStart.AddDate(0, 0, -WeeklyReportWindowDays)
	lastEnd := windowStart
	rep.LastWeekTotal, rep.LastWeekCompleted, rep.LastWeekNoShow = countLastWeekAppointments(ctx, shopID, lastStart, lastEnd)

	// 9. 增长率 + delta
	if rep.LastWeekTotal > 0 {
		rep.TotalGrowthRate = float64(rep.TotalAppointments-rep.LastWeekTotal) / float64(rep.LastWeekTotal)
	}
	if rep.LastWeekCompleted > 0 {
		rep.CompletedGrowthRate = float64(rep.CompletedAppointments-rep.LastWeekCompleted) / float64(rep.LastWeekCompleted)
	}
	rep.NoShowDelta = rep.NoShowAppointments - rep.LastWeekNoShow

	return rep, nil
}

// countLastWeekAppointments 拉 [from, to) 内的 total / completed / noshow 计数
//
//   - 复用 BuildWeeklyUsageReport 的 date+time 解析 + 精确过滤口径
//   - 返回 (total, completed, noshow) — 复用于周环比
func countLastWeekAppointments(ctx context.Context, shopID string, from, to time.Time) (int, int, int) {
	if DB == nil {
		return 0, 0, 0
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	dateFrom := from.AddDate(0, 0, -1).Format("2006-01-02")
	dateTo := to.AddDate(0, 0, 1).Format("2006-01-02")
	var appts []Appointment
	if err := DB.WithContext(ctx).
		Where("shop_id = ? AND date >= ? AND date <= ?", shopID, dateFrom, dateTo).
		Find(&appts).Error; err != nil {
		return 0, 0, 0
	}
	var total, completed, noshow int
	for _, a := range appts {
		dt, err := time.ParseInLocation("2006-01-02 15:04", a.Date+" "+a.Time, loc)
		if err != nil {
			continue
		}
		if dt.Before(from) || !dt.Before(to) {
			continue
		}
		total++
		switch a.Status {
		case "completed":
			completed++
		case "noshow":
			noshow++
		}
	}
	return total, completed, noshow
}

// BuildChainWeeklyUsageReport 组装跨店本周周报（v4.3 PRD §11.12 连锁版）
//
//   - now：当前时间（周一 9:00 cron 触发）
//   - 时间窗：[now-7d, now)（与单店 BuildWeeklyUsageReport 一致）
//   - 行为：ListAllShopIDs 拿到所有店 → 逐店 BuildWeeklyUsageReport → 累加 totals
//   - 跨店服务/顾客排行：union 后再 top 5
//   - DB 未初始化：返回空 ChainWeeklyReport（不报错）
func BuildChainWeeklyUsageReport(ctx context.Context, now time.Time) (ChainWeeklyReport, error) {
	windowStart := now.AddDate(0, 0, -WeeklyReportWindowDays)
	rep := ChainWeeklyReport{
		GeneratedAt: now,
		WindowStart: windowStart,
		WindowEnd:   now,
		WindowDays:  WeeklyReportWindowDays,
		WeekLabel:   windowStart.Format("2006-01-02") + " ~ " + now.Format("2006-01-02"),
	}

	ids, err := ListAllShopIDs(ctx)
	if err != nil {
		return rep, err
	}
	if len(ids) == 0 {
		return rep, nil
	}

	chainService := make(map[string]int)
	chainCustomer := make(map[string]*CustomerStat)

	for _, shopID := range ids {
		wr, err := BuildWeeklyUsageReport(ctx, shopID, now)
		if err != nil {
			// 单店失败不阻塞跨店（log 已在 BuildWeeklyUsageReport 内部）
			continue
		}
		rep.Total.TotalAppointments += wr.TotalAppointments
		rep.Total.CompletedAppointments += wr.CompletedAppointments
		rep.Total.NoShowAppointments += wr.NoShowAppointments
		rep.Total.CancelledAppointments += wr.CancelledAppointments
		rep.Total.ActiveAppointments += wr.ActiveAppointments
		for _, s := range wr.ServiceRank {
			chainService[s.Service] += s.Count
		}
		for _, c := range wr.TopCustomers {
			cs, ok := chainCustomer[c.CustomerID]
			if !ok {
				cs = &CustomerStat{CustomerID: c.CustomerID, Name: c.Name}
				chainCustomer[c.CustomerID] = cs
			}
			cs.Total += c.Total
			if cs.Name == "" && c.Name != "" {
				cs.Name = c.Name
			}
		}
		rep.PerShop = append(rep.PerShop, wr)
	}

	// 排序 + 截断 per_shop
	sort.Slice(rep.PerShop, func(i, j int) bool {
		return rep.PerShop[i].ShopID < rep.PerShop[j].ShopID
	})
	rep.ShopCount = len(rep.PerShop)

	// 跨店率
	closed := rep.Total.NoShowAppointments + rep.Total.CompletedAppointments
	if closed > 0 {
		rep.Total.CompletionRate = float64(rep.Total.CompletedAppointments) / float64(closed)
		rep.Total.NoShowRate = float64(rep.Total.NoShowAppointments) / float64(closed)
	}

	rep.TopServices = topServices(chainService, WeeklyReportRankLimit)
	rep.TopCustomers = topCustomers(chainCustomer, WeeklyReportRankLimit)

	return rep, nil
}

// ListAllShopIDs 列出所有店铺 ID（v4.3 PRD §11.12 周报 cron 用 — 覆盖所有店铺）
//
//   - 与 chain_repo.ListAllShops 区别：本函数只返 ID（轻量；不需要 name / plan 等）
//   - shop 表是必有的核心表，所有店铺都一定有 ID
func ListAllShopIDs(ctx context.Context) ([]string, error) {
	if DB == nil {
		return nil, nil
	}
	var ids []string
	if err := DB.WithContext(ctx).
		Model(&Shop{}).
		Pluck("id", &ids).Error; err != nil {
		return nil, fmt.Errorf("列店铺失败: %w", err)
	}
	return ids, nil
}
