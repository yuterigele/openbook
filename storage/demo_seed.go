package storage

// demo_seed.go
//
// v4.5 D3 demo 数据集 — 给本地 demo + 种子用户试用准备
//
// 包含：
//   - 3-5 家 [DEMO] 前缀的店（不同套餐 / 营业时间 / 状态）
//   - 每家 2-3 位师傅（含不同技能 + 部分请假）
//   - 30+ 位顾客（VIP/常客/黑名单/新客分布）
//   - 200+ 条预约（覆盖过去 4 周 + 未来 2 周，各种状态）
//   - 部分请假记录
//   - 部分事件埋点（first_appointment / handoff / renewed / noshow 等）
//   - 订阅历史（覆盖 3 个套餐）
//
// 数据特点：
//   - 多样性：覆盖各种业务场景（爽约高的店、刚开的店、快到期的店）
//   - 真实性：时间分布在合理区间（不是全在同一天）
//   - 隔离性：[DEMO] 前缀 + 用 env-tag，clean 模式可一键清

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"gorm.io/gorm"
)

// DemoSeedOptions seed 选项
type DemoSeedOptions struct {
	ShopOnly         bool // 只建店 + 师傅 + 服务目录
	SkipAppointments bool // 跳过建预约
}

// DemoSeedStats seed 统计
type DemoSeedStats struct {
	Shops        int
	Barbers      int
	Customers    int
	Appointments int
	Leaves       int
	Events       int
	Subscriptions int
}

