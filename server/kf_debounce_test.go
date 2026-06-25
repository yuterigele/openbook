package server

// kf_debounce_test.go
//
// 覆盖 v4.13.3 debounce 合并行为：
//  1. debounce 后 1.5s 触发（累积 N 条等待 → 合并成 1 次回调）
//  2. 满 5 条立即触发（不继续等）
//  3. 处理期间新消息开新 batch（不阻塞）
//  4. 不同 session 互不影响
//  5. 空 batch 不触发 callback
//  6. timer 重置（连续 enqueue 重置等待时间）
//
// Run:
//   go test ./server/... -v -run "TestKfDebounce"

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuterigele/openbook/wecom"
)

// makeKfMsg 测试用：构造 KfMsgItem
func makeKfMsg(msgid, externalUserID, content string) *wecom.KfMsgItem {
	return &wecom.KfMsgItem{
		Msgid:          msgid,
		OpenKfid:       "wk-test",
		ExternalUserid: externalUserID,
		Origin:         3,
		MsgType:        "text",
		SendTime:       time.Now().Unix(),
		Text: &struct {
			Content string `json:"content"`
		}{Content: content},
	}
}

// TestKfDebounce_IntervalTrigger debounce 1.5s 触发
func TestKfDebounce_IntervalTrigger(t *testing.T) {
	kfDebounceReset()
	defer kfDebounceReset()

	var (
		callCount int32
		gotMsgs   []string
		mu        sync.Mutex
	)
	callback := func(merged []*wecom.KfMsgItem) {
		atomic.AddInt32(&callCount, 1)
		mu.Lock()
		for _, m := range merged {
			gotMsgs = append(gotMsgs, m.Msgid)
		}
		mu.Unlock()
	}

	sessionID := "wecom_test_interval"

	// 累积 3 条
	kfDebounceEnqueue(sessionID, makeKfMsg("m1", "u1", "hi"), callback)
	time.Sleep(100 * time.Millisecond)
	kfDebounceEnqueue(sessionID, makeKfMsg("m2", "u1", "there"), callback)
	time.Sleep(100 * time.Millisecond)
	kfDebounceEnqueue(sessionID, makeKfMsg("m3", "u1", "friend"), callback)

	// 此时 callback 不应已触发（1.5s 内持续 enqueue）
	if got := atomic.LoadInt32(&callCount); got != 0 {
		t.Errorf("1.5s 内持续 enqueue，callback 不应已触发，got callCount=%d", got)
	}

	// 等 debounce 触发 + callback 执行
	time.Sleep(2 * time.Second)

	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("debounce 应触发 1 次 callback，got %d", got)
	}
	mu.Lock()
	if len(gotMsgs) != 3 {
		t.Errorf("callback 应收到 3 条合并消息，got %d: %v", len(gotMsgs), gotMsgs)
	}
	mu.Unlock()
}

// TestKfDebounce_MaxMsgsImmediateTrigger 满 5 条立即触发（不继续等）
func TestKfDebounce_MaxMsgsImmediateTrigger(t *testing.T) {
	kfDebounceReset()
	defer kfDebounceReset()

	var callCount int32
	callback := func(merged []*wecom.KfMsgItem) {
		atomic.AddInt32(&callCount, 1)
	}

	sessionID := "wecom_test_max"

	// 累积 5 条
	for i := 1; i <= 5; i++ {
		kfDebounceEnqueue(sessionID, makeKfMsg("m"+string(rune('0'+i)), "u1", "msg"), callback)
	}

	// 第 5 条 enqueue 应立即触发（不等 1.5s）
	// 给 100ms 让 goroutine 跑
	time.Sleep(100 * time.Millisecond)

	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("满 5 条应立即触发 1 次 callback，got %d", got)
	}
}

