package server

// send_reply_test.go
//
// 覆盖 v4.13.1 修复：SendKfTextMessage 失败后**不再 fallback** 到 SendTextMessage
//   - 原代码：KF 接口失败 → fallback 应用消息接口 → external user 走应用消息必报 81013，
//     把真因（95001 未认证 / 95018 真人接管 / 95002 48h 超时）掩盖
//   - v4.13.1：失败打 ⚠️ 日志，直接冒泡真因；fallback 是死路，去掉
//
// 验证点：
//  1. KF 成功：调 SendKfTextMessage 一次（不调 SendTextMessage）+ log 含"客服回复成功"
//  2. KF 失败：不调 SendTextMessage（mock 计数器验证）+ log 含 ⚠️ 警告
//  3. 无 openKfID（admin API 路径）：走 SendTextMessage，行为不变
//
// Run:
//   go test ./server/... -v -run "TestSendReply"

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

// fakeReplySender 记录所有调用 + 可配置返回值
type fakeReplySender struct {
	kfErr          error
	kfCalls        int
	kfLastExternal string
	kfLastOpenKfID string
	kfLastContent  string

	textErr         error
	textCalls       int
	textLastUser    string
	textLastContent string
}

func (f *fakeReplySender) SendKfTextMessage(_ context.Context, externalUserID, openKfID, content string) error {
	f.kfCalls++
	f.kfLastExternal = externalUserID
	f.kfLastOpenKfID = openKfID
	f.kfLastContent = content
	return f.kfErr
}

func (f *fakeReplySender) SendTextMessage(_ context.Context, userID, content string) error {
	f.textCalls++
	f.textLastUser = userID
	f.textLastContent = content
	return f.textErr
}

// captureLog 把 log 输出重定向到 buffer，便于断言
func captureLog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0) // 关掉时间戳，断言更稳
	return &buf, func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}
}

// ===================== 1) KF 成功：调 KF 一次，不调 text，log 含"客服回复成功" =====================

func TestSendReply_KfSuccess(t *testing.T) {
	sender := &fakeReplySender{kfErr: nil}
	buf, restore := captureLog(t)
	defer restore()

	sendReply(context.Background(), sender, "ext-user-1", "open-kf-1", "hello", "shop-1")

	if sender.kfCalls != 1 {
		t.Errorf("SendKfTextMessage 调用次数 = %d, want 1", sender.kfCalls)
	}
	if sender.textCalls != 0 {
		t.Errorf("成功路径不应调 SendTextMessage, calls = %d", sender.textCalls)
	}
	if sender.kfLastExternal != "ext-user-1" || sender.kfLastOpenKfID != "open-kf-1" || sender.kfLastContent != "hello" {
		t.Errorf("参数不对：external=%q openKfID=%q content=%q", sender.kfLastExternal, sender.kfLastOpenKfID, sender.kfLastContent)
	}
	if !strings.Contains(buf.String(), "客服回复成功") {
		t.Errorf("log 应含'客服回复成功'，got %q", buf.String())
	}
}

// ===================== 2) KF 失败：⚠️ 警告 + 不调 SendTextMessage（关键约束） =====================

