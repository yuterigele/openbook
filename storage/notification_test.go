package storage

// notification_test.go
//
// v4.10 leave notify 基础设施单测（2026-06-23）
//
// 覆盖：
//   - SendWithRetry：成功不重试、失败重试到上限、ctx cancel 提前返回
//   - ChannelSelector：4 种 customer 状态（KF / App / SMS / 无联系方式）
//   - ParallelSender：5 worker 并发、混合成功/失败、ctx cancel
//   - TruncatePreview：超长截断带省略号、不超长原样
//   - MarkSent/MarkFailed/MarkSkipped：DB row 状态回写正确
//
// Run:
//   go test ./storage/... -run "TestSendWithRetry|TestSelectChannel|TestParallelSender|TestTruncatePreview|TestNotificationStatus"

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ===================== SendWithRetry =====================

// fakeWeComSender 一个可控的 sender，用于测试重试
type fakeWeComSender struct {
	calls    atomic.Int32
	failN    int    // 前 failN 次返回 err
	errToRet error  // 返回的错误
}

func (f *fakeWeComSender) Send(_ context.Context, _, _ string) error {
	n := f.calls.Add(1)
	if int(n) <= f.failN {
		return f.errToRet
	}
	return nil
}

func TestSendWithRetry_SuccessFirstTry(t *testing.T) {
	s := &fakeWeComSender{}
	err := SendWithRetry(context.Background(), s, "target", "content", SendOptions{
		MaxAttempts:    3,
		InitialBackoff: 1 * time.Millisecond, // 测试用短间隔
		MaxBackoff:     10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("SendWithRetry should succeed: %v", err)
	}
	if s.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (no retry needed)", s.calls.Load())
	}
}

func TestSendWithRetry_FailTwiceThenSuccess(t *testing.T) {
	s := &fakeWeComSender{failN: 2, errToRet: errors.New("network blip")}
	err := SendWithRetry(context.Background(), s, "target", "content", SendOptions{
		MaxAttempts:    3,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("SendWithRetry should eventually succeed: %v", err)
	}
	if s.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (fail 2 + success 1)", s.calls.Load())
	}
}

func TestSendWithRetry_AllAttemptsFail(t *testing.T) {
	s := &fakeWeComSender{failN: 100, errToRet: errors.New("permanent fail")}
	err := SendWithRetry(context.Background(), s, "target", "content", SendOptions{
		MaxAttempts:    3,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
	})
	if err == nil {
		t.Fatalf("SendWithRetry should fail after all attempts")
	}
	if s.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (all attempts exhausted)", s.calls.Load())
	}
}

func TestSendWithRetry_CtxCancelMidway(t *testing.T) {
	s := &fakeWeComSender{failN: 100, errToRet: errors.New("always fail")}
	ctx, cancel := context.WithCancel(context.Background())
	// 让首次失败后立刻 cancel
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	err := SendWithRetry(ctx, s, "target", "content", SendOptions{
		MaxAttempts:    5,
		InitialBackoff: 100 * time.Millisecond, // 退避时间长一点让 cancel 抢先
		MaxBackoff:     1 * time.Second,
	})
	if err == nil {
		t.Fatalf("SendWithRetry should return ctx-canceled error")
	}
	// 应该停在 1-2 次之间（不会跑完 5 次）
	if s.calls.Load() >= 5 {
		t.Errorf("calls = %d, should be < 5 due to ctx cancel", s.calls.Load())
	}
}

// ===================== ChannelSelector =====================

func TestSelectChannel_NoCustomer(t *testing.T) {
	d := SelectChannel(nil)
	if d.HasContact {
		t.Errorf("nil customer should have HasContact=false")
	}
}

func TestSelectChannel_PrefersExternalUserID(t *testing.T) {
	c := &Customer{
		ExternalUserID: "ext-123",
		WechatOpenID:   "wx-456", // 也存在但优先级低
		Phone:          "13800138000",
	}
	d := SelectChannel(c)
	if d.Channel != NotifChannelWeComKF {
		t.Errorf("Channel = %q, want %q", d.Channel, NotifChannelWeComKF)
	}
	if d.Target != "ext-123" {
		t.Errorf("Target = %q, want ext-123", d.Target)
	}
	if !d.HasContact {
		t.Errorf("HasContact should be true")
	}
}

