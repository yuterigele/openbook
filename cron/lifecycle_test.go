package cron

// lifecycle_test.go
//
// LifecycleTrigger 单元测试（v4.2 PRD §11.11 D+15 集成）
//
// 覆盖：
//   1. NewLifecycleTrigger 默认 sender = NoopSender（不报错）
//   2. SetSender 替换 sender；SetSender(nil) 恢复 Noop
//   3. SetReportTo 接收空 / 多收件人
//   4. triggerD15Report 在 DB 未初始化时不 panic
//   5. triggerD15Report 在无 first_appointment 时 skip + 不发邮件（用 mock sender 验证）
//   6. triggerD15Report 完整路径：埋点 + 组装报告 + 邮件 + 微信（用 mock 验证）
//   7. findFirstApptAt：DB nil / 找不到 / 找到
//
// Mock Sender：实现 notify.Sender 接口，记录 SendHTML 调用

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/notify"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// mockSender 记录 SendHTML 调用，便于断言
type mockSender struct {
	mu     sync.Mutex
	calls  []mockSendCall
	failOn bool
}

type mockSendCall struct {
	to      []string
	subject string
	body    string
}

func (m *mockSender) SendHTML(ctx context.Context, to []string, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failOn {
		return errMockSend
	}
	m.calls = append(m.calls, mockSendCall{to: to, subject: subject, body: body})
	return nil
}

func (m *mockSender) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

var errMockSend = &mockSendError{msg: "mock send failure"}

type mockSendError struct{ msg string }

func (e *mockSendError) Error() string { return e.msg }

// ---- Setter / 默认值 ----

func TestLifecycleTrigger_DefaultSenderIsNoop(t *testing.T) {
	l := NewLifecycleTrigger(nil)
	if l.sender == nil {
		t.Fatal("default sender should not be nil")
	}
	if _, ok := l.sender.(*notify.NoopSender); !ok {
		t.Errorf("default sender: want *NoopSender, got %T", l.sender)
	}
}

func TestLifecycleTrigger_SetSender_Replace(t *testing.T) {
	l := NewLifecycleTrigger(nil)
	mock := &mockSender{}
	l.SetSender(mock)
	if l.sender != mock {
		t.Errorf("SetSender should replace sender")
	}
}

func TestLifecycleTrigger_SetSender_NilRestoresNoop(t *testing.T) {
	l := NewLifecycleTrigger(nil)
	l.SetSender(&mockSender{})
	l.SetSender(nil)
	if _, ok := l.sender.(*notify.NoopSender); !ok {
		t.Errorf("SetSender(nil) should restore NoopSender, got %T", l.sender)
	}
}

func TestLifecycleTrigger_SetReportTo(t *testing.T) {
	l := NewLifecycleTrigger(nil)
	l.SetReportTo([]string{"a@b.com", "c@d.com"})
	if len(l.reportTo) != 2 {
		t.Errorf("SetReportTo: want 2, got %d", len(l.reportTo))
	}
}

// ---- triggerD15Report 行为 ----

func TestTriggerD15Report_DBNotInitialized_NoPanic(t *testing.T) {
	storage.DB = nil
	defer func() { storage.DB = nil }()

	l := NewLifecycleTrigger(nil)
	l.triggerD15Report(context.Background(), "shop-x")
	// 不 panic 就算过
}

func TestTriggerD15Report_NoFirstAppt_SkipsReport(t *testing.T) {
	storage.SetupTestDB(t)
	storage.MakeShop(t, "shop-no-first", "")

	mock := &mockSender{}
	l := NewLifecycleTrigger(nil)
	l.SetSender(mock)
	l.SetReportTo([]string{"owner@shop.com"})

	l.triggerD15Report(context.Background(), "shop-no-first")

	if mock.CallCount() != 0 {
		t.Errorf("should not send email without first_appointment, got %d calls", mock.CallCount())
	}
}

func TestTriggerD15Report_NoReportTo_DoesNotCallSender(t *testing.T) {
	storage.SetupTestDB(t)
	shopID := "shop-no-recipient"
	storage.MakeShop(t, shopID, "")

	// 写 first_appointment 事件 + 几条 appointments
	firstAt := time.Now().AddDate(0, 0, -14)
	storage.TrackEvent(context.Background(), shopID, storage.EventFirstAppointment, "", nil)
	storage.DB.Create(&storage.Appointment{
		ID:         "a1",
		ShopID:     shopID,
		BarberID:   "barber-Tony",
		BarberName: "Tony",
		CustomerID: "c1",
		Customer:   "Alice",
		Date:       firstAt.AddDate(0, 0, 5).Format("2006-01-02"),
		Time:       "10:00",
		Status:     "completed",
		Source:     "test",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})

	mock := &mockSender{}
	l := NewLifecycleTrigger(nil)
	l.SetSender(mock)
	// 注意：没调 SetReportTo → 收件人为空

	l.triggerD15Report(context.Background(), shopID)

	if mock.CallCount() != 0 {
		t.Errorf("empty reportTo should not call sender, got %d calls", mock.CallCount())
	}
}

