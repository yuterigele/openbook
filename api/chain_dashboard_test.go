package api

// chain_dashboard_test.go
//
// Tests for the cross-shop / chain dashboard (PRD §11.10 v4.0).
//
// Coverage:
//   - storage layer: ListAllShops, ShopAggregateByID
//   - api layer: buildChainDashboard, chainEventFunnel, chainDashboardHandler
//
// Run:
//   go test ./api/... -v -run "TestChainDashboard|TestChainEventFunnel|TestListAllShops|TestShopAggregateByID"

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/yuterigele/openbook/storage"
)

// seedAppointment 在指定 shop / 状态 / 时间建一条预约（直接写表）
//
//   - 必须显式设 ID：Appointment.ID 是 primary key（string），GORM 不会自动填
//   - BarberID 留空（汇总不依赖 barber_id，只看 barber_name + status）
//   - CustomerID 留空
func seedAppointment(t *testing.T, shopID, barber, date, timeStr, status string) storage.Appointment {
	t.Helper()
	a := storage.Appointment{
		ID:         uuid.NewString(),
		ShopID:     shopID,
		BarberID:   "barber-" + barber,
		BarberName: barber,
		Date:       date,
		Time:       timeStr,
		Service:    "剪发",
		Status:     status,
		Source:     "wecom",
		Customer:   "cust-" + shopID,
	}
	if err := storage.DB.Create(&a).Error; err != nil {
		t.Fatalf("seed appt: %v", err)
	}
	return a
}

// ===================== storage.ListAllShops =====================

func TestListAllShops_EmptyDB(t *testing.T) {
	setupAPITestDB(t)
	shops := storage.ListAllShops(t.Context())
	if len(shops) != 0 {
		t.Errorf("空 DB 应返回空切片，got %d", len(shops))
	}
}

func TestListAllShops_MultipleShops(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-A", "")
	storage.MakeShop(t, "shop-B", "")
	storage.MakeShop(t, "shop-C", "")

	shops := storage.ListAllShops(t.Context())
	if len(shops) != 3 {
		t.Fatalf("应有 3 家店，got %d", len(shops))
	}
	// 按 id ASC 排序
	want := []string{"shop-A", "shop-B", "shop-C"}
	for i, s := range shops {
		if s.ID != want[i] {
			t.Errorf("pos %d ID = %q, want %q", i, s.ID, want[i])
		}
	}
}

// ===================== storage.ShopAggregateByID =====================

func TestShopAggregateByID_EmptyDB(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	from := now.AddDate(0, 0, -7)
	to := now.AddDate(0, 0, 1)

	stats, err := storage.ShopAggregateByID(t.Context(), "shop-1", from, to)
	if err != nil {
		t.Fatalf("ShopAggregateByID: %v", err)
	}
	if stats.Total != 0 || stats.Completed != 0 || stats.NoShow != 0 {
		t.Errorf("空数据应全 0，got %+v", stats)
	}
}

func TestShopAggregateByID_GroupsByStatus(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")

	// 3 个 today：1 completed + 1 noshow + 1 cancelled + 1 active
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	seedAppointment(t, "shop-1", "Tony", today, "10:00", "completed")
	seedAppointment(t, "shop-1", "Tony", today, "11:00", "noshow")
	seedAppointment(t, "shop-1", "Kevin", today, "14:00", "cancelled")
	seedAppointment(t, "shop-1", "Kevin", today, "15:00", "active")

	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	to := from.Add(24 * time.Hour)
	stats, err := storage.ShopAggregateByID(t.Context(), "shop-1", from, to)
	if err != nil {
		t.Fatalf("ShopAggregateByID: %v", err)
	}
	if stats.Total != 4 {
		t.Errorf("Total = %d, want 4", stats.Total)
	}
	if stats.Completed != 1 {
		t.Errorf("Completed = %d, want 1", stats.Completed)
	}
	if stats.NoShow != 1 {
		t.Errorf("NoShow = %d, want 1", stats.NoShow)
	}
	if stats.Cancelled != 1 {
		t.Errorf("Cancelled = %d, want 1", stats.Cancelled)
	}
	if stats.Active != 1 {
		t.Errorf("Active = %d, want 1", stats.Active)
	}
	// 闭单率：1 completed / 2 closed = 0.5
	if stats.CompleteRate < 0.49 || stats.CompleteRate > 0.51 {
		t.Errorf("CompleteRate = %f, want ~0.5", stats.CompleteRate)
	}
	if stats.NoShowRate < 0.49 || stats.NoShowRate > 0.51 {
		t.Errorf("NoShowRate = %f, want ~0.5", stats.NoShowRate)
	}
}

