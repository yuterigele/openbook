package api

// dashboard_test.go
//
// Tests for the dashboard event funnel (PRD §11.2 P2 dashboard 数据补全 + v3.8).
//
// Coverage:
//   - eventFunnel: pure helper — grouping / sorting / limit / time-range filter
//   - buildDashboard: integration — funnel wired into response, all 3 windows
//
// Pattern: setupAPITestDB → plant events via storage.TrackEvent → call
// eventFunnel / buildDashboard directly (no HTTP) → assert on slice/struct.
//
// Run:
//   go test ./api/... -v -run "TestEventFunnel|TestBuildDashboard"

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// ===================== eventFunnel (helper) =====================

func TestEventFunnel_EmptyDB(t *testing.T) {
	setupAPITestDB(t)
	now := time.Now()
	got := eventFunnel(t.Context(), "shop-1", now.Add(-24*time.Hour), now, 10)
	if len(got) != 0 {
		t.Errorf("空 DB 应返回空，got %v", got)
	}
}

func TestEventFunnel_GroupsByType(t *testing.T) {
	setupAPITestDB(t)
	now := time.Now()
	since := now.Add(-1 * time.Hour)

	// 写 3 个 appointment_created + 2 个 appointment_cancelled + 1 个 customer_blacklisted
	storage.TrackEvent(t.Context(), "shop-1", storage.EventAppointmentCreated, "a1", nil)
	storage.TrackEvent(t.Context(), "shop-1", storage.EventAppointmentCreated, "a2", nil)
	storage.TrackEvent(t.Context(), "shop-1", storage.EventAppointmentCreated, "a3", nil)
	storage.TrackEvent(t.Context(), "shop-1", storage.EventAppointmentCancelled, "a1", nil)
	storage.TrackEvent(t.Context(), "shop-1", storage.EventAppointmentCancelled, "a2", nil)
	storage.TrackEvent(t.Context(), "shop-1", storage.EventBlacklisted, "cust-1", nil)

	got := eventFunnel(t.Context(), "shop-1", since, now.Add(time.Hour), 20)
	counts := make(map[string]int)
	for _, e := range got {
		counts[e.EventType] = e.Count
	}
	want := map[string]int{
		storage.EventAppointmentCreated:   3,
		storage.EventAppointmentCancelled: 2,
		storage.EventBlacklisted:          1,
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("%s count = %d, want %d (full: %v)", k, counts[k], v, counts)
		}
	}
}

