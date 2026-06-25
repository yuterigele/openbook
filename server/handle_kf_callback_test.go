package server

// handle_kf_callback_test.go
//
// 覆盖 v4.13.1 handleKfCallback 修复 + filterKfMsgsByWindow helper：
//  1. filterKfMsgsByWindow：纯函数，覆盖 origin/msgtype/窗口三条过滤规则
//  2. handleKfCallback：cursor 持久化（重启不丢）+ 首次拉取按窗口过滤 + msgid 去重
//
// 重点：用 fakeFetcher mock syncMsgFetcher interface，避免真打 HTTP。
// handleWeComMessageWithOpenKfID 内部跑 goroutine，要等它跑完才能断言。
//
// Run:
//   go test ./server/... -v -run "TestFilterKf|TestHandleKfCallback"

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/mem"
	"github.com/yuterigele/openbook/storage"
	"github.com/yuterigele/openbook/wecom"
)

// newTestSrvWithAgent 构造带 mock agent 的 Server（避免 processAgentMessage nil panic）
func newTestSrvWithAgent(t *testing.T) *Server[*schema.Message] {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := mem.NewStore[*schema.Message](tmpDir)
	if err != nil {
		t.Fatalf("mem.NewStore: %v", err)
	}
	return New(Config[*schema.Message]{
		Agent:        simpleReplyAgent("mock agent reply"),
		Store:        store,
		WorkspaceDir: t.TempDir(),
		ProjectRoot:  t.TempDir(),
		ExamplesDir:  t.TempDir(),
	})
}

// fakeFetcher mock syncMsgFetcher：记录 SyncMsg 调用 + 返回配置的 SyncKfMsgResult
//
// 也实现 replySender 接口（syncMsgFetcher 内嵌 replySender），让 handleKfCallback
// 拉消息后调 handleWeComMessageWithOpenKfID 走同一对象。send 链路用 channel 记录。
type fakeFetcher struct {
	mu sync.Mutex

	// SyncMsg 配置：每次按调用顺序返回一个 Result
	results []*wecom.SyncKfMsgResult
	errs    []error

	// SyncMsg 调用记录
	syncCalls int32
	lastCursor string

	// SendKfTextMessage 配置 + 调用记录
	kfErr   error
	kfCalls int32

	// SendTextMessage 配置 + 调用记录
	textErr   error
	textCalls int32
}

func (f *fakeFetcher) SyncMsg(_ context.Context, cursor, _ string, _ int) (*wecom.SyncKfMsgResult, error) {
	atomic.AddInt32(&f.syncCalls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCursor = cursor
	idx := int(atomic.LoadInt32(&f.syncCalls)) - 1
	if idx >= len(f.results) {
		// 默认返回空
		return &wecom.SyncKfMsgResult{MsgList: nil}, nil
	}
	if idx < len(f.errs) && f.errs[idx] != nil {
		return nil, f.errs[idx]
	}
	return f.results[idx], nil
}

func (f *fakeFetcher) SendKfTextMessage(_ context.Context, _, _, _ string) error {
	atomic.AddInt32(&f.kfCalls, 1)
	return f.kfErr
}

func (f *fakeFetcher) SendTextMessage(_ context.Context, _, _ string) error {
	atomic.AddInt32(&f.textCalls, 1)
	return f.textErr
}

// waitForCalls 等异步 goroutine 跑完——handleKfCallback 里 handleWeComMessage 是 go func()
func waitForCalls(t *testing.T, fetcher *fakeFetcher, expected int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&fetcher.kfCalls) >= expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for kfCalls=%d (got %d)", expected, atomic.LoadInt32(&fetcher.kfCalls))
}

// ===================== filterKfMsgsByWindow 纯函数测试 =====================