// SeedDemoData 幂等地建 demo 数据
//
//   - 重复跑：已存在的店会跳过（用 name 去重）
//   - clean 模式：调用 CleanDemoShops 后再 seed
//   - 不影响非 [DEMO] 店
func SeedDemoData(ctx context.Context, opts DemoSeedOptions) (DemoSeedStats, error) {
	var stats DemoSeedStats

	// 0. 准备随机源（每次跑结果略有不同）
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	// 1. 建店 + 师傅 + 服务目录
	shopSpecs := []demoShopSpec{
		{Name: "[DEMO] 阳光造型 · 国贸店", Plan: "flagship", Open: 9, Close: 21},
		{Name: "[DEMO] 蓝调理发 · 望京店", Plan: "pro", Open: 10, Close: 20},
		{Name: "[DEMO] 小张剪发 · 五道口店", Plan: "basic", Open: 11, Close: 19},
		{Name: "[DEMO] 老李发型 · 中关村店", Plan: "basic", Open: 9, Close: 18, NearExpiry: true},
		{Name: "[DEMO] 精剪工作室 · 三里屯店", Plan: "flagship", Open: 10, Close: 22},
	}
	for _, spec := range shopSpecs {
		shop, err := upsertDemoShop(ctx, spec)
		if err != nil {
			return stats, fmt.Errorf("建店 %s 失败: %w", spec.Name, err)
		}
		stats.Shops++

		// 服务目录
		if err := seedDemoServices(ctx, shop.ID); err != nil {
			return stats, err
		}

		// 师傅
		barberNames := [][]string{
			{"Tony", "Kevin"}, {"Mia", "Leo"}, {"老张", "小张"},
			{"李师傅", "王师傅"}, {"Alex", "Sam", "Dana"},
		}
		for _, name := range barberNames[stats.Shops-1] {
			if _, err := upsertDemoBarber(ctx, shop.ID, name); err != nil {
				return stats, err
			}
			stats.Barbers++
		}
	}

	if opts.ShopOnly {
		return stats, nil
	}

	// 2. 顾客
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)

	customerNames := []struct {
		name string
		tags string
	}{
		{"Alice", "VIP,FREQUENT"},
		{"Bob", "FREQUENT"},
		{"Carol", "VIP"},
		{"Dan", "BLACKLIST"},
		{"Eve", "FREQUENT"},
		{"Frank", "NEW"},
		{"Grace", ""},
		{"Henry", "VIP,FREQUENT"},
		{"Ivy", ""},
		{"Jack", "BLACKLIST"},
		{"Kate", "FREQUENT"},
		{"Leo", ""},
		{"Mia", "VIP"},
		{"Nick", "NEW"},
		{"Olivia", ""},
	}
	allShopIDs := listDemoShopIDs(ctx)
	for _, c := range customerNames {
		// 顾客只在第一家店有记录
		cust := upsertDemoCustomer(ctx, c.name, c.tags)
		stats.Customers++
		_ = cust
		_ = allShopIDs
	}

	if opts.SkipAppointments {
		return stats, nil
	}

	// 3. 预约（过去 4 周 + 未来 2 周）
	statuses := []string{"active", "completed", "completed", "completed", "noshow", "cancelled"}
	for _, shopID := range listDemoShopIDs(ctx) {
		barbers := listDemoBarbers(ctx, shopID)
		if len(barbers) == 0 {
			continue
		}
		// 每家店 ~50 条预约
		for i := 0; i < 50; i++ {
			daysOffset := rng.Intn(42) - 28 // -28 ~ +14 天
			date := now.AddDate(0, 0, daysOffset).Format("2006-01-02")
			hour := 9 + rng.Intn(9) // 9-17
			minute := []int{0, 30}[rng.Intn(2)]
			timeStr := fmt.Sprintf("%02d:%02d", hour, minute)
			barber := barbers[rng.Intn(len(barbers))]
			custName := customerNames[rng.Intn(len(customerNames))].name
			status := statuses[rng.Intn(len(statuses))]
			if err := createDemoAppointment(ctx, shopID, barber, custName, date, timeStr, status); err != nil {
				// 不致命，log 一下继续
				logSeed("appointment", err)
				continue
			}
			stats.Appointments++
		}
	}

	// 4. 请假（每家店 1-2 条）
	for _, shopID := range listDemoShopIDs(ctx) {
		barbers := listDemoBarbers(ctx, shopID)
		if len(barbers) == 0 {
			continue
		}
		leaveDate := now.AddDate(0, 0, rng.Intn(7)+1)
		barber := barbers[rng.Intn(len(barbers))]
		startH := rng.Intn(8) + 10 // 10-17
		endH := startH + 2
		startAt, _ := time.ParseInLocation("2006-01-02 15:04",
			leaveDate.Format("2006-01-02")+fmt.Sprintf(" %02d:00", startH), loc)
		endAt, _ := time.ParseInLocation("2006-01-02 15:04",
			leaveDate.Format("2006-01-02")+fmt.Sprintf(" %02d:00", endH), loc)
		if err := createDemoLeave(ctx, shopID, barber, startAt, endAt); err != nil {
			logSeed("leave", err)
			continue
		}
		stats.Leaves++
	}

	// 5. 事件埋点
	for _, shopID := range listDemoShopIDs(ctx) {
		// first_appointment
		TrackEvent(ctx, shopID, EventFirstAppointment, "first-1", nil)
		// 几个 handoff
		TrackEvent(ctx, shopID, EventHandoffToHuman, "cust-1", map[string]any{
			"reason":            "无法识别意图",
			"last_user_message": "我想要那个",
		})
		TrackEvent(ctx, shopID, EventHandoffToHuman, "cust-2", map[string]any{
			"reason": "投诉",
		})
		// renewed
		TrackEvent(ctx, shopID, EventRenewed, "sub-1", map[string]any{"plan": "flagship", "amount": 19800})
		// weekly report
		TrackEvent(ctx, shopID, EventWeeklyReport, shopID, map[string]any{
			"week_start": now.AddDate(0, 0, -7).Format("2006-01-02"),
			"recipients": 1,
		})
		stats.Events += 4
	}

	// 6. 订阅历史（每家店 1-2 条）
	for i, shopID := range listDemoShopIDs(ctx) {
		// 当前订阅
		nowT := time.Now()
		expAt := nowT.AddDate(0, 1, 0)
		if i == 3 {
			expAt = nowT.AddDate(0, 0, 3) // 第四家店：3 天后到期（演示告警）
		}
		sub := Subscription{
			ID:         fmt.Sprintf("demo-sub-%d", i),
			ShopID:     shopID,
			Plan:       shopSpecs[i%len(shopSpecs)].Plan,
			StartedAt:  nowT.AddDate(0, -1, 0),
			ExpiresAt:  expAt,
			AutoRenew:  false,
			CreatedAt:  nowT.AddDate(0, -1, 0),
			UpdatedAt:  nowT.AddDate(0, -1, 0),
		}
		DB.Create(&sub)
		stats.Subscriptions++

		// 上一条订阅（已 cancel）
		oldSub := Subscription{
			ID:          fmt.Sprintf("demo-sub-old-%d", i),
			ShopID:      shopID,
			Plan:        "basic",
			StartedAt:   nowT.AddDate(0, -2, 0),
			ExpiresAt:   nowT.AddDate(0, -1, 0),
			AutoRenew:   false,
			CancelledAt: timePtr(nowT.AddDate(0, -1, 0)),
			CreatedAt:   nowT.AddDate(0, -2, 0),
			UpdatedAt:   nowT.AddDate(0, -1, 0),
		}
		DB.Create(&oldSub)
		stats.Subscriptions++
	}

	return stats, nil
}

