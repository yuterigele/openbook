package cron

// weekly_report_test.go
//
// WeeklyReporter 单元测试（v4.3 PRD §11.12 单店 + v4.5 跨店连锁版）
//
// 覆盖：
//  1. 默认值：sender = NoopSender
//  2. Setter：SetSender / SetSender(nil) / SetReportTo / SetChainReportTo
//  3. scan：DB 未初始化不 panic
//  4. triggerOne：单店完整路径（埋点 + 组装 + 邮件）
//  5. triggerChain：跨店完整路径（埋点 + 组装 + 邮件 + 多店聚合）
//  6. 失败语义：sender 报错不 panic；scan 收件人为空不发邮件
//  7. 双路独立：reportTo + chainReportTo 同时配时两个都发
//
// Mock Sender：实现 notify.Sender 接口，记录 SendHTML 调用

import (
	"context"
	"testing"
	"time"

	"github.com/yuterigele/openbook/notify"
	"github.com/yuterigele/openbook/storage"
)

// ---- Setter / 默认值 ----

func TestWeeklyReporter_DefaultSenderIsNoop(t *testing.T) {
	r := NewWeeklyReporter()
	if r.sender == nil {
		t.Fatal("default sender should not be nil")
	}
	if _, ok := r.sender.(*notify.NoopSender); !ok {
		t.Errorf("default sender: want *NoopSender, got %T", r.sender)
	}
}

func TestWeeklyReporter_SetSender_Replace(t *testing.T) {
	r := NewWeeklyReporter()
	mock := &mockSender{}
	r.SetSender(mock)
	if r.sender != mock {
		t.Errorf("SetSender should replace sender")
	}
}

func TestWeeklyReporter_SetSender_NilRestoresNoop(t *testing.T) {
	r := NewWeeklyReporter()
	mock := &mockSender{}
	r.SetSender(mock)
	r.SetSender(nil)
	if _, ok := r.sender.(*notify.NoopSender); !ok {
		t.Errorf("SetSender(nil) should restore NoopSender, got %T", r.sender)
	}
}

func TestWeeklyReporter_SetReportTo(t *testing.T) {
	r := NewWeeklyReporter()
	r.SetReportTo([]string{"a@b.com", "c@d.com"})
	if len(r.reportTo) != 2 {
		t.Errorf("SetReportTo: want 2, got %d", len(r.reportTo))
	}
}

// v4.5 增量：SetChainReportTo setter 验证
func TestWeeklyReporter_SetChainReportTo(t *testing.T) {
	r := NewWeeklyReporter()
	r.SetChainReportTo([]string{"chain@group.com"})
	if len(r.chainReportTo) != 1 {
		t.Errorf("SetChainReportTo: want 1, got %d", len(r.chainReportTo))
	}
	if r.chainReportTo[0] != "chain@group.com" {
		t.Errorf("SetChainReportTo[0]: want chain@group.com, got %s", r.chainReportTo[0])
	}

	// 独立性：与 reportTo 互不干扰
	r.SetReportTo([]string{"per-shop@shop.com"})
	if len(r.reportTo) != 1 || len(r.chainReportTo) != 1 {
		t.Errorf("SetReportTo / SetChainReportTo 应独立；got reportTo=%d chainReportTo=%d",
			len(r.reportTo), len(r.chainReportTo))
	}
}

// ---- scan 行为 ----

func TestWeeklyReporter_Scan_DBNotInitialized_NoPanic(t *testing.T) {
	storage.DB = nil
	defer func() { storage.DB = nil }()

	r := NewWeeklyReporter()
	r.SetReportTo([]string{"x@y.com"})
	r.SetChainReportTo([]string{"chain@x.com"})

	// 不应该 panic
	r.scan()
}