func TestFilterKfMsgsByWindow_KeepsRecentCustomerText(t *testing.T) {
	now := time.Unix(1_700_000_000, 0) // 固定时间便于断言
	mkMsg := func(origin int, msgType string, sendTime int64, content string) wecom.KfMsgItem {
		m := wecom.KfMsgItem{
			Origin: origin, MsgType: msgType, SendTime: sendTime,
		}
		if content != "" {
			m.Text = &struct {
				Content string `json:"content"`
			}{content}
		}
		return m
	}

	in := []wecom.KfMsgItem{
		mkMsg(3, "text", now.Unix()-3600, "1h ago text"),          // ✅ 客户 + text + 48h 内
		mkMsg(3, "text", now.Unix()-47*3600, "47h ago text"),       // ✅ 边界内
		mkMsg(3, "image", now.Unix()-3600, "1h ago image"),         // ❌ 非 text
		mkMsg(4, "text", now.Unix()-3600, "agent text"),            // ❌ origin=4（客服发的）
		mkMsg(3, "text", now.Unix()-49*3600, "49h ago text"),       // ❌ 超过 48h
		mkMsg(3, "text", now.Unix()-3600, ""),                       // ❌ text 为空
	}

	kept, skipped := filterKfMsgsByWindow(in, now)
	if len(kept) != 2 {
		t.Errorf("应保留 2 条（1h ago + 47h ago），got %d: %+v", len(kept), kept)
	}
	if skipped != 4 {
		t.Errorf("应跳过 4 条（image / origin4 / 49h / 空 text），got %d", skipped)
	}
}

// TestFilterKfMsgsByWindow_AllKept 原 bug 场景：用户连发 3 条，全在窗口内，全保留
//
// 原代码"前 N-1 条跳过"会让第 1、2 条被丢，只处理第 3 条 → 用户发 3 条只收到 1 条回复
func TestFilterKfMsgsByWindow_AllKept_UserSendsThree(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mkMsg := func(content string, secAgo int64) wecom.KfMsgItem {
		return wecom.KfMsgItem{
			Origin: 3, MsgType: "text", SendTime: now.Unix() - secAgo,
			Text: &struct {
				Content string `json:"content"`
			}{content},
		}
	}

	in := []wecom.KfMsgItem{
		mkMsg("msg1", 60),
		mkMsg("msg2", 30),
		mkMsg("msg3", 10),
	}

	kept, _ := filterKfMsgsByWindow(in, now)
	if len(kept) != 3 {
		t.Fatalf("用户连发 3 条，窗口过滤后应全保留（3 条），got %d", len(kept))
	}
	if kept[0].Text.Content != "msg1" || kept[1].Text.Content != "msg2" || kept[2].Text.Content != "msg3" {
		t.Errorf("顺序应保留：%q %q %q", kept[0].Text.Content, kept[1].Text.Content, kept[2].Text.Content)
	}
}

// ===================== handleKfCallback 集成测试 =====================

// TestHandleKfCallback_FirstPull_WritesCursorAndSeen 首次拉取：
//   - cursor="" → 拉消息
//   - 写 cursor 到 DB（关键：进程重启后能续上）
//   - 标 msgid seen（重启后不重复处理）
func TestHandleKfCallback_FirstPull_WritesCursorAndSeen(t *testing.T) {
	storage.SetupTestDB(t)

	now := time.Now()
	msgList := []wecom.KfMsgItem{
		mkTestMsg("msg-first-1", "ext-user-1", now.Unix()),
	}
	fetcher := &fakeFetcher{
		results: []*wecom.SyncKfMsgResult{
			{
				NextCursor: "cursor-v1",
				HasMore:    0,
				MsgList:    msgList,
			},
		},
	}

	srv := newTestSrvWithAgent(t)

	callback := &wecom.MessageXML{
		OpenKfId: "wk-test-first",
		Token:    "tok",
	}
	srv.handleKfCallback(context.Background(), fetcher, callback, "shop-1")

	// 等异步 goroutine 跑完 sendReply
	waitForCalls(t, fetcher, 1, 3*time.Second)

	// 1) cursor 已持久化到 DB
	cursor, err := storage.GetKfCursor("wk-test-first")
	if err != nil {
		t.Fatalf("GetKfCursor: %v", err)
	}
	if cursor != "cursor-v1" {
		t.Errorf("首次拉取后 cursor 应持久化到 DB，got %q want %q", cursor, "cursor-v1")
	}

	// 2) msgid 已标 seen
	seen, _ := storage.IsKfMsgSeen("msg-first-1")
	if !seen {
		t.Errorf("处理过的 msgid 应标 seen，got false")
	}

	// 3) sendReply 链路跑通
	if atomic.LoadInt32(&fetcher.kfCalls) != 1 {
		t.Errorf("SendKfTextMessage 应被调 1 次，got %d", atomic.LoadInt32(&fetcher.kfCalls))
	}
}