func TestSelectChannel_FallbackToWeChatOpenID(t *testing.T) {
	c := &Customer{
		ExternalUserID: "", // 空 → 跳过
		WechatOpenID:   "wx-456",
		Phone:          "13800138000",
	}
	d := SelectChannel(c)
	if d.Channel != NotifChannelWeComApp {
		t.Errorf("Channel = %q, want %q", d.Channel, NotifChannelWeComApp)
	}
	if d.Target != "wx-456" {
		t.Errorf("Target = %q, want wx-456", d.Target)
	}
}

func TestSelectChannel_FallbackToPhone(t *testing.T) {
	c := &Customer{
		ExternalUserID: "",
		WechatOpenID:   "",
		Phone:          "13800138000",
	}
	d := SelectChannel(c)
	if d.Channel != NotifChannelSMS {
		t.Errorf("Channel = %q, want %q", d.Channel, NotifChannelSMS)
	}
	if d.Target != "13800138000" {
		t.Errorf("Target = %q, want phone", d.Target)
	}
}

func TestSelectChannel_NoContact(t *testing.T) {
	c := &Customer{} // 都没
	d := SelectChannel(c)
	if d.HasContact {
		t.Errorf("empty customer should have HasContact=false")
	}
}

// ===================== ParallelSender =====================

// countingSender 计数 + 可控失败
type countingSender struct {
	calls atomic.Int32
	fail  bool
}

func (c *countingSender) Send(_ context.Context, _, _ string) error {
	c.calls.Add(1)
	if c.fail {
		return errors.New("simulated failure")
	}
	return nil
}

func TestParallelSender_AllSucceed(t *testing.T) {
	ps := &ParallelSender{Concurrency: 3}
	tasks := make([]SendTask, 10)
	s := &countingSender{}
	for i := range tasks {
		tasks[i] = SendTask{
			ID:      uint64(i + 1),
			Target:  "target",
			Content: "content",
			Channel: NotifChannelWeComKF,
			Sender:  s,
		}
	}
	results := ps.SendAll(context.Background(), tasks, SendOptions{
		MaxAttempts:    1, // 不重试，加快测试
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
	})
	if len(results) != 10 {
		t.Fatalf("results = %d, want 10", len(results))
	}
	for i, r := range results {
		if !r.Success {
			t.Errorf("result[%d] Success = false", i)
		}
	}
	if s.calls.Load() != 10 {
		t.Errorf("calls = %d, want 10", s.calls.Load())
	}
}

func TestParallelSender_MixedSuccessFailure(t *testing.T) {
	ps := &ParallelSender{Concurrency: 2}
	// 5 个 task：1/3/5 失败，2/4 成功
	tasks := make([]SendTask, 5)
	failSender := &countingSender{fail: true}
	okSender := &countingSender{fail: false}
	for i := range tasks {
		tasks[i] = SendTask{
			ID:      uint64(i + 1),
			Target:  "target",
			Content: "content",
			Channel: NotifChannelWeComKF,
		}
		if i%2 == 0 {
			tasks[i].Sender = failSender
		} else {
			tasks[i].Sender = okSender
		}
	}
	results := ps.SendAll(context.Background(), tasks, SendOptions{
		MaxAttempts:    1,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
	})
	if len(results) != 5 {
		t.Fatalf("results = %d, want 5", len(results))
	}
	successCount, failCount := 0, 0
	for i, r := range results {
		// task ID 1/3/5 (i=0,2,4) 失败，2/4 (i=1,3) 成功
		if i%2 == 0 {
			if r.Success {
				t.Errorf("task %d should fail", i)
			}
			failCount++
		} else {
			if !r.Success {
				t.Errorf("task %d should succeed", i)
			}
			successCount++
		}
	}
	if successCount != 2 || failCount != 3 {
		t.Errorf("success=%d fail=%d, want 2/3", successCount, failCount)
	}
}

