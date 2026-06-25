package server

// kf_debounce.go
//
// 把同一 session 的多条 KF 消息合并成 1 次 agent 推理 + 1 条回复。
//
// v4.13.3 修复：用户连发 N 条消息时（原行为 N 次 agent 推理 + N 条 sendReply）
//   - 触发企微客服消息发送频率限制（95001 send msg count limit，5 条/秒）
//   - 用户感知："我发 1 条，agent 回 5-10 条，太啰嗦"
//   - 真人客服连发也是多次回，但 agent 显得机械且浪费 token
//
// 解决：per-session debounce 队列
//   - 同 session 的多条消息累积到 batch
//   - 1.5 秒内无新消息 → 触发 batch 处理（1 次 agent 推理 + 1 条回复）
//   - 累积 ≥ 5 条 → 立即处理（不继续等，避免延迟）
//   - batch 处理期间新消息开新 batch（不阻塞新消息接收）
//
// 调试点：
//   - 1.5 秒间隔不能太长（用户感知延迟），不能太短（合并率低）
//   - 5 条阈值是经验值：用户正常连发 2-3 条，5 条时已属异常场景
//   - batch 处理 callback 同步执行（agent 推理 1-3 秒），期间新消息开新 batch
//
// 不影响：
//   - msgid seen 去重（仍然在 handleKfCallback 主循环立即标 seen，防重拉）
//   - cursor 持久化（仍然在主循环立即写）
//   - sendReply 限流重试（v4.13.2 修复，与本 debounce 解耦）

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yuterigele/openbook/wecom"
)

// 触发策略
const (
	kfDebounceInterval = 1500 * time.Millisecond // 1.5 秒无新消息 → 触发
	kfDebounceMaxMsgs  = 5                       // 累积 5 条 → 立即触发
)

// kfDebounceState 单个 session 的累积状态
//
// 状态机：
//   - idle:  msgs=nil, processing=false
//   - accumulating: msgs=[m1,m2,...], timer=active, processing=false
//   - processing: msgs=nil, processing=true（callback 在跑）
//   - transitioning: accumulating→processing 临界，timer 触发但还没拿锁
//
// 并发约束：
//   - state.mu 保护所有字段读写
//   - processing=true 时新消息**开新 state**（kfDebounceMap.Store 新指针）
//   - callback 跑完检查 state 还在不在（防止误清理新 batch）
type kfDebounceState struct {
	mu         sync.Mutex
	batchID    int64             // 自增 ID，callback 跑完用 batchID 验证状态
	msgs       []*wecom.KfMsgItem // 累积待处理消息
	timer      *time.Timer       // debounce timer
	enqueuedAt time.Time         // 首条消息入队时间（用于调试日志）
	processing bool              // callback 在跑，新消息开新 batch
}

var (
	kfDebounceMap    sync.Map // sessionID -> *kfDebounceState
	kfDebounceNextID int64    // atomic 自增，分配 batchID
)

// kfDebounceEnqueue 把 KF 消息加入对应 session 的 debounce 队列
//
// 行为：
//   - 累积到 5 条 → 立即触发
//   - 否则 1.5 秒无新消息 → 触发
//   - 触发时 callback(merged) 同步执行
//   - callback 跑期间新消息开新 state，新 batch
//
// 线程安全：可并发调用（不同 session / 同 session 多次）
func kfDebounceEnqueue(sessionID string, msg *wecom.KfMsgItem, callback func(merged []*wecom.KfMsgItem)) {
	stateI, _ := kfDebounceMap.LoadOrStore(sessionID, &kfDebounceState{})
	state := stateI.(*kfDebounceState)

	state.mu.Lock()

	// 状态 1：当前 batch 在跑，开新 state（新 batch）
	if state.processing {
		newState := &kfDebounceState{
			batchID:    atomic.AddInt64(&kfDebounceNextID, 1),
			msgs:       []*wecom.KfMsgItem{msg},
			enqueuedAt: time.Now(),
		}
		kfDebounceMap.Store(sessionID, newState)
		state.mu.Unlock()
		kfScheduleProcess(sessionID, newState, callback)
		return
	}

	// 状态 2：追加到当前 batch
	if state.batchID == 0 {
		state.batchID = atomic.AddInt64(&kfDebounceNextID, 1)
	}
	state.msgs = append(state.msgs, msg)
	if state.enqueuedAt.IsZero() {
		state.enqueuedAt = time.Now()
	}
	state.mu.Unlock()

	kfScheduleProcess(sessionID, state, callback)
}

// kfScheduleProcess 安排 batch 处理（立即 / 1.5s 后）
func kfScheduleProcess(sessionID string, state *kfDebounceState, callback func(merged []*wecom.KfMsgItem)) {
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.processing {
		return // 已被其他 goroutine 抢着处理（race-safe）
	}

	// 满 5 条 → 立即处理
	if len(state.msgs) >= kfDebounceMaxMsgs {
		go kfFireProcess(sessionID, state, callback)
		return
	}

	// 否则 1.5s 后处理（重置 timer）
	if state.timer != nil {
		state.timer.Stop()
	}
	state.timer = time.AfterFunc(kfDebounceInterval, func() {
		kfFireProcess(sessionID, state, callback)
	})
}

// kfFireProcess 触发 batch 处理（调 callback）
//
// 必须从 goroutine 调用（避免阻塞 kfDebounceEnqueue 主流程）。
// 串行锁：state.mu
func kfFireProcess(sessionID string, state *kfDebounceState, callback func(merged []*wecom.KfMsgItem)) {
	state.mu.Lock()
	if state.processing {
		state.mu.Unlock()
		return // 已被其他 goroutine 处理
	}
	state.processing = true
	msgs := state.msgs
	batchID := state.batchID
	state.msgs = nil
	state.timer = nil
	state.mu.Unlock()

	if len(msgs) == 0 {
		// 空 batch（理论不应发生，但 race-safe 兜底）
		state.mu.Lock()
		state.processing = false
		state.mu.Unlock()
		return
	}

	waited := time.Since(state.enqueuedAt)
	log.Printf("[kf-debounce] 合并 %d 条消息处理 session=%s batchID=%d waited=%v",
		len(msgs), sessionID, batchID, waited.Round(10*time.Millisecond))

	// 同步调 callback（agent 推理 1-3 秒，期间 session 阻塞）
	callback(msgs)

	// 处理完：释放 processing 标志
	// 注意：kfDebounceEnqueue 可能在 processing 期间 Store 了 newState 替换了 map 里的指针，
	// 所以这里**只**清自己的 state，不动 map
	state.mu.Lock()
	state.processing = false
	state.batchID = 0
	state.enqueuedAt = time.Time{}
	state.mu.Unlock()
}

// kfDebounceReset 测试用：清空 debounce 状态（防止测试间状态污染）
func kfDebounceReset() {
	kfDebounceMap.Range(func(key, _ any) bool {
		kfDebounceMap.Delete(key)
		return true
	})
}