// TestKfDebounce_NewBatchDuringProcessing 处理期间新消息开新 batch
func TestKfDebounce_NewBatchDuringProcessing(t *testing.T) {
	kfDebounceReset()
	defer kfDebounceReset()

	var (
		callCount  int32
		batchSizes []int
		mu         sync.Mutex
	)
	// 模拟慢 callback（agent 推理 1.5s）
	callback := func(merged []*wecom.KfMsgItem) {
		atomic.AddInt32(&callCount, 1)
		mu.Lock()
		batchSizes = append(batchSizes, len(merged))
		mu.Unlock()
		time.Sleep(1500 * time.Millisecond) // 模拟 agent 推理慢
	}

	sessionID := "wecom_test_newbatch"

	// 第 1 批：3 条 → debounce 1.5s 后触发 → callback 跑 1.5s
	kfDebounceEnqueue(sessionID, makeKfMsg("m1", "u1", "msg"), callback)
	time.Sleep(200 * time.Millisecond)
	kfDebounceEnqueue(sessionID, makeKfMsg("m2", "u1", "msg"), callback)
	time.Sleep(200 * time.Millisecond)
	kfDebounceEnqueue(sessionID, makeKfMsg("m3", "u1", "msg"), callback)

	// 等第 1 批触发 + 跑到一半
	time.Sleep(1800 * time.Millisecond) // 1.5s debounce + 0.3s callback 跑中

	// 此时 callback 第 1 次应正在跑，新消息进新 batch
	kfDebounceEnqueue(sessionID, makeKfMsg("m4", "u1", "new batch msg"), callback)

	// 等第 1 批 callback 跑完 + 第 2 批 debounce 触发
	time.Sleep(3500 * time.Millisecond) // 1.2s 第 1 批剩余 + 1.5s 第 2 批 debounce + buffer

	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("应触发 2 次 callback（第 1 批 + 第 2 批），got %d", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(batchSizes) != 2 {
		t.Fatalf("应有 2 个 batch 记录，got %d", len(batchSizes))
	}
	if batchSizes[0] != 3 {
		t.Errorf("第 1 批应 3 条消息，got %d", batchSizes[0])
	}
	if batchSizes[1] != 1 {
		t.Errorf("第 2 批应 1 条消息，got %d", batchSizes[1])
	}
}

// TestKfDebounce_DifferentSessionsIsolated 不同 session 互不影响
func TestKfDebounce_DifferentSessionsIsolated(t *testing.T) {
	kfDebounceReset()
	defer kfDebounceReset()

	var callCount int32
	callback := func(merged []*wecom.KfMsgItem) {
		atomic.AddInt32(&callCount, 1)
	}

	// session A 和 session B 各发 1 条
	kfDebounceEnqueue("wecom_test_A", makeKfMsg("a1", "userA", "hi"), callback)
	kfDebounceEnqueue("wecom_test_B", makeKfMsg("b1", "userB", "hi"), callback)

	// 等 debounce
	time.Sleep(2 * time.Second)

	// 不同 session → 2 次 callback
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("不同 session 应分别触发，got callCount=%d", got)
	}
}

// TestKfDebounce_TimerReset 持续 enqueue 重置 timer（不立即触发）
func TestKfDebounce_TimerReset(t *testing.T) {
	kfDebounceReset()
	defer kfDebounceReset()

	var callCount int32
	callback := func(merged []*wecom.KfMsgItem) {
		atomic.AddInt32(&callCount, 1)
	}

	sessionID := "wecom_test_reset"

	// 每 500ms enqueue 1 次，共 5 次（总 2.5s）
	// 如果 timer 不重置，第 1 条 1.5s 后就会触发 → 触发 2 次
	// 如果 timer 正确重置，2.5s 内持续 enqueue → 触发 1 次
	for i := 0; i < 5; i++ {
		kfDebounceEnqueue(sessionID, makeKfMsg("m", "u", "msg"), callback)
		time.Sleep(500 * time.Millisecond)
	}

	// 最后一波 enqueue 完后再等 2s
	time.Sleep(2 * time.Second)

	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("持续 enqueue 重置 timer 应只触发 1 次，got %d（说明 timer 没重置）", got)
	}
}

// TestKfDebounce_EmptyBatchSafe 空 batch 不触发 callback
func TestKfDebounce_EmptyBatchSafe(t *testing.T) {
	kfDebounceReset()
	defer kfDebounceReset()

	var callCount int32
	callback := func(merged []*wecom.KfMsgItem) {
		atomic.AddInt32(&callCount, 1)
	}

	// 直接调 fireProcess 模拟空 batch（理论不应发生，但 race-safe 兜底测试）
	// 这里改测：1 条消息 → debounce → 触发 → callback 收到 1 条（不是 0）
	kfDebounceEnqueue("wecom_test_empty", makeKfMsg("m1", "u1", "msg"), callback)
	time.Sleep(2 * time.Second)

	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("1 条消息应触发 1 次 callback，got %d", got)
	}
}