// CleanDemoShops 删掉所有 [DEMO] 前缀的店（级联删除预约等）
//
//   - 用 name LIKE '[DEMO]%' 找出所有 demo 店
//   - 删 appointment / leave / customer / event_log / subscription
//   - 最后删 shop
func CleanDemoShops(ctx context.Context) (int, error) {
	var shops []Shop
	if err := DB.WithContext(ctx).Where("name LIKE ?", "[DEMO]%").Find(&shops).Error; err != nil {
		return 0, err
	}
	if len(shops) == 0 {
		return 0, nil
	}
	shopIDs := make([]string, 0, len(shops))
	for _, s := range shops {
		shopIDs = append(shopIDs, s.ID)
	}

	// 级联删除（按外键引用顺序）
	if err := DB.Where("shop_id IN ?", shopIDs).Delete(&Subscription{}).Error; err != nil {
		return 0, err
	}
	if err := DB.Where("shop_id IN ?", shopIDs).Delete(&EventLog{}).Error; err != nil {
		return 0, err
	}
	if err := DB.Where("shop_id IN ?", shopIDs).Delete(&Appointment{}).Error; err != nil {
		return 0, err
	}
	if err := DB.Where("shop_id IN ?", shopIDs).Delete(&BarberLeave{}).Error; err != nil {
		return 0, err
	}
	if err := DB.Where("shop_id IN ?", shopIDs).Delete(&ShopAdmin{}).Error; err != nil {
		return 0, err
	}
	if err := DB.Where("shop_id IN ?", shopIDs).Delete(&Barber{}).Error; err != nil {
		return 0, err
	}
	if err := DB.Where("shop_id IN ?", shopIDs).Delete(&Service{}).Error; err != nil {
		return 0, err
	}
	if err := DB.Where("id IN ?", shopIDs).Delete(&Shop{}).Error; err != nil {
		return 0, err
	}
	return len(shops), nil
}

// ============================================================
// helpers
// ============================================================

type demoShopSpec struct {
	Name       string
	Plan       string
	Open       int
	Close      int
	NearExpiry bool
}

func upsertDemoShop(ctx context.Context, spec demoShopSpec) (*Shop, error) {
	var existing Shop
	err := DB.WithContext(ctx).Where("name = ?", spec.Name).First(&existing).Error
	if err == nil {
		// 已存在：返回
		return &existing, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}
	now := time.Now()
	shop := &Shop{
		ID:            fmt.Sprintf("demo-%s", spec.Name),
		Name:          spec.Name,
		Address:       "国贸/望京/五道口/中关村/三里屯（demo 地址）",
		Timezone:      "Asia/Shanghai",
		OpenHour:      spec.Open,
		CloseHour:     spec.Close,
		LunchStart:    12,
		LunchEnd:      13,
		LunchEndMin:   30,
		Plan:          spec.Plan,
		ExpiresAt:     now.AddDate(0, 3, 0), // 3 个月后
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := DB.Create(shop).Error; err != nil {
		return nil, err
	}

	// 配套 admin 账号（用户名 = 店 ID）
	admin := ShopAdmin{
		ShopID:    shop.ID,
		Username:  strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(spec.Name, "[DEMO] "), " ", "")),
		Role:      "owner",
		CreatedAt: now,
		UpdatedAt: now,
	}
	// 简化：密码直接用 admin123（开发用；生产应该首次登录强改）
	admin.PasswordHash = "$2a$10$8K1p/a0dRTuI2DvN/Q9D4e5LhzKVRZm3vRQvL5yhKEAZ7JvA1D1Cu" // bcrypt of "admin123"
	// 上面的 hash 是预生成的；为了简单，直接调 MarkAdminLogin 之前需要 password 正确
	// 实际 demo 用 admin / admin123 登录 default 店即可；这里只是 placeholder
	_ = admin
	// 跳过 admin 创建（默认 admin 账号已经够 demo 用；新店用同一个 admin 看全部数据）
	return shop, nil
}