func TestShopAggregateByID_FiltersByDateRange(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	to := from.Add(24 * time.Hour)

	// 5 天前的预约 —— 不在窗内
	old := now.AddDate(0, 0, -5).Format("2006-01-02")
	seedAppointment(t, "shop-1", "Tony", old, "10:00", "completed")

	// 今天窗内的
	today := now.Format("2006-01-02")
	seedAppointment(t, "shop-1", "Tony", today, "10:00", "completed")
	seedAppointment(t, "shop-1", "Tony", today, "11:00", "completed")

	stats, err := storage.ShopAggregateByID(t.Context(), "shop-1", from, to)
	if err != nil {
		t.Fatalf("ShopAggregateByID: %v", err)
	}
	if stats.Total != 2 {
		t.Errorf("Total = %d, want 2（5 天前的不应计入）", stats.Total)
	}
}

func TestShopAggregateByID_ShopIsolation(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-A", "")
	storage.MakeShop(t, "shop-B", "")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	seedAppointment(t, "shop-A", "Tony", today, "10:00", "completed")
	seedAppointment(t, "shop-B", "Tony", today, "11:00", "completed")
	seedAppointment(t, "shop-B", "Kevin", today, "12:00", "completed")

	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	to := from.Add(24 * time.Hour)
	statsA, _ := storage.ShopAggregateByID(t.Context(), "shop-A", from, to)
	statsB, _ := storage.ShopAggregateByID(t.Context(), "shop-B", from, to)

	if statsA.Total != 1 {
		t.Errorf("shop-A Total = %d, want 1", statsA.Total)
	}
	if statsB.Total != 2 {
		t.Errorf("shop-B Total = %d, want 2", statsB.Total)
	}
}

// ===================== api.buildChainDashboard =====================

func TestBuildChainDashboard_EmptyDB(t *testing.T) {
	setupAPITestDB(t)
	resp := buildChainDashboard(t.Context(), "month")
	if resp.TotalShops != 0 {
		t.Errorf("TotalShops = %d, want 0", resp.TotalShops)
	}
	if len(resp.Shops) != 0 {
		t.Errorf("Shops = %d, want 0", len(resp.Shops))
	}
	if resp.ChainTotals.Total != 0 {
		t.Errorf("ChainTotals.Total = %d, want 0", resp.ChainTotals.Total)
	}
	if resp.Window != "month" {
		t.Errorf("Window = %q, want %q", resp.Window, "month")
	}
}

func TestBuildChainDashboard_SingleShop(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	seedAppointment(t, "shop-1", "Tony", today, "10:00", "completed")
	seedAppointment(t, "shop-1", "Kevin", today, "11:00", "noshow")

	resp := buildChainDashboard(t.Context(), "today")
	if resp.TotalShops != 1 {
		t.Errorf("TotalShops = %d, want 1", resp.TotalShops)
	}
	if len(resp.Shops) != 1 {
		t.Fatalf("Shops len = %d, want 1", len(resp.Shops))
	}
	if resp.Shops[0].Shop.ID != "shop-1" {
		t.Errorf("Shops[0].Shop.ID = %q, want %q", resp.Shops[0].Shop.ID, "shop-1")
	}
	if resp.Shops[0].Stats.Total != 2 {
		t.Errorf("Shops[0].Stats.Total = %d, want 2", resp.Shops[0].Stats.Total)
	}
	// 链总和应等于单店
	if resp.ChainTotals.Total != 2 {
		t.Errorf("ChainTotals.Total = %d, want 2", resp.ChainTotals.Total)
	}
	if resp.ChainTotals.NoShow != 1 || resp.ChainTotals.Completed != 1 {
		t.Errorf("ChainTotals 分项错: %+v", resp.ChainTotals)
	}
	if resp.ChainTotals.Window != "today" {
		t.Errorf("ChainTotals.Window = %q, want %q", resp.ChainTotals.Window, "today")
	}
}