func TestSendReply_KfFailure_NoFallback(t *testing.T) {
	// 模拟真实生产日志里的 95001 错误（**非限流**——未认证场景）
	// v4.13.2：限流（95001 send msg count limit）会重试 3 次，
	// 但其他 95001（如 no valid kf account）不重试。
	// 这里用 "95001 no valid kf account" 模拟**非限流**失败路径。
	sender := &fakeReplySender{
		kfErr:   errors.New("发送客服消息失败: 95001 no valid kf account"),
		textErr: nil, // 即使 text 不会成功，也配置成 nil，验证根本不会被调
	}
	buf, restore := captureLog(t)
	defer restore()

	sendReply(context.Background(), sender, "ext-user-2", "open-kf-2", "hi", "shop-2")

	// 关键约束：KF 失败后绝对不能 fallback 到 SendTextMessage
	if sender.textCalls != 0 {
		t.Errorf("KF 失败不应调 SendTextMessage（这是死路），calls = %d", sender.textCalls)
	}
	// 非限流错误：只调 1 次（不重试）
	if sender.kfCalls != 1 {
		t.Errorf("非限流错误应只调 1 次（不重试），got %d", sender.kfCalls)
	}
	if !strings.Contains(buf.String(), "⚠️") {
		t.Errorf("失败应打 ⚠️ 警告，got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "顾客没收到回复") {
		t.Errorf("失败 log 应说明顾客没收到回复，got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "95001") {
		t.Errorf("失败 log 应包含 errcode（便于排查真因），got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "ext-user-2") {
		t.Errorf("失败 log 应包含 to user（便于定位），got %q", buf.String())
	}
}

// ===================== 3) 无 openKfID（admin API 路径）：走 SendTextMessage，行为不变 =====================

func TestSendReply_NoOpenKfID_TextSuccess(t *testing.T) {
	sender := &fakeReplySender{textErr: nil}
	buf, restore := captureLog(t)
	defer restore()

	sendReply(context.Background(), sender, "internal-user", "", "hi", "shop-3")

	if sender.textCalls != 1 {
		t.Errorf("无 openKfID 应走 SendTextMessage，calls = %d", sender.textCalls)
	}
	if sender.kfCalls != 0 {
		t.Errorf("无 openKfID 不应调 SendKfTextMessage，calls = %d", sender.kfCalls)
	}
	if sender.textLastUser != "internal-user" || sender.textLastContent != "hi" {
		t.Errorf("参数不对：user=%q content=%q", sender.textLastUser, sender.textLastContent)
	}
	if !strings.Contains(buf.String(), "发送回复成功") {
		t.Errorf("log 应含'发送回复成功'，got %q", buf.String())
	}
}

func TestSendReply_NoOpenKfID_TextFailure(t *testing.T) {
	sender := &fakeReplySender{textErr: errors.New("some err")}
	buf, restore := captureLog(t)
	defer restore()

	sendReply(context.Background(), sender, "u", "", "hi", "shop-4")

	if !strings.Contains(buf.String(), "发送消息失败") {
		t.Errorf("失败 log 应含'发送消息失败'，got %q", buf.String())
	}
}

// ===================== 4) 防回归：原死路行为绝不能再现 =====================

// TestSendReply_NoDeadFallback_No81013OnKfFailure 强约束：
// 原代码"KF 失败 → fallback SendTextMessage → 81013"的死路绝不能回来。
// mock 中如果 SendTextMessage 被调，测试就失败。
func TestSendReply_NoDeadFallback_No81013OnKfFailure(t *testing.T) {
	// 配置 KF 返回 95001（用户场景），text 返回 81013（如果 fallback 了就会这样）
	sender := &fakeReplySender{
		kfErr:   errors.New("95001 send msg count limit"),
		textErr: errors.New("81013 user & party & tag all invalid"),
	}
	_, restore := captureLog(t)
	defer restore()

	sendReply(context.Background(), sender, "ext", "kf-1", "hi", "shop")

	// 死路指标：textCalls > 0 表示 fallback 又回来了
	if sender.textCalls > 0 {
		t.Fatalf("死路复活！KF 失败时不应 fallback，但 textCalls=%d（这条路径下 81013 必然出现，会掩盖真因 95001）",
			sender.textCalls)
	}
}

// ===================== 6) AGENT_REPLY_MODE=mock 模式（v4.13.1 demo 兜底） =====================
//
// 验证 mock 模式下：
//   - 不调真实 sender（kfCalls / textCalls 都是 0）
//   - 写 event_logs（demo_reply 类型）
//   - 永远返回 nil（假装成功，让上游不报错）

func TestSendReply_MockMode_DoesNotCallRealSender(t *testing.T) {
	// 切换到 mock 模式（测试结束恢复）
	SetReplyMode("mock")
	defer SetReplyMode("real") // 还原默认，避免污染其他测试

	storage.SetupTestDB(t)

	sender := &fakeReplySender{}
	buf, restore := captureLog(t)
	defer restore()

	// mock 模式下：KF 路径应跳过真实 sender
	sendReply(context.Background(), sender, "ext-mock-1", "kf-mock", "demo reply content", "shop-mock")

	// 关键：real sender 一次都没被调
	if sender.kfCalls != 0 {
		t.Errorf("mock 模式不应调 SendKfTextMessage，got kfCalls=%d", sender.kfCalls)
	}
	if sender.textCalls != 0 {
		t.Errorf("mock 模式不应调 SendTextMessage，got textCalls=%d", sender.textCalls)
	}
	// log 应含 demo-reply 标记
	if !strings.Contains(buf.String(), "[demo-reply]") {
		t.Errorf("mock 模式 log 应含 [demo-reply] 标记，got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "demo reply content") {
		t.Errorf("mock 模式 log 应包含 reply 内容，got %q", buf.String())
	}
}

func TestSendReply_MockMode_WritesEventLog(t *testing.T) {
	SetReplyMode("mock")
	defer SetReplyMode("real")

	storage.SetupTestDB(t)

	sender := &fakeReplySender{}
	sendReply(context.Background(), sender, "ext-elog", "kf-elog", "reply for event log test", "shop-elog")

	// 查 event_logs 验证
	var rows []storage.EventLog
	if err := storage.DB.Where("event_type = ?", storage.EventDemoReply).Find(&rows).Error; err != nil {
		t.Fatalf("查 event_logs: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("event_logs 应有 1 条 demo_reply，got %d", len(rows))
	}
	if rows[0].ShopID != "shop-elog" {
		t.Errorf("ShopID 不对：%q", rows[0].ShopID)
	}
}

func TestSendReply_MockMode_RealModeAfterRestore(t *testing.T) {
	// 防回归：测试 SetReplyMode 后是否正确恢复 real 模式
	SetReplyMode("mock")
	SetReplyMode("real") // 立即恢复

	storage.SetupTestDB(t)
	sender := &fakeReplySender{}

	sendReply(context.Background(), sender, "ext", "kf", "real mode reply", "shop")

	if sender.kfCalls != 1 {
		t.Errorf("real 模式应调 SendKfTextMessage，got kfCalls=%d", sender.kfCalls)
	}
}

func TestSendReply_MockMode_OpenKfIDEmptyAlsoMocked(t *testing.T) {
	// mock 模式下，无论 openKfID 是否非空，都走 mock 路径
	SetReplyMode("mock")
	defer SetReplyMode("real")

	storage.SetupTestDB(t)
	sender := &fakeReplySender{}

	// openKfID 空（admin API 路径）也应被 mock
	sendReply(context.Background(), sender, "internal-user", "", "admin path mock reply", "shop")

	if sender.textCalls != 0 {
		t.Errorf("mock 模式（openKfID 空）也不应调 SendTextMessage，got textCalls=%d", sender.textCalls)
	}
}

func TestSetReplyMode_DefaultReal(t *testing.T) {
	// 重置 replyMode 到 "real" 然后验证 IsMockReplyMode 返回 false
	SetReplyMode("real")
	if IsMockReplyMode() {
		t.Error("SetReplyMode(real) 后 IsMockReplyMode 应返回 false")
	}
}

func TestSetReplyMode_MockActivates(t *testing.T) {
	SetReplyMode("mock")
	defer SetReplyMode("real")

	if !IsMockReplyMode() {
		t.Error("SetReplyMode(mock) 后 IsMockReplyMode 应返回 true")
	}
}

func TestSetReplyMode_UnknownValueFallsBackToReal(t *testing.T) {
	// 任何拼写错误 / 未知值都不应触发 mock（生产安全）
	SetReplyMode("MOCK")     // 大小写错
	SetReplyMode("mocky")    // 拼写错
	SetReplyMode("")         // 空
	SetReplyMode("anything") // 任意
	defer SetReplyMode("real")

	if IsMockReplyMode() {
		t.Error("未知值不应触发 mock 模式（生产安全）")
	}
}

// ===================== v4.13.2 限流重试 + 间隔（防 95001 send msg count limit） =====================

// flakyReplySender 模拟"第 N 次调用前都返回限流错误，第 N 次后才成功"
// 用于验证 sendReply 在限流时退避重试，第 N 次成功后不再重试。
type flakyReplySender struct {
	failTimes int        // 前 N 次返回 rateLimitErr
	calls     int        // 总调用次数（线程不安全——sendReply 已用 sendKfRateMu 全局串行 KF 发送，所以无 race）
	rateLimit error
}

func (f *flakyReplySender) SendKfTextMessage(_ context.Context, _, _, _ string) error {
	f.calls++
	if f.calls <= f.failTimes {
		return f.rateLimit
	}
	return nil
}

func (f *flakyReplySender) SendTextMessage(_ context.Context, _, _ string) error {
	return nil
}

// TestSendReply_RateLimit_RetrySuccess 限流：第 1 次失败，第 2 次成功 → 不应再重试
func TestSendReply_RateLimit_RetrySuccess(t *testing.T) {
	sender := &flakyReplySender{
		failTimes: 1,
		rateLimit: errors.New("发送客服消息失败: 95001 send msg count limit"),
	}
	_, restore := captureLog(t)
	defer restore()

	sendReply(context.Background(), sender, "ext-rate-1", "kf-rate-1", "hi", "shop-rate")

	// 预期：第 1 次限流失败 → 退避重试 → 第 2 次成功 → 退出
	if sender.calls != 2 {
		t.Errorf("限流时第 2 次应成功，预期调用 2 次，got %d", sender.calls)
	}
}

// TestSendReply_RateLimit_Retry3TimesAllFail 限流：3 次全失败 → kfCalls=3 + log 含"重试 3 次"
func TestSendReply_RateLimit_Retry3TimesAllFail(t *testing.T) {
	sender := &flakyReplySender{
		failTimes: 99, // 永远限流
		rateLimit: errors.New("发送客服消息失败: 95001 send msg count limit"),
	}
	buf, restore := captureLog(t)
	defer restore()

	sendReply(context.Background(), sender, "ext-rate-fail", "kf-rate-fail", "hi", "shop-rate-fail")

	// 预期：3 次全失败
	if sender.calls != 3 {
		t.Errorf("限流 3 次后应停止，预期 3 次调用，got %d", sender.calls)
	}
	if !strings.Contains(buf.String(), "限流重试 3 次仍失败") {
		t.Errorf("log 应含 '限流重试 3 次仍失败'，got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "⚠️") {
		t.Errorf("最终失败应打 ⚠️ 警告，got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "95001") {
		t.Errorf("log 应保留 errcode 95001 便于排查，got %q", buf.String())
	}
}

// TestSendReply_NonRateLimitError_NoRetry 非限流错误不重试（避免错把未认证当限流）
func TestSendReply_NonRateLimitError_NoRetry(t *testing.T) {
	// 95001 no valid kf account——这是未认证/无接待人员，**不是**限流
	sender := &flakyReplySender{
		failTimes: 99, // 即使配成 99 次都失败
		rateLimit: errors.New("发送客服消息失败: 95001 no valid kf account"),
	}
	_, restore := captureLog(t)
	defer restore()

	sendReply(context.Background(), sender, "ext-noauth", "kf-noauth", "hi", "shop-noauth")

	// 非限流错误：只调 1 次（不重试）
	if sender.calls != 1 {
		t.Errorf("非限流错误不应重试，预期 1 次调用，got %d", sender.calls)
	}
}

// TestSendReply_RateLimit_Interval 验证 sendReply 之间有间隔（防触发限流）
//
// v4.13.2 加：200ms 间隔在 KF 发送路径，3 次连续 sendReply 应 ≥ 600ms。
// 但这测试**耗时**——3 次 * (200ms 间隔 + 假设 mock 立即返回) = ~600ms
// 我们用 500ms 作为下限（留点调度余量），用 3 秒作为上限（防止 sleep 写错变成分钟级）
func TestSendReply_RateLimit_Interval(t *testing.T) {
	sender := &fakeReplySender{kfErr: nil} // 立即成功（不会被退避 sleep 影响断言）

	_, restore := captureLog(t)
	defer restore()

	start := time.Now()
	sendReply(context.Background(), sender, "u1", "kf-1", "hi", "shop-1")
	sendReply(context.Background(), sender, "u2", "kf-2", "hi", "shop-2")
	sendReply(context.Background(), sender, "u3", "kf-3", "hi", "shop-3")
	elapsed := time.Since(start)

	// 3 次 sendReply 串行：每次 200ms 间隔，期望 ≥ 600ms（3*200）
	// 实际可能因调度略有偏差，500ms 是安全下限
	if elapsed < 500*time.Millisecond {
		t.Errorf("3 次 sendReply 间隔应 ≥ 500ms（实际应 ~600ms），got %v", elapsed)
	}
	// 上限：不能太离谱（防止 sleep 写错变成分钟级）
	if elapsed > 3*time.Second {
		t.Errorf("3 次 sendReply 间隔应 ≤ 3s，got %v（可能 sleep 写错了）", elapsed)
	}
}

// TestIsKfRateLimited 单元测试 isKfRateLimited 的判断逻辑
func TestIsKfRateLimited(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"限流 95001 count limit", errors.New("发送客服消息失败: 95001 send msg count limit"), true},
		{"未认证 95001 no valid kf", errors.New("发送客服消息失败: 95001 no valid kf account"), false},
		{"95018 真人接管", errors.New("发送客服消息失败: 95018 kf session is closed"), false},
		{"95002 48h 超时", errors.New("发送客服消息失败: 95002 expire"), false},
		{"网络错误", errors.New("connection reset"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isKfRateLimited(tc.err); got != tc.want {
				t.Errorf("isKfRateLimited(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}