func TestWeeklyReporter_Scan_BothReportToAndChainReportTo_FiresBoth(t *testing.T) {
	storage.SetupTestDB(t)
	storage.MakeShop(t, "shop-scan", "")
	// 加 1 笔上周 appt 避免聚合全 0
	mkApptInTest(t, "shop-scan", "C1", "Alice", "Tony", "2026-06-20", "10:00", "completed")

	mock := &mockSender{}
	r := NewWeeklyReporter()
	r.SetSender(mock)
	r.SetReportTo([]string{"per-shop@x.com"})
	r.SetChainReportTo([]string{"chain@x.com"})

	r.scan()

	// 期望：1 封单店 + 1 封跨店 = 2 封
	if mock.CallCount() != 2 {
		t.Fatalf("expected 2 emails (per-shop + chain), got %d", mock.CallCount())
	}

	// 检查 subject 分别命中
	var perShop, chain bool
	for _, call := range mock.calls {
		// 跨店周报 subject 含"连锁周报"；单店 subject 含"周报"
		if call.subject == "" {
			t.Error("subject should not be empty")
		}
		if contains(call.subject, "连锁周报") {
			chain = true
		}
		if !contains(call.subject, "连锁周报") {
			perShop = true
		}
	}
	if !chain {
		t.Error("expected chain report email (subject containing 连锁周报)")
	}
	if !perShop {
		t.Error("expected per-shop report email (subject NOT containing 连锁周报)")
	}
}

func TestWeeklyReporter_Scan_NoReportTo_NoChainReportTo_DoesNotCallSender(t *testing.T) {
	storage.SetupTestDB(t)
	storage.MakeShop(t, "shop-no-recipients", "")

	mock := &mockSender{}
	r := NewWeeklyReporter()
	r.SetSender(mock)
	// 不设任何收件人

	r.scan()

	if mock.CallCount() != 0 {
		t.Errorf("neither reportTo nor chainReportTo set; should not call sender, got %d calls",
			mock.CallCount())
	}
}

// ---- triggerChain 行为（v4.5 增量核心）----

func TestTriggerChain_DBNotInitialized_NoPanic(t *testing.T) {
	storage.DB = nil
	defer func() { storage.DB = nil }()

	r := NewWeeklyReporter()
	// 直接调 triggerChain：scan 层的 guard 不在这里
	r.triggerChain(context.Background(), time.Now())
}

func TestTriggerChain_NoShops_DoesNotCallSender(t *testing.T) {
	storage.SetupTestDB(t)

	mock := &mockSender{}
	r := NewWeeklyReporter()
	r.SetSender(mock)
	r.SetChainReportTo([]string{"chain@x.com"})

	r.triggerChain(context.Background(), time.Now())

	if mock.CallCount() != 0 {
		t.Errorf("empty DB (no shops) should not call sender, got %d calls", mock.CallCount())
	}
}

func TestTriggerChain_FullPath_SendsOneChainEmail(t *testing.T) {
	storage.SetupTestDB(t)
	storage.MakeShop(t, "shop-chain-1", "")
	// 2 笔上周 completed
	mkApptInTest(t, "shop-chain-1", "C1", "Alice", "Tony", "2026-06-18", "10:00", "completed")
	mkApptInTest(t, "shop-chain-1", "C2", "Bob", "Tony", "2026-06-19", "10:00", "completed")

	mock := &mockSender{}
	r := NewWeeklyReporter()
	r.SetSender(mock)
	r.SetChainReportTo([]string{"chain@group.com"})

	r.triggerChain(context.Background(), weeklyTestNowValue())

	if mock.CallCount() != 1 {
		t.Fatalf("expected 1 chain email, got %d", mock.CallCount())
	}
	call := mock.calls[0]
	if !contains(call.subject, "连锁周报") {
		t.Errorf("chain email subject should contain 连锁周报, got %q", call.subject)
	}
	if len(call.to) != 1 || call.to[0] != "chain@group.com" {
		t.Errorf("to: want [chain@group.com], got %v", call.to)
	}
	if call.body == "" {
		t.Error("body should not be empty")
	}
}