// TestHandleKfCallback_CursorSurvivesRestart 核心场景：模拟进程重启后 cursor 还在
//
// 流程：
//   1) 第一次"启动"：cursor="" 拉消息，写 cursor="v1"
//   2) 模拟"进程重启"——DB 状态保留（SQLite 等价于 MySQL 持久化）
//   3) 第二次"启动"：cursor 应该是 "v1" 而不是 ""，sync_msg 用 v1 续拉（不会重拉历史）
func TestHandleKfCallback_CursorSurvivesRestart(t *testing.T) {
	storage.SetupTestDB(t)

	now := time.Now()
	firstMsgs := []wecom.KfMsgItem{
		mkTestMsg("msg-session-1", "ext-user-1", now.Unix()),
	}
	secondMsgs := []wecom.KfMsgItem{
		mkTestMsg("msg-session-2", "ext-user-1", now.Unix()),
	}

	srv := newTestSrvWithAgent(t)
	callback := &wecom.MessageXML{
		OpenKfId: "wk-test-restart",
		Token:    "tok",
	}

	// === 第 1 次启动：cursor="" 拉消息 ===
	fetcher1 := &fakeFetcher{
		results: []*wecom.SyncKfMsgResult{
			{
				NextCursor: "cursor-after-1",
				HasMore:    0,
				MsgList:    firstMsgs,
			},
		},
	}
	srv.handleKfCallback(context.Background(), fetcher1, callback, "shop-1")
	waitForCalls(t, fetcher1, 1, 3*time.Second)

	// 验证 cursor 已写
	c1, _ := storage.GetKfCursor("wk-test-restart")
	if c1 != "cursor-after-1" {
		t.Fatalf("第 1 次拉取后 cursor 应是 %q, got %q", "cursor-after-1", c1)
	}
	// 验证 sync_msg 用了空 cursor（首次）
	if fetcher1.lastCursor != "" {
		t.Errorf("第 1 次 sync_msg cursor 应是 ''（首次），got %q", fetcher1.lastCursor)
	}

	// === 第 2 次启动：cursor 应是 "cursor-after-1"（关键：进程重启不丢） ===
	fetcher2 := &fakeFetcher{
		results: []*wecom.SyncKfMsgResult{
			{
				NextCursor: "cursor-after-2",
				HasMore:    0,
				MsgList:    secondMsgs,
			},
		},
	}
	srv.handleKfCallback(context.Background(), fetcher2, callback, "shop-1")
	waitForCalls(t, fetcher2, 1, 3*time.Second)

	// 关键断言：第 2 次 sync_msg 应该用持久化的 cursor，不是空
	if fetcher2.lastCursor != "cursor-after-1" {
		t.Errorf("第 2 次启动应从 DB 恢复 cursor（=cursor-after-1），但 sync_msg 用了 %q（如果空，说明 cursor 重启丢失了——这就是 v4.13.1 修的 bug）",
			fetcher2.lastCursor)
	}

	// msgid 应不重复处理
	c2, _ := storage.GetKfCursor("wk-test-restart")
	if c2 != "cursor-after-2" {
		t.Errorf("第 2 次拉取后 cursor 应更新，got %q", c2)
	}
}

