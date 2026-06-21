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

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
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
	resp := buildChainDashboard(t.Context())
	if resp.TotalShops != 0 {
		t.Errorf("TotalShops = %d, want 0", resp.TotalShops)
	}
	if len(resp.Shops) != 0 {
		t.Errorf("Shops = %d, want 0", len(resp.Shops))
	}
	if resp.ChainTotals.Total != 0 {
		t.Errorf("ChainTotals.Total = %d, want 0", resp.ChainTotals.Total)
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

	resp := buildChainDashboard(t.Context())
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

	resp := buildChainDashboard(t.Context())
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

	resp := buildChainDashboard(t.Context())
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
	status, _ := runHandler(t, chainDashboardHandler, ctx)
	if status != statusUnauthorized {
		t.Errorf("未登录应返回 401，got %d", status)
	}
}

func TestChainDashboardHandler_HappyPath(t *testing.T) {
	setupAPITestDB(t)
	storage.MakeShop(t, "shop-1", "")
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	seedAppointment(t, "shop-1", "Tony", today, "10:00", "completed")

	ctx := newAPIContext(t, "GET", "/api/admin/chain/dashboard", nil,
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