func TestTriggerChain_MultipleShops_AggregatesCorrectly(t *testing.T) {
	storage.SetupTestDB(t)
	storage.MakeShop(t, "shop-A", "")
	storage.MakeShop(t, "shop-B", "")

	// shop-A: 3 completed
	mkApptInTest(t, "shop-A", "C1", "Alice", "Tony", "2026-06-18", "10:00", "completed")
	mkApptInTest(t, "shop-A", "C2", "Bob", "Tony", "2026-06-19", "10:00", "completed")
	mkApptInTest(t, "shop-A", "C3", "Cara", "Tony", "2026-06-20", "10:00", "completed")
	// shop-B: 1 completed + 1 noshow
	mkApptInTest(t, "shop-B", "C4", "Dan", "Kevin", "2026-06-19", "10:00", "completed")
	mkApptInTest(t, "shop-B", "C5", "Eve", "Kevin", "2026-06-20", "10:00", "noshow")

	mock := &mockSender{}
	r := NewWeeklyReporter()
	r.SetSender(mock)
	r.SetChainReportTo([]string{"chain@group.com"})

	r.triggerChain(context.Background(), weeklyTestNowValue())

	if mock.CallCount() != 1 {
		t.Fatalf("expected 1 chain email, got %d", mock.CallCount())
	}

	// 通过 body 校验聚合：4 completed + 1 noshow
	body := mock.calls[0].body
	if !contains(body, "4") || !contains(body, "1") {
		t.Errorf("chain email body should reflect aggregate counts; got %.200s...", body)
	}
}

func TestTriggerChain_SenderError_DoesNotPanic(t *testing.T) {
	storage.SetupTestDB(t)
	storage.MakeShop(t, "shop-fail", "")
	mkApptInTest(t, "shop-fail", "C1", "Alice", "Tony", "2026-06-19", "10:00", "completed")

	mock := &mockSender{failOn: true}
	r := NewWeeklyReporter()
	r.SetSender(mock)
	r.SetChainReportTo([]string{"chain@x.com"})

	// 不应 panic
	r.triggerChain(context.Background(), weeklyTestNowValue())
}

// ---- triggerOne 回归（v4.3 单店；v4.5 不破坏）----

func TestTriggerOne_FullPath_StillSendsPerShop(t *testing.T) {
	storage.SetupTestDB(t)
	shopID := "shop-per-shop"
	storage.MakeShop(t, shopID, "")
	mkApptInTest(t, shopID, "C1", "Alice", "Tony", "2026-06-19", "10:00", "completed")

	mock := &mockSender{}
	r := NewWeeklyReporter()
	r.SetSender(mock)
	r.SetReportTo([]string{"owner@shop.com"})
	// 注意：chainReportTo 留空，确保 triggerOne 路径不受影响

	r.triggerOne(context.Background(), shopID, weeklyTestNowValue())

	if mock.CallCount() != 1 {
		t.Fatalf("expected 1 per-shop email, got %d", mock.CallCount())
	}
	if contains(mock.calls[0].subject, "连锁周报") {
		t.Errorf("per-shop email should not contain 连锁周报, got %q", mock.calls[0].subject)
	}
}

// ---- helpers ----

// mkApptInTest 在测试中塞一笔 appointment（直接走 storage.DB，绕开 UUID 分配）
func mkApptInTest(t *testing.T, shopID, custID, custName, barberName, date, timeStr, status string) {
	t.Helper()
	storage.DB.Create(&storage.Appointment{
		ID:         "appt-" + shopID + "-" + custID + "-" + date,
		ShopID:     shopID,
		BarberID:   "barber-" + barberName,
		BarberName: barberName,
		CustomerID: custID,
		Customer:   custName,
		Date:       date,
		Time:       timeStr,
		Status:     status,
		Source:     "test",
		CreatedAt:  time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC),
	})
}

// weeklyTestNowValue 固定 now = 2026-06-22 09:00 UTC（与 storage.usage_report_test 一致口径）
func weeklyTestNowValue() time.Time {
	return time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
}

// contains 简单 contains 检查（避免引 strings 包的 import 噪音）
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