// TestHandleKfCallback_DedupAcrossRestarts 重启后历史 msgid 不被重复处理
func TestHandleKfCallback_DedupAcrossRestarts(t *testing.T) {
	storage.SetupTestDB(t)

	now := time.Now()
	// 第 1 次拉取的消息
	msgA := mkTestMsg("msg-history-A", "ext-user-A", now.Unix())

	// 第 2 次拉取：msgA 还在（没去重干净？模拟 cursor 丢失重启场景）
	//   但我们这次特意保留 seen 表（这是 v4.13.1 修的事）
	msgList2 := []wecom.KfMsgItem{msgA}

	srv := newTestSrvWithAgent(t)
	callback := &wecom.MessageXML{
		OpenKfId: "wk-test-dedup",
		Token:    "tok",
	}

	// 第 1 次：处理 msgA
	fetcher1 := &fakeFetcher{
		results: []*wecom.SyncKfMsgResult{
			{NextCursor: "c1", MsgList: []wecom.KfMsgItem{msgA}},
		},
	}
	srv.handleKfCallback(context.Background(), fetcher1, callback, "shop-1")
	waitForCalls(t, fetcher1, 1, 3*time.Second)

	// 第 2 次：同 msgA 出现在结果里——应被 seen 去重，不重复处理
	fetcher2 := &fakeFetcher{
		results: []*wecom.SyncKfMsgResult{
			{NextCursor: "c2", MsgList: msgList2},
		},
	}
	srv.handleKfCallback(context.Background(), fetcher2, callback, "shop-1")

	// 等异步 goroutine 处理完
	time.Sleep(500 * time.Millisecond)

	// 关键断言：第 2 次 SendKfTextMessage 没被调（seen 去重生效）
	if atomic.LoadInt32(&fetcher2.kfCalls) != 0 {
		t.Errorf("msgA 已 seen 应跳过，但 kfCalls=%d（说明重启后历史消息被重复处理——这就是 v4.13.1 修的 bug）",
			atomic.LoadInt32(&fetcher2.kfCalls))
	}
}

// TestHandleKfCallback_PanicIsolation 一条消息 panic 不影响整体（cursor 仍写回）
func TestHandleKfCallback_PanicIsolation(t *testing.T) {
	storage.SetupTestDB(t)

	now := time.Now()
	msgList := []wecom.KfMsgItem{
		mkTestMsg("msg-panic-test", "ext-user", now.Unix()),
	}
	fetcher := &fakeFetcher{
		results: []*wecom.SyncKfMsgResult{
			{NextCursor: "c-panic", MsgList: msgList},
		},
	}

	srv := newTestSrvWithAgent(t)
	callback := &wecom.MessageXML{OpenKfId: "wk-panic", Token: "tok"}

	srv.handleKfCallback(context.Background(), fetcher, callback, "shop-1")
	time.Sleep(500 * time.Millisecond) // 等内部 goroutine

	// 关键断言：handleKfCallback 主流程先写 cursor 再 go goroutine 处理消息
	// 即使后续消息处理出问题，cursor 已持久化（防止下次 sync_msg 重拉）
	c, _ := storage.GetKfCursor("wk-panic")
	if c != "c-panic" {
		t.Errorf("cursor 应在主流程写回（即使后续 panic），got %q", c)
	}
}

// ===================== helpers =====================

// mkTestMsg 构造测试用的 KfMsgItem（origin=3 text）
func mkTestMsg(msgID, externalUserID string, sendTime int64) wecom.KfMsgItem {
	return wecom.KfMsgItem{
		Msgid:          msgID,
		OpenKfid:       "wk-test",
		ExternalUserid: externalUserID,
		Origin:         3,
		MsgType:        "text",
		SendTime:       sendTime,
		Text: &struct {
			Content string `json:"content"`
		}{Content: "hello"},
	}
}

// testMsg = *schema.Message（Server 是 generic [M adk.MessageType]，M 是指针类型）
type testMsg = *schema.Message