func TestParallelSender_Empty(t *testing.T) {
	ps := &ParallelSender{Concurrency: 3}
	results := ps.SendAll(context.Background(), nil, SendOptions{})
	if len(results) != 0 {
		t.Errorf("empty tasks should return empty results")
	}
}

// ===================== TruncatePreview =====================

func TestTruncatePreview_NoTruncation(t *testing.T) {
	s := "短文案"
	got := TruncatePreview(s, 256)
	if got != s {
		t.Errorf("got %q, want %q", got, s)
	}
}

func TestTruncatePreview_LongTruncated(t *testing.T) {
	long := strings.Repeat("a", 300)
	got := TruncatePreview(long, 256)
	// rune 数 = 257（256 + 省略号占 1 rune）
	if len([]rune(got)) != 257 {
		t.Errorf("len = %d runes, want 257", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated string should end with …")
	}
}

func TestTruncatePreview_DefaultMaxLen(t *testing.T) {
	long := strings.Repeat("x", 1000)
	got := TruncatePreview(long, 0) // 用默认 256
	if len([]rune(got)) != 257 {
		t.Errorf("default maxLen should be 256; got %d runes", len([]rune(got)))
	}
}

// ===================== NotificationStatus CRUD =====================

func TestNotificationLifecycle_SentPath(t *testing.T) {
	SetupTestDB(t)

	// 1) Create
	n := &CustomerNotification{
		LeaveID:       "leave-1",
		AppointmentID: "appt-1",
		ShopID:        "shop-1",
		CustomerID:    "cust-1",
		Type:          NotifTypeLeaveCancel,
	}
	id, err := CreateCustomerNotification(context.Background(), n)
	if err != nil {
		t.Fatalf("CreateCustomerNotification: %v", err)
	}
	if id == 0 {
		t.Fatalf("expected non-zero id")
	}

	// 2) 验证初始状态
	var got CustomerNotification
	if err := DB.First(&got, id).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if got.Status != NotifStatusPending {
		t.Errorf("initial Status = %q, want pending", got.Status)
	}

	// 3) MarkSent
	if err := MarkNotificationSent(context.Background(), id, 1); err != nil {
		t.Fatalf("MarkNotificationSent: %v", err)
	}
	if err := DB.First(&got, id).Error; err != nil {
		t.Fatalf("First after sent: %v", err)
	}
	if got.Status != NotifStatusSent {
		t.Errorf("Status after sent = %q, want sent", got.Status)
	}
	if got.SentAt == nil {
		t.Errorf("SentAt should be set after MarkSent")
	}
	if got.AttemptCount != 1 {
		t.Errorf("AttemptCount = %d, want 1", got.AttemptCount)
	}
}

func TestNotificationLifecycle_FailedPath(t *testing.T) {
	SetupTestDB(t)

	n := &CustomerNotification{
		LeaveID:    "leave-1",
		ShopID:     "shop-1",
		CustomerID: "cust-1",
		Type:       NotifTypeLeaveCancel,
	}
	id, _ := CreateCustomerNotification(context.Background(), n)

	if err := MarkNotificationFailed(context.Background(), id, 3, "wechat api 95004"); err != nil {
		t.Fatalf("MarkNotificationFailed: %v", err)
	}
	var got CustomerNotification
	DB.First(&got, id)
	if got.Status != NotifStatusFailed {
		t.Errorf("Status = %q, want failed", got.Status)
	}
	if got.AttemptCount != 3 {
		t.Errorf("AttemptCount = %d, want 3", got.AttemptCount)
	}
	if got.ErrorMessage != "wechat api 95004" {
		t.Errorf("ErrorMessage = %q, want wechat api 95004", got.ErrorMessage)
	}
}

func TestNotificationLifecycle_SkippedPath(t *testing.T) {
	SetupTestDB(t)

	n := &CustomerNotification{
		LeaveID:    "leave-1",
		ShopID:     "shop-1",
		CustomerID: "",
		Type:       NotifTypeLeaveNoContact,
	}
	id, _ := CreateCustomerNotification(context.Background(), n)

	if err := MarkNotificationSkipped(context.Background(), id, "customer has no contact"); err != nil {
		t.Fatalf("MarkNotificationSkipped: %v", err)
	}
	var got CustomerNotification
	DB.First(&got, id)
	if got.Status != NotifStatusSkipped {
		t.Errorf("Status = %q, want skipped", got.Status)
	}
	if got.ErrorMessage != "customer has no contact" {
		t.Errorf("ErrorMessage = %q", got.ErrorMessage)
	}
}

func TestNotificationLifecycle_LongErrorTruncated(t *testing.T) {
	SetupTestDB(t)
	n := &CustomerNotification{LeaveID: "l1", ShopID: "s1", CustomerID: "c1"}
	id, _ := CreateCustomerNotification(context.Background(), n)

	long := strings.Repeat("x", 1000)
	if err := MarkNotificationFailed(context.Background(), id, 1, long); err != nil {
		t.Fatalf("MarkNotificationFailed: %v", err)
	}
	var got CustomerNotification
	DB.First(&got, id)
	if len(got.ErrorMessage) > 512 {
		t.Errorf("ErrorMessage len = %d, should be truncated to 512", len(got.ErrorMessage))
	}
}

func TestListNotificationsByLeave(t *testing.T) {
	SetupTestDB(t)
	// 同一 leave 写 3 条
	for i := 0; i < 3; i++ {
		CreateCustomerNotification(context.Background(), &CustomerNotification{
			LeaveID:    "leave-99",
			ShopID:     "shop-1",
			CustomerID: "cust-" + string(rune('A'+i)),
			Type:       NotifTypeLeaveCancel,
		})
	}
	// 另一 leave 写 1 条（不应出现在结果中）
	CreateCustomerNotification(context.Background(), &CustomerNotification{
		LeaveID:    "leave-other",
		ShopID:     "shop-1",
		CustomerID: "cust-Z",
		Type:       NotifTypeLeaveCancel,
	})
	list, err := ListNotificationsByLeave(context.Background(), "leave-99")
	if err != nil {
		t.Fatalf("ListNotificationsByLeave: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("len(list) = %d, want 3", len(list))
	}
}

func TestListPendingNotifications(t *testing.T) {
	SetupTestDB(t)
	// shop-1: 2 pending + 1 failed + 1 sent → 应返回 3
	CreateCustomerNotification(context.Background(), &CustomerNotification{LeaveID: "l", ShopID: "shop-1", CustomerID: "a", Status: NotifStatusPending})
	CreateCustomerNotification(context.Background(), &CustomerNotification{LeaveID: "l", ShopID: "shop-1", CustomerID: "b", Status: NotifStatusPending})
	id, _ := CreateCustomerNotification(context.Background(), &CustomerNotification{LeaveID: "l", ShopID: "shop-1", CustomerID: "c", Status: NotifStatusPending})
	MarkNotificationFailed(context.Background(), id, 1, "err")
	// sent 不应出现
	CreateCustomerNotification(context.Background(), &CustomerNotification{LeaveID: "l", ShopID: "shop-1", CustomerID: "d", Status: NotifStatusSent})
	// 另一个 shop 不应出现
	CreateCustomerNotification(context.Background(), &CustomerNotification{LeaveID: "l", ShopID: "shop-2", CustomerID: "x", Status: NotifStatusPending})

	list, err := ListPendingNotifications(context.Background(), "shop-1", 100)
	if err != nil {
		t.Fatalf("ListPendingNotifications: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("len = %d, want 3 (2 pending + 1 failed)", len(list))
	}
}

// ===================== ListNotificationsForShop =====================

func TestListNotificationsForShop_FilterByStatus(t *testing.T) {
	SetupTestDB(t)
	// shop-1: 1 sent + 1 failed + 1 pending + 1 skipped → status=failed 应只返回 1
	id1, _ := CreateCustomerNotification(context.Background(), &CustomerNotification{LeaveID: "l1", ShopID: "shop-1", CustomerID: "c1", Status: NotifStatusSent})
	_ = id1
	id2, _ := CreateCustomerNotification(context.Background(), &CustomerNotification{LeaveID: "l1", ShopID: "shop-1", CustomerID: "c2", Status: NotifStatusFailed})
	_ = id2
	CreateCustomerNotification(context.Background(), &CustomerNotification{LeaveID: "l1", ShopID: "shop-1", CustomerID: "c3", Status: NotifStatusPending})
	CreateCustomerNotification(context.Background(), &CustomerNotification{LeaveID: "l1", ShopID: "shop-1", CustomerID: "c4", Status: NotifStatusSkipped})
	// 另一个 shop，不应出现
	CreateCustomerNotification(context.Background(), &CustomerNotification{LeaveID: "l1", ShopID: "shop-2", CustomerID: "c5", Status: NotifStatusFailed})

	list, err := ListNotificationsForShop(context.Background(), "shop-1", NotifStatusFailed, "", "", 100)
	if err != nil {
		t.Fatalf("ListNotificationsForShop: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("len = %d, want 1", len(list))
	}
	if list[0].CustomerID != "c2" {
		t.Errorf("got customer %s, want c2", list[0].CustomerID)
	}
}

func TestListNotificationsForShop_FilterByLeaveID(t *testing.T) {
	SetupTestDB(t)
	CreateCustomerNotification(context.Background(), &CustomerNotification{LeaveID: "leave-A", ShopID: "shop-1", CustomerID: "c1", Status: NotifStatusFailed})
	CreateCustomerNotification(context.Background(), &CustomerNotification{LeaveID: "leave-B", ShopID: "shop-1", CustomerID: "c2", Status: NotifStatusFailed})

	list, err := ListNotificationsForShop(context.Background(), "shop-1", NotifStatusFailed, "", "leave-A", 100)
	if err != nil {
		t.Fatalf("ListNotificationsForShop: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("len = %d, want 1", len(list))
	}
	if list[0].LeaveID != "leave-A" {
		t.Errorf("got leave %s, want leave-A", list[0].LeaveID)
	}
}

func TestListNotificationsForShop_LimitCap(t *testing.T) {
	SetupTestDB(t)
	// 写 600 条；limit=0 时应被截到 200
	for i := 0; i < 600; i++ {
		CreateCustomerNotification(context.Background(), &CustomerNotification{
			LeaveID: "l", ShopID: "shop-1", CustomerID: "c", Status: NotifStatusFailed,
		})
	}
	list, err := ListNotificationsForShop(context.Background(), "shop-1", NotifStatusFailed, "", "", 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(list) != 200 {
		t.Errorf("len = %d, want 200 (default limit)", len(list))
	}
}

// ===================== RetryNotification =====================

// fakeRetrySender 记录调用，按 calls 控制失败
type fakeRetrySender struct {
	calls      int
	failWith   error
	calledWith struct {
		apptID string
		text   string
	}
}

func (f *fakeRetrySender) send(ctx context.Context, appt *Appointment, text string) error {
	f.calls++
	if appt != nil {
		f.calledWith.apptID = appt.ID
	}
	f.calledWith.text = text
	return f.failWith
}

func TestRetryNotification_NotFound(t *testing.T) {
	SetupTestDB(t)
	_, err := RetryNotification(context.Background(), 9999, func(ctx context.Context, appt *Appointment, text string) error {
		return nil
	})
	if !errors.Is(err, ErrNotificationNotFound) {
		t.Errorf("err = %v, want ErrNotificationNotFound", err)
	}
}

func TestRetryNotification_NilSender(t *testing.T) {
	SetupTestDB(t)
	id, _ := CreateCustomerNotification(context.Background(), &CustomerNotification{
		LeaveID: "l", ShopID: "shop-1", CustomerID: "c", Status: NotifStatusFailed,
	})
	_, err := RetryNotification(context.Background(), id, nil)
	if err == nil {
		t.Errorf("nil sender should error")
	}
}

func TestRetryNotification_AlreadySent_Refuses(t *testing.T) {
	SetupTestDB(t)
	// 准备 appt（理论上 retry 不会取到这一步，但为了完整）
	MakeAppointment(t, "shop-1", "cust-1", "Alice", "Tony", "2026-06-25", "10:00")

	id, _ := CreateCustomerNotification(context.Background(), &CustomerNotification{
		LeaveID:       "l",
		ShopID:        "shop-1",
		AppointmentID: "non-existent", // 给个非空值进入判断分支
		CustomerID:    "cust-1",
		Status:        NotifStatusSent, // 已发
		TextPreview:   "x",
	})
	// 注意：MarkSent 不需要 appointmentID；我们直接看是否拒绝重发
	senderCalled := false
	_, err := RetryNotification(context.Background(), id, func(ctx context.Context, appt *Appointment, text string) error {
		senderCalled = true
		return nil
	})
	if !errors.Is(err, ErrNotificationAlreadySent) {
		t.Errorf("err = %v, want ErrNotificationAlreadySent", err)
	}
	if senderCalled {
		t.Errorf("sender should NOT be called for already-sent notification")
	}
}

func TestRetryNotification_FailedToSent(t *testing.T) {
	SetupTestDB(t)
	appt := MakeAppointment(t, "shop-1", "cust-1", "Alice", "Tony", "2026-06-25", "10:00")
	id, _ := CreateCustomerNotification(context.Background(), &CustomerNotification{
		LeaveID:       "leave-1",
		ShopID:        "shop-1",
		AppointmentID: appt.ID,
		CustomerID:    "cust-1",
		Type:          NotifTypeLeaveCancel,
		Status:        NotifStatusFailed,
		TextPreview:   "原通知",
		AttemptCount:  3,
	})
	sender := &fakeRetrySender{failWith: nil}
	res, err := RetryNotification(context.Background(), id, sender.send)
	if err != nil {
		t.Fatalf("RetryNotification: %v", err)
	}
	if res.NewStatus != NotifStatusSent {
		t.Errorf("NewStatus = %s, want sent", res.NewStatus)
	}
	if sender.calls != 1 {
		t.Errorf("sender.calls = %d, want 1", sender.calls)
	}
	if sender.calledWith.apptID != appt.ID {
		t.Errorf("sender got apptID %s, want %s", sender.calledWith.apptID, appt.ID)
	}
	if sender.calledWith.text != "原通知" {
		t.Errorf("sender got text %q, want 原通知 (use original TextPreview)", sender.calledWith.text)
	}
	// DB 验证：status=sent, attempt=4 (从3累加1), sent_at!=nil
	n, _, _ := GetNotificationByID(context.Background(), id)
	if n.Status != NotifStatusSent {
		t.Errorf("DB status = %s, want sent", n.Status)
	}
	if n.AttemptCount != 4 {
		t.Errorf("DB attempt = %d, want 4 (3+1)", n.AttemptCount)
	}
	if n.SentAt == nil {
		t.Errorf("DB sent_at should be set")
	}
}

func TestRetryNotification_StillFailed(t *testing.T) {
	SetupTestDB(t)
	appt := MakeAppointment(t, "shop-1", "cust-1", "Alice", "Tony", "2026-06-25", "10:00")
	id, _ := CreateCustomerNotification(context.Background(), &CustomerNotification{
		LeaveID:       "leave-1",
		ShopID:        "shop-1",
		AppointmentID: appt.ID,
		CustomerID:    "cust-1",
		Type:          NotifTypeLeaveCancel,
		Status:        NotifStatusFailed,
		AttemptCount:  3,
	})
	boom := errors.New("still broken")
	sender := &fakeRetrySender{failWith: boom}
	res, err := RetryNotification(context.Background(), id, sender.send)
	if err == nil {
		t.Fatalf("should still error")
	}
	if res.NewStatus != NotifStatusFailed {
		t.Errorf("NewStatus = %s, want failed", res.NewStatus)
	}
	n, _, _ := GetNotificationByID(context.Background(), id)
	if n.Status != NotifStatusFailed {
		t.Errorf("DB status = %s, want failed", n.Status)
	}
	if n.AttemptCount != 4 {
		t.Errorf("DB attempt = %d, want 4", n.AttemptCount)
	}
	if n.ErrorMessage == "" {
		t.Errorf("DB error_message should be set")
	}
}

func TestRetryNotification_NoContact_Skipped(t *testing.T) {
	SetupTestDB(t)
	appt := MakeAppointment(t, "shop-1", "cust-1", "Alice", "Tony", "2026-06-25", "10:00")
	id, _ := CreateCustomerNotification(context.Background(), &CustomerNotification{
		LeaveID:       "leave-1",
		ShopID:        "shop-1",
		AppointmentID: appt.ID,
		CustomerID:    "cust-1",
		Type:          NotifTypeLeaveCancel,
		Status:        NotifStatusSkipped,
	})
	sender := &fakeRetrySender{failWith: ErrNoCustomerContact}
	res, err := RetryNotification(context.Background(), id, sender.send)
	if !errors.Is(err, ErrNoCustomerContact) {
		t.Errorf("err = %v, want ErrNoCustomerContact", err)
	}
	if res.NewStatus != NotifStatusSkipped {
		t.Errorf("NewStatus = %s, want skipped", res.NewStatus)
	}
}

func TestRetryNotification_ApptMissing_FailsImmediately(t *testing.T) {
	SetupTestDB(t)
	id, _ := CreateCustomerNotification(context.Background(), &CustomerNotification{
		LeaveID:       "leave-1",
		ShopID:        "shop-1",
		AppointmentID: "non-existent-appt",
		CustomerID:    "cust-1",
		Status:        NotifStatusFailed,
	})
	sender := &fakeRetrySender{failWith: nil}
	_, err := RetryNotification(context.Background(), id, sender.send)
	if err == nil {
		t.Errorf("expected error when appt missing")
	}
	if sender.calls != 0 {
		t.Errorf("sender should NOT be called when appt missing")
	}
}

// ===================== RetryShopFailedNotifications =====================

func TestRetryShopFailedNotifications_BatchRetry(t *testing.T) {
	SetupTestDB(t)
	appt := MakeAppointment(t, "shop-1", "cust-1", "Alice", "Tony", "2026-06-25", "10:00")
	// 3 failed leave 通知
	for i := 0; i < 3; i++ {
		CreateCustomerNotification(context.Background(), &CustomerNotification{
			LeaveID:       "leave-1",
			ShopID:        "shop-1",
			AppointmentID: appt.ID,
			CustomerID:    "cust-" + string(rune('A'+i)),
			Type:          NotifTypeLeaveCancel,
			Status:        NotifStatusFailed,
		})
	}
	// 1 sent（不应该被重发）
	CreateCustomerNotification(context.Background(), &CustomerNotification{
		LeaveID:       "leave-1",
		ShopID:        "shop-1",
		AppointmentID: appt.ID,
		CustomerID:    "cust-D",
		Type:          NotifTypeLeaveCancel,
		Status:        NotifStatusSent,
	})
	// 另一个 shop（不应该被重发）
	CreateCustomerNotification(context.Background(), &CustomerNotification{
		LeaveID:       "leave-1",
		ShopID:        "shop-2",
		AppointmentID: appt.ID,
		CustomerID:    "cust-E",
		Type:          NotifTypeLeaveCancel,
		Status:        NotifStatusFailed,
	})

	sender := &fakeRetrySender{failWith: nil} // sender 全部成功
	suc, fail, err := RetryShopFailedNotifications(context.Background(), "shop-1", sender.send)
	if err != nil {
		t.Fatalf("RetryShopFailedNotifications: %v", err)
	}
	if suc != 3 {
		t.Errorf("succeeded = %d, want 3", suc)
	}
	if fail != 0 {
		t.Errorf("failed = %d, want 0", fail)
	}
}

func TestRetryShopFailedNotifications_NilSender(t *testing.T) {
	SetupTestDB(t)
	_, _, err := RetryShopFailedNotifications(context.Background(), "shop-1", nil)
	if err == nil {
		t.Errorf("nil sender should error")
	}
}