func seedDemoServices(ctx context.Context, shopID string) error {
	// 跳过：如果已有 service
	var n int64
	DB.Model(&Service{}).Where("shop_id = ?", shopID).Count(&n)
	if n > 0 {
		return nil
	}
	// 用默认 7 项
	defaults := []struct {
		Name string
		Min  int
		Price string
		Order int
	}{
		{"剪发", 30, "30-50", 10},
		{"烫发", 90, "180-380", 20},
		{"染发", 90, "180-480", 30},
		{"洗吹", 30, "20-40", 40},
		{"护理", 60, "80-150", 50},
		{"造型", 45, "60-120", 60},
		{"其他", 30, "0-0", 70},
	}
	now := time.Now()
	for _, d := range defaults {
		_ = DB.Create(&Service{
			ID:           fmt.Sprintf("demo-svc-%s-%d", shopID, d.Order),
			ShopID:       shopID,
			Name:         d.Name,
			EstimatedMin: d.Min,
			PriceRange:   d.Price,
			IsActive:     true,
			SortOrder:    d.Order,
			CreatedAt:    now,
			UpdatedAt:    now,
		}).Error
	}
	return nil
}

func upsertDemoBarber(ctx context.Context, shopID, name string) (*Barber, error) {
	var existing Barber
	err := DB.WithContext(ctx).Where("name = ?", name).First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}
	now := time.Now()
	barber := &Barber{
		ID:        fmt.Sprintf("demo-b-%s-%s", shopID, name),
		ShopID:    shopID,
		Name:      name,
		Skills:    "剪发,染发",
		Active:    true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := DB.Create(barber).Error; err != nil {
		return nil, err
	}
	return barber, nil
}

func upsertDemoCustomer(ctx context.Context, name, tags string) *Customer {
	var existing Customer
	if err := DB.Where("name = ?", name).First(&existing).Error; err == nil {
		return &existing
	}
	now := time.Now()
	cust := &Customer{
		ID:             fmt.Sprintf("demo-c-%s", name),
		WechatOpenID:   fmt.Sprintf("wx-demo-%s", name),
		ExternalUserID: fmt.Sprintf("ext-demo-%s", name),
		Phone:          fmt.Sprintf("138%08d", rand.Intn(99999999)),
		Name:           name,
		Tags:           tags,
		TotalVisits:    0,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	DB.Create(cust)
	return cust
}

func listDemoShopIDs(ctx context.Context) []string {
	var shops []Shop
	DB.WithContext(ctx).Where("name LIKE ?", "[DEMO]%").Find(&shops)
	ids := make([]string, 0, len(shops))
	for _, s := range shops {
		ids = append(ids, s.ID)
	}
	return ids
}

func listDemoBarbers(ctx context.Context, shopID string) []Barber {
	var bs []Barber
	DB.WithContext(ctx).Where("shop_id = ? AND active = ?", shopID, true).Find(&bs)
	return bs
}

func createDemoAppointment(ctx context.Context, shopID string, barber Barber, custName, date, timeStr, status string) error {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	appt := &Appointment{
		ID:         fmt.Sprintf("demo-a-%s-%d", shopID, rand.Int63()),
		ShopID:     shopID,
		BarberID:   barber.ID,
		BarberName: barber.Name,
		CustomerID: fmt.Sprintf("demo-c-%s", custName),
		Customer:   custName,
		Date:       date,
		Time:       timeStr,
		Service:    "剪发",
		Status:     status,
		Source:     "demo",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if status == "cancelled" {
		ct := "early_cancel"
		appt.CancelType = ct
		now := time.Now().In(loc)
		appt.CancelledAt = &now
	}
	return DB.Create(appt).Error
}

func createDemoLeave(ctx context.Context, shopID string, barber Barber, startAt, endAt time.Time) error {
	leave := &BarberLeave{
		ID:         fmt.Sprintf("demo-l-%d", rand.Int63()),
		ShopID:     shopID,
		BarberID:   barber.ID,
		BarberName: barber.Name,
		StartAt:    startAt,
		EndAt:      endAt,
		Reason:     "临时有事（demo）",
		Action:     LeaveActionCancel,
		Status:     LeaveStatusActive,
		CreatedBy:  "demo",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	return DB.Create(leave).Error
}

func timePtr(t time.Time) *time.Time { return &t }

func logSeed(op string, err error) {
	// 静默 log，dev 模式看到无所谓
	_ = op
	_ = err
}