func TestBuildChainDashboard_MultiShop(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-A", "")
	storage.MakeShop(t, "shop-B", "")
	storage.MakeShop(t, "shop-C", "")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	// shop-A 2 单 + shop-B 5 单 + shop-C 1 单 = 8
	for i := 0; i < 2; i++ {
		seedAppointment(t, "shop-A", "Tony", today, "10:00", "completed")
	}
	for i := 0; i < 5; i++ {
		seedAppointment(t, "shop-B", "Kevin", today, "11:00", "completed")
	}
	seedAppointment(t, "shop-C", "Leo", today, "14:00", "noshow")

	resp := buildChainDashboard(t.Context(), "month")
	if resp.TotalShops != 3 {
		t.Errorf("TotalShops = %d, want 3", resp.TotalShops)
	}
	if resp.ChainTotals.Total != 8 {
		t.Errorf("ChainTotals.Total = %d, want 8（=2+5+1）", resp.ChainTotals.Total)
	}

	// TopShops 应按 total DESC 排序：B(5) > A(2) > C(1)
	if len(resp.TopShops) != 3 {
		t.Fatalf("TopShops len = %d, want 3", len(resp.TopShops))
	}
	wantOrder := []struct {
		ID    string
		Total int
	}{
		{"shop-B", 5},
		{"shop-A", 2},
		{"shop-C", 1},
	}
	for i, w := range wantOrder {
		if resp.TopShops[i].ShopID != w.ID || resp.TopShops[i].Total != w.Total {
			t.Errorf("TopShops[%d] = %+v, want %+v", i, resp.TopShops[i], w)
		}
	}
}

func TestBuildChainDashboard_TopShops_Limit5(t *testing.T) {
	setupAPITestDB(t)
	// 建 8 家店，让 top 5 触发 limit
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	for i := 0; i < 8; i++ {
		id := string(rune('A' + i))
		shopID := "shop-" + id
		storage.MakeShop(t, shopID, "")
		// 倒序：i 越大单数越少（A=8, B=7, ..., H=1）
		for j := 0; j < 8-i; j++ {
			seedAppointment(t, shopID, "Tony", today, "10:00", "completed")
		}
	}

	resp := buildChainDashboard(t.Context(), "month")
	if resp.TotalShops != 8 {
		t.Errorf("TotalShops = %d, want 8", resp.TotalShops)
	}
	if len(resp.TopShops) != 5 {
		t.Errorf("TopShops len = %d, want 5（limit 5）", len(resp.TopShops))
	}
	// top 5 应是 A(8) B(7) C(6) D(5) E(4)
	wantOrder := []string{"shop-A", "shop-B", "shop-C", "shop-D", "shop-E"}
	for i, id := range wantOrder {
		if resp.TopShops[i].ShopID != id {
			t.Errorf("TopShops[%d].ShopID = %q, want %q", i, resp.TopShops[i].ShopID, id)
		}
	}
}

// ===================== api.chainEventFunnel =====================

