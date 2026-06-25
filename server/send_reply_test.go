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
	// 模拟真实生产日志里的 95001 错误
	sender := &fakeReplySender{
		kfErr:   errors.New("发送客服消息失败: 95001 send msg count limit"),
		textErr: nil, // 即使 text 不会成功，也配置成 nil，验证根本不会被调
	}
	buf, restore := captureLog(t)
	defer restore()

	sendReply(context.Background(), sender, "ext-user-2", "open-kf-2", "hi", "shop-2")

	// 关键约束：KF 失败后绝对不能 fallback 到 SendTextMessage
	if sender.textCalls != 0 {
		t.Errorf("KF 失败不应调 SendTextMessage（这是死路），calls = %d", sender.textCalls)
	}
	if sender.kfCalls != 1 {
		t.Errorf("SendKfTextMessage 应只调一次, got %d", sender.kfCalls)
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