func TestTriggerD15Report_FullPath_SendsEmail(t *testing.T) {
	storage.SetupTestDB(t)
	shopID := "shop-full"
	storage.MakeShop(t, shopID, "")

	// 写 first_appointment 事件
	firstAt := time.Now().AddDate(0, 0, -14)
	storage.TrackEvent(context.Background(), shopID, storage.EventFirstAppointment, "", nil)
	// 改 created_at 为 firstAt（TrackEvent 写 now）
	storage.DB.Model(&storage.EventLog{}).
		Where("shop_id = ? AND event_type = ?", shopID, storage.EventFirstAppointment).
		Update("created_at", firstAt)

	// 5 笔 completed + 1 笔 noshow
	for i := 0; i < 5; i++ {
		storage.DB.Create(&storage.Appointment{
			ID:         "a" + string(rune('a'+i)),
			ShopID:     shopID,
			BarberID:   "barber-Tony",
			BarberName: "Tony",
			CustomerID: "c1",
			Customer:   "Alice",
			Date:       firstAt.AddDate(0, 0, 5+i).Format("2006-01-02"),
			Time:       "10:00",
			Status:     "completed",
			Source:     "test",
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		})
	}
	storage.DB.Create(&storage.Appointment{
		ID:         "az",
		ShopID:     shopID,
		BarberID:   "barber-Tony",
		BarberName: "Tony",
		CustomerID: "c1",
		Customer:   "Alice",
		Date:       firstAt.AddDate(0, 0, 11).Format("2006-01-02"),
		Time:       "10:00",
		Status:     "noshow",
		Source:     "test",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})

	mock := &mockSender{}
	l := NewLifecycleTrigger(nil)
	l.SetSender(mock)
	l.SetReportTo([]string{"owner@shop.com"})

	l.triggerD15Report(context.Background(), shopID)

	if mock.CallCount() != 1 {
		t.Fatalf("expected 1 email, got %d", mock.CallCount())
	}
	call := mock.calls[0]
	if call.subject == "" {
		t.Error("subject should not be empty")
	}
	if call.body == "" {
		t.Error("body should not be empty")
	}
	if len(call.to) != 1 || call.to[0] != "owner@shop.com" {
		t.Errorf("to: want [owner@shop.com], got %v", call.to)
	}
}

func TestTriggerD15Report_SenderError_DoesNotPanic(t *testing.T) {
	storage.SetupTestDB(t)
	shopID := "shop-sender-fail"
	storage.MakeShop(t, shopID, "")

	firstAt := time.Now().AddDate(0, 0, -14)
	storage.TrackEvent(context.Background(), shopID, storage.EventFirstAppointment, "", nil)
	storage.DB.Model(&storage.EventLog{}).
		Where("shop_id = ? AND event_type = ?", shopID, storage.EventFirstAppointment).
		Update("created_at", firstAt)

	mock := &mockSender{failOn: true}
	l := NewLifecycleTrigger(nil)
	l.SetSender(mock)
	l.SetReportTo([]string{"owner@shop.com"})

	// 不应该 panic
	l.triggerD15Report(context.Background(), shopID)
}

// ---- findFirstApptAt ----

func TestFindFirstApptAt_DBNotInitialized(t *testing.T) {
	storage.DB = nil
	defer func() { storage.DB = nil }()

	l := NewLifecycleTrigger(nil)
	got := l.findFirstApptAt(context.Background(), "any")
	if !got.IsZero() {
		t.Errorf("DB nil should return zero time, got %v", got)
	}
}

func TestFindFirstApptAt_NoEvent(t *testing.T) {
	storage.SetupTestDB(t)
	storage.MakeShop(t, "shop-no-event", "")

	l := NewLifecycleTrigger(nil)
	got := l.findFirstApptAt(context.Background(), "shop-no-event")
	if !got.IsZero() {
		t.Errorf("no first_appointment event should return zero, got %v", got)
	}
}

func TestFindFirstApptAt_Found(t *testing.T) {
	storage.SetupTestDB(t)
	shopID := "shop-with-first"
	storage.MakeShop(t, shopID, "")

	want := time.Date(2026, 6, 7, 10, 30, 0, 0, time.UTC)
	storage.TrackEvent(context.Background(), shopID, storage.EventFirstAppointment, "", nil)
	storage.DB.Model(&storage.EventLog{}).
		Where("shop_id = ? AND event_type = ?", shopID, storage.EventFirstAppointment).
		Update("created_at", want)

	l := NewLifecycleTrigger(nil)
	got := l.findFirstApptAt(context.Background(), shopID)

	// 跨驱动时间容差 ±1s
	if got.Sub(want).Abs() > time.Second {
		t.Errorf("firstApptAt: want %v, got %v", want, got)
	}
}