func TestChainEventFunnel_GroupsAcrossShops(t *testing.T) {
	setupAPITestDB(t)
	fixtureTime := time.Now().Add(-30 * time.Minute)
	// shop-A 2 个 appointment_created + shop-B 1 个 appointment_created + shop-B 1 个 handoff
	storage.DB.Create(&storage.EventLog{
		ShopID: "shop-A", EventType: storage.EventAppointmentCreated, RefID: "a1", CreatedAt: fixtureTime,
	})
	storage.DB.Create(&storage.EventLog{
		ShopID: "shop-A", EventType: storage.EventAppointmentCreated, RefID: "a2", CreatedAt: fixtureTime,
	})
	storage.DB.Create(&storage.EventLog{
		ShopID: "shop-B", EventType: storage.EventAppointmentCreated, RefID: "b1", CreatedAt: fixtureTime,
	})
	storage.DB.Create(&storage.EventLog{
		ShopID: "shop-B", EventType: storage.EventHandoffToHuman, RefID: "b-h", CreatedAt: fixtureTime,
	})

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	from := now.AddDate(0, 0, -1)
	got := chainEventFunnel(t.Context(), from, now.Add(24*time.Hour), 20)

	counts := make(map[string]int, len(got))
	for _, e := range got {
		counts[e.EventType] = e.Count
	}
	if counts[storage.EventAppointmentCreated] != 3 {
		t.Errorf("appointment_created 应 = 3（跨店合计），got %d", counts[storage.EventAppointmentCreated])
	}
	if counts[storage.EventHandoffToHuman] != 1 {
		t.Errorf("handoff_to_human 应 = 1，got %d", counts[storage.EventHandoffToHuman])
	}
}

func TestChainEventFunnel_ExcludesOldEvents(t *testing.T) {
	setupAPITestDB(t)
	fixtureTime := time.Now().Add(-30 * time.Minute)
	// 1 个窗内 + 1 个 40 天前
	storage.DB.Create(&storage.EventLog{
		ShopID: "shop-A", EventType: storage.EventAppointmentCreated, RefID: "fresh", CreatedAt: fixtureTime,
	})
	storage.DB.Create(&storage.EventLog{
		ShopID: "shop-A", EventType: storage.EventAppointmentCreated, RefID: "old", CreatedAt: time.Now().AddDate(0, 0, -40),
	})

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	from := now.AddDate(0, -1, 0) // 1 month back
	got := chainEventFunnel(t.Context(), from, now.Add(24*time.Hour), 20)
	if len(got) != 1 || got[0].Count != 1 {
		t.Errorf("old 事件不应被计入，got %+v", got)
	}
}

func TestChainEventFunnel_NormalizesIdleSlotPush(t *testing.T) {
	setupAPITestDB(t)
	fixtureTime := time.Now().Add(-30 * time.Minute)
	today := time.Now().Format("2006-01-02")
	// 3 个不同 customer 的 idle_slot_push 应归一
	storage.DB.Create(&storage.EventLog{
		ShopID: "shop-A", EventType: storage.EventIdleSlotPush + ":" + today + ":c1", RefID: "", CreatedAt: fixtureTime,
	})
	storage.DB.Create(&storage.EventLog{
		ShopID: "shop-B", EventType: storage.EventIdleSlotPush + ":" + today + ":c2", RefID: "", CreatedAt: fixtureTime,
	})
	storage.DB.Create(&storage.EventLog{
		ShopID: "shop-A", EventType: storage.EventIdleSlotPush + ":" + today + ":c3", RefID: "", CreatedAt: fixtureTime,
	})

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	from := now.AddDate(0, 0, -1)
	got := chainEventFunnel(t.Context(), from, now.Add(24*time.Hour), 20)
	if len(got) != 1 {
		t.Fatalf("应归一为 1 类事件，got %d (%+v)", len(got), got)
	}
	if got[0].EventType != storage.EventIdleSlotPush || got[0].Count != 3 {
		t.Errorf("应归一为 idle_slot_push=3，got %+v", got[0])
	}
}

// ===================== chainDashboardHandler (auth + JSON) =====================

func TestChainDashboardHandler_NoClaims_401(t *testing.T) {
	setupAPITestDB(t)
	ctx := newAPIContext(t, "GET", "/api/admin/chain/dashboard", nil)
	// v4.10.1：路由用 RequireRole(RolePlatformAdmin)，无 claims → 401
	status, body := runWithRole(t, []string{storage.RolePlatformAdmin}, chainDashboardHandler, ctx)
	if status != statusUnauthorized {
		t.Errorf("未登录应返回 401，got %d body=%s", status, body)
	}
}