func TestEventFunnel_SortByCountDesc(t *testing.T) {
	setupAPITestDB(t)
	now := time.Now()
	since := now.Add(-1 * time.Hour)

	// 1 个 + 5 个 + 3 个
	storage.TrackEvent(t.Context(), "shop-1", storage.EventAppointmentCreated, uuid.NewString(), nil)
	for i := 0; i < 5; i++ {
		storage.TrackEvent(t.Context(), "shop-1", storage.EventAppointmentCancelled, uuid.NewString(), nil)
	}
	for i := 0; i < 3; i++ {
		storage.TrackEvent(t.Context(), "shop-1", storage.EventBlacklisted, uuid.NewString(), nil)
	}

	got := eventFunnel(t.Context(), "shop-1", since, now.Add(time.Hour), 20)
	if len(got) != 3 {
		t.Fatalf("expected 3 types, got %d (%v)", len(got), got)
	}
	// 顺序：5 > 3 > 1
	want := []struct {
		EventType string
		Count     int
	}{
		{storage.EventAppointmentCancelled, 5},
		{storage.EventBlacklisted, 3},
		{storage.EventAppointmentCreated, 1},
	}
	for i, w := range want {
		if got[i].EventType != w.EventType || got[i].Count != w.Count {
			t.Errorf("pos %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestEventFunnel_LimitApplied(t *testing.T) {
	setupAPITestDB(t)
	now := time.Now()
	since := now.Add(-1 * time.Hour)

	// 写 5 种不同事件
	events := []string{
		storage.EventAppointmentCreated,
		storage.EventAppointmentCancelled,
		storage.EventAppointmentCompleted,
		storage.EventAppointmentNoShow,
		storage.EventBlacklisted,
	}
	for _, e := range events {
		storage.TrackEvent(t.Context(), "shop-1", e, uuid.NewString(), nil)
	}
	got := eventFunnel(t.Context(), "shop-1", since, now.Add(time.Hour), 3)
	if len(got) != 3 {
		t.Errorf("limit=3 应截断到 3 条，got %d", len(got))
	}
}

func TestEventFunnel_NormalizesIdleSlotPushPrefix(t *testing.T) {
	setupAPITestDB(t)
	now := time.Now()
	since := now.Add(-1 * time.Hour)

	// idle_slot_push 实际存为 idle_slot_push:DATE:CUSTID —— 应归一为 idle_slot_push
	today := now.Format("2006-01-02")
	storage.TrackEvent(t.Context(), "shop-1", storage.EventIdleSlotPush+":"+today+":cust-1", "", nil)
	storage.TrackEvent(t.Context(), "shop-1", storage.EventIdleSlotPush+":"+today+":cust-2", "", nil)
	storage.TrackEvent(t.Context(), "shop-1", storage.EventIdleSlotPush+":"+today+":cust-3", "", nil)

	got := eventFunnel(t.Context(), "shop-1", since, now.Add(time.Hour), 20)
	if len(got) != 1 {
		t.Fatalf("应归一成 1 类事件，got %d (%v)", len(got), got)
	}
	if got[0].EventType != storage.EventIdleSlotPush {
		t.Errorf("event_type = %q, want %q", got[0].EventType, storage.EventIdleSlotPush)
	}
	if got[0].Count != 3 {
		t.Errorf("count = %d, want 3", got[0].Count)
	}
}

func TestEventFunnel_FiltersByShopID(t *testing.T) {
	setupAPITestDB(t)
	now := time.Now()
	since := now.Add(-1 * time.Hour)

	storage.TrackEvent(t.Context(), "shop-A", storage.EventAppointmentCreated, "a1", nil)
	storage.TrackEvent(t.Context(), "shop-B", storage.EventAppointmentCreated, "b1", nil)
	storage.TrackEvent(t.Context(), "shop-B", storage.EventAppointmentCreated, "b2", nil)

	gotA := eventFunnel(t.Context(), "shop-A", since, now.Add(time.Hour), 20)
	if len(gotA) != 1 || gotA[0].Count != 1 {
		t.Errorf("shop-A 应只有 1 条，got %v", gotA)
	}
	gotB := eventFunnel(t.Context(), "shop-B", since, now.Add(time.Hour), 20)
	if len(gotB) != 1 || gotB[0].Count != 2 {
		t.Errorf("shop-B 应只有 2 条，got %v", gotB)
	}
}

func TestEventFunnel_FiltersByTimeRange(t *testing.T) {
	setupAPITestDB(t)
	now := time.Now()

	// 1 个 2h 前 + 1 个现在
	past := now.Add(-2 * time.Hour)
	storage.DB.Create(&storage.EventLog{
		ShopID:    "shop-1",
		EventType: storage.EventAppointmentCreated,
		RefID:     "past",
		CreatedAt: past,
	})
	storage.TrackEvent(t.Context(), "shop-1", storage.EventAppointmentCreated, "now", nil)

	// 查最近 1h —— 应只剩 "now"
	got := eventFunnel(t.Context(), "shop-1", now.Add(-1*time.Hour), now.Add(time.Hour), 20)
	if len(got) != 1 || got[0].Count != 1 {
		t.Errorf("查最近 1h 应只剩 1 条，got %v", got)
	}
}

func TestEventFunnel_DBNotInitialized(t *testing.T) {
	storage.DB = nil
	defer func() { storage.DB = nil }() // 测试结束后不污染

	got := eventFunnel(t.Context(), "shop-1", time.Now(), time.Now(), 10)
	if got != nil {
		t.Errorf("DB 未初始化应返回 nil，got %v", got)
	}
}

// ===================== buildDashboard (integration) =====================

func TestBuildDashboard_IncludesEventFunnel(t *testing.T) {
	setupAPITestDB(t)
	now := time.Now()
	// 用 "30 分钟前" 的固定时间，避免和 buildDashboard 内部的 now 抢时钟
	fixtureTime := now.Add(-30 * time.Minute)

	// 写 2 个 today 的事件（用显式 created_at）+ 1 个 old 事件（超 month 范围）
	for _, refID := range []string{"t1", "t2"} {
		storage.DB.Create(&storage.EventLog{
			ShopID:    "shop-1",
			EventType: storage.EventAppointmentCreated,
			RefID:     refID,
			CreatedAt: fixtureTime,
		})
	}
	storage.DB.Create(&storage.EventLog{
		ShopID:    "shop-1",
		EventType: storage.EventBlacklisted,
		RefID:     "old",
		CreatedAt: now.AddDate(0, 0, -40), // 40 天前 —— 超 month 范围
	})

	resp := buildDashboard(t.Context(), "shop-1")
	if len(resp.EventFunnelToday) == 0 {
		t.Error("EventFunnelToday 应非空")
	}
	if len(resp.EventFunnelWeek) == 0 {
		t.Error("EventFunnelWeek 应非空")
	}
	if len(resp.EventFunnelMonth) == 0 {
		t.Error("EventFunnelMonth 应非空")
	}

	// today funnel: 2 个 appointment_created
	todayCount := 0
	for _, e := range resp.EventFunnelToday {
		if e.EventType == storage.EventAppointmentCreated {
			todayCount = e.Count
		}
	}
	if todayCount != 2 {
		t.Errorf("EventFunnelToday.appointment_created = %d, want 2 (full: %v)", todayCount, resp.EventFunnelToday)
	}

	// 40 天前的事件不应在 month 内
	for _, e := range resp.EventFunnelMonth {
		if e.EventType == storage.EventBlacklisted {
			t.Errorf("40 天前的事件不应在 month funnel 中，got count=%d", e.Count)
		}
	}
}