func TestChainDashboardHandler_OwnerIsForbidden_403(t *testing.T) {
	// v4.10.1：单店 owner 不能看多店看板（权限泄漏修复）
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")
	ctx := newAPIContext(t, "GET", "/api/admin/chain/dashboard", nil,
		withClaims(adminClaims("shop-1")), // role=owner
	)
	status, body := runWithRole(t, []string{storage.RolePlatformAdmin}, chainDashboardHandler, ctx)
	if status != statusForbidden {
		t.Errorf("owner 应返回 403，got %d body=%s", status, body)
	}
}

func TestChainDashboardHandler_PlatformAdminOK_200(t *testing.T) {
	// v4.10.1：platform_admin 应该能进
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")
	// 注意：adminClaims 默认 role=owner；用 setClaimsForAdmin 覆盖
	ctx := newAPIContext(t, "GET", "/api/admin/chain/dashboard", nil)
	setClaimsForAdmin(ctx, 99, "shop-1", storage.RolePlatformAdmin)
	status, body := runWithRole(t, []string{storage.RolePlatformAdmin}, chainDashboardHandler, ctx)
	if status != statusOK {
		t.Errorf("platform_admin 应返回 200，got %d body=%s", status, body)
	}
}

func TestChainDashboardHandler_HappyPath(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	seedAppointment(t, "shop-1", "Tony", today, "10:00", "completed")

	// v4.10.1：走中间件模拟真实 HTTP 路径，用 platform_admin 才能进
	ctx := newAPIContext(t, "GET", "/api/admin/chain/dashboard", nil)
	setClaimsForAdmin(ctx, 99, "shop-1", storage.RolePlatformAdmin)
	status, body := runWithRole(t, []string{storage.RolePlatformAdmin}, chainDashboardHandler, ctx)
	if status != statusOK {
		t.Fatalf("happy path 应返回 200，got %d, body=%s", status, body)
	}

	var resp ChainDashboardResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("反序列化失败: %v, body=%s", err, body)
	}
	if resp.TotalShops != 1 {
		t.Errorf("TotalShops = %d, want 1", resp.TotalShops)
	}
	if resp.ChainTotals.Total != 1 {
		t.Errorf("ChainTotals.Total = %d, want 1", resp.ChainTotals.Total)
	}
	if len(resp.Shops) != 1 || resp.Shops[0].Shop.ID != "shop-1" {
		t.Errorf("Shops 异常: %+v", resp.Shops)
	}
}

func TestChainDashboardHandler_DBNotInitialized(t *testing.T) {
	storage.DB = nil
	defer func() { storage.DB = nil }()

	ctx := newAPIContext(t, "GET", "/api/admin/chain/dashboard", nil,
		withClaims(adminClaims("shop-1")),
	)
	status, _ := runHandler(t, chainDashboardHandler, ctx)
	if status != 503 {
		t.Errorf("DB 未初始化应返回 503，got %d", status)
	}
}

// ===================== v4.1: parseWindow + resolveWindowBounds =====================

func TestParseWindow(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "month"},                   // 空 → 默认
		{"   ", "month"},                // 仅空白 → 默认
		{"today", "today"},              // 小写合法
		{"TODAY", "today"},              // 大写自动 normalize
		{"  month  ", "month"},          // trim + 小写
		{"week", "week"},
		{"year", ""},                    // 非法 → ""
		{"daily", ""},                   // 非法 → ""
		{"yesterday", ""},               // 非法 → ""
		{"month;DROP TABLE", ""},        // 注入尝试 → ""
	}
	for _, c := range cases {
		got := parseWindow(c.in)
		if got != c.want {
			t.Errorf("parseWindow(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveWindowBounds_Today(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	// 周三 2026-06-17 10:30:00
	now := time.Date(2026, 6, 17, 10, 30, 0, 0, loc)
	from, to := resolveWindowBounds(now, "today")
	wantFrom := time.Date(2026, 6, 17, 0, 0, 0, 0, loc)
	wantTo := time.Date(2026, 6, 18, 0, 0, 0, 0, loc)
	if !from.Equal(wantFrom) {
		t.Errorf("today from = %v, want %v", from, wantFrom)
	}
	if !to.Equal(wantTo) {
		t.Errorf("today to = %v, want %v", to, wantTo)
	}
}

func TestResolveWindowBounds_Week_StartsOnMonday(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")

	// 周三 → 本周一 06-15 00:00 到下周一 06-22 00:00
	nowWed := time.Date(2026, 6, 17, 10, 30, 0, 0, loc)
	from, to := resolveWindowBounds(nowWed, "week")
	wantFrom := time.Date(2026, 6, 15, 0, 0, 0, 0, loc)
	wantTo := time.Date(2026, 6, 22, 0, 0, 0, 0, loc)
	if !from.Equal(wantFrom) {
		t.Errorf("周三 week from = %v, want %v（应回退到周一）", from, wantFrom)
	}
	if !to.Equal(wantTo) {
		t.Errorf("周三 week to = %v, want %v", to, wantTo)
	}

	// 周日 → 本周一 06-15（注意：周日属于"下一周的开始"之前，所以是本周一）= 上周一 +7 天
	nowSun := time.Date(2026, 6, 21, 23, 59, 59, 0, loc)
	fromSun, toSun := resolveWindowBounds(nowSun, "week")
	if !fromSun.Equal(wantFrom) {
		t.Errorf("周日 week from = %v, want %v（周日也应回退到本周一）", fromSun, wantFrom)
	}
	if !toSun.Equal(wantTo) {
		t.Errorf("周日 week to = %v, want %v", toSun, wantTo)
	}

	// 周一自身 → 本周一 00:00
	nowMon := time.Date(2026, 6, 15, 0, 0, 1, 0, loc)
	fromMon, toMon := resolveWindowBounds(nowMon, "week")
	if !fromMon.Equal(wantFrom) {
		t.Errorf("周一 week from = %v, want %v", fromMon, wantFrom)
	}
	if !toMon.Equal(wantTo) {
		t.Errorf("周一 week to = %v, want %v", toMon, wantTo)
	}
}

func TestResolveWindowBounds_Month(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 6, 17, 10, 30, 0, 0, loc)
	from, to := resolveWindowBounds(now, "month")
	wantFrom := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	wantTo := time.Date(2026, 7, 1, 0, 0, 0, 0, loc)
	if !from.Equal(wantFrom) {
		t.Errorf("month from = %v, want %v", from, wantFrom)
	}
	if !to.Equal(wantTo) {
		t.Errorf("month to = %v, want %v", to, wantTo)
	}

	// 跨年：12 月 → 次年 1 月
	nowDec := time.Date(2026, 12, 31, 23, 59, 59, 0, loc)
	fromDec, toDec := resolveWindowBounds(nowDec, "month")
	if !fromDec.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, loc)) {
		t.Errorf("12 月 from = %v, want 12-01", fromDec)
	}
	if !toDec.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, loc)) {
		t.Errorf("12 月 to = %v, want 次年 1-01", toDec)
	}
}

func TestResolveWindowBounds_FallbackOnUnknown(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 6, 17, 10, 30, 0, 0, loc)
	from, to := resolveWindowBounds(now, "year") // 未知值
	wantFrom := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	wantTo := time.Date(2026, 7, 1, 0, 0, 0, 0, loc)
	if !from.Equal(wantFrom) || !to.Equal(wantTo) {
		t.Errorf("未知 window 应 fallback 到 month, got from=%v to=%v", from, to)
	}
}

// ===================== v4.1: buildChainDashboard 按窗口隔离 =====================

func TestBuildChainDashboard_WindowIsolation_TodayVsMonth(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	farPast := now.AddDate(0, 0, -10).Format("2006-01-02")

	// 今天 2 单（completed/noshow）
	seedAppointment(t, "shop-1", "Tony", today, "10:00", "completed")
	seedAppointment(t, "shop-1", "Kevin", today, "11:00", "noshow")
	// 昨天 1 单（应在 month 内、不在 today 内）
	seedAppointment(t, "shop-1", "Leo", yesterday, "10:00", "completed")
	// 10 天前 1 单（仅 month 内，且因为可能跨月，所以用月内偏早日期保险）
	// 注意：如果 today 是月初（1~10 号），farPast 可能已经不在这个月内；用月初测试更稳
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	if now.Day() > 10 {
		// 安全：仅当 today 不是月初时才加这条 farPast
		seedAppointment(t, "shop-1", "Mike", farPast, "10:00", "completed")
	} else {
		// 月初测试：farPast 改成 monthStart + 5 天 = 仍在本月内
		earlyInMonth := monthStart.AddDate(0, 0, 5).Format("2006-01-02")
		seedAppointment(t, "shop-1", "Mike", earlyInMonth, "10:00", "completed")
	}

	respToday := buildChainDashboard(t.Context(), "today")
	if respToday.ChainTotals.Total != 2 {
		t.Errorf("today 总数 = %d, want 2（昨天的+月初的不算）", respToday.ChainTotals.Total)
	}

	respMonth := buildChainDashboard(t.Context(), "month")
	// month 应至少 3 单（today 2 + yesterday 1），可能 4（如果 farPast/earlyInMonth 在本月内）
	if respMonth.ChainTotals.Total < 3 {
		t.Errorf("month 总数 = %d, want >=3", respMonth.ChainTotals.Total)
	}
	if respMonth.ChainTotals.Total <= respToday.ChainTotals.Total {
		t.Errorf("month 应 >= today，got month=%d today=%d",
			respMonth.ChainTotals.Total, respToday.ChainTotals.Total)
	}
}

func TestBuildChainDashboard_DefaultsToMonth(t *testing.T) {
	// 不传 window → 应等价于 month
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	seedAppointment(t, "shop-1", "Tony", today, "10:00", "completed")

	// 直接传 month 当对照
	respMonth := buildChainDashboard(t.Context(), "month")
	if respMonth.Window != "month" {
		t.Errorf("显式 month 的响应 Window = %q", respMonth.Window)
	}
	if respMonth.ChainTotals.Total != 1 {
		t.Errorf("month Total = %d, want 1", respMonth.ChainTotals.Total)
	}
}

// ===================== v4.1: chainDashboardHandler ?window= =====================

func TestChainDashboardHandler_WindowQuery_Today(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	seedAppointment(t, "shop-1", "Tony", today, "10:00", "completed")

	ctx := newAPIContext(t, "GET", "/api/admin/chain/dashboard", nil,
		withQuery("window", "today"),
		withClaims(adminClaims("shop-1")),
	)
	status, body := runHandler(t, chainDashboardHandler, ctx)
	if status != statusOK {
		t.Fatalf("happy path 应返回 200，got %d, body=%s", status, body)
	}
	var resp ChainDashboardResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("反序列化失败: %v, body=%s", err, body)
	}
	if resp.Window != "today" {
		t.Errorf("Window = %q, want today", resp.Window)
	}
	if resp.ChainTotals.Window != "today" {
		t.Errorf("ChainTotals.Window = %q, want today", resp.ChainTotals.Window)
	}
}

func TestChainDashboardHandler_DefaultWindowWhenMissing(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")

	// 不传 window → 默认 month
	ctx := newAPIContext(t, "GET", "/api/admin/chain/dashboard", nil,
		withClaims(adminClaims("shop-1")),
	)
	status, body := runHandler(t, chainDashboardHandler, ctx)
	if status != statusOK {
		t.Fatalf("默认应返回 200，got %d", status)
	}
	var resp ChainDashboardResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("反序列化失败: %v, body=%s", err, body)
	}
	if resp.Window != "month" {
		t.Errorf("默认 Window = %q, want month", resp.Window)
	}
}

func TestChainDashboardHandler_InvalidWindow_400(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")

	ctx := newAPIContext(t, "GET", "/api/admin/chain/dashboard", nil,
		withQuery("window", "year"), // 非法
		withClaims(adminClaims("shop-1")),
	)
	status, body := runHandler(t, chainDashboardHandler, ctx)
	if status != statusBadRequest {
		t.Errorf("非法 window 应返回 400，got %d, body=%s", status, body)
	}
}
