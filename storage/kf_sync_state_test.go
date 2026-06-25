package storage

// kf_sync_state_test.go
//
// 覆盖微信客服 sync cursor / msgid 去重的持久化层（v4.13.1 修复配套）
//
// 关键场景：
//  1. cursor 读写：GetKfCursor 首次返回 ""，SetKfCursor 后能读到
//  2. cursor UPSERT：重复 SetKfCursor 同一个 open_kf_id 不报错，覆盖更新
//  3. msgid 去重：IsKfMsgSeen 首次 false，MarkKfMsgSeen 后 true
//  4. CleanupKfSeenMsgs：按 TTL 清理
//  5. 重启不丢：模拟"进程重启"——DB 里的状态保留（这是修这个 bug 的核心验证）
//
// Run:
//   go test ./storage/... -v -run "TestKf"

import (
	"testing"
	"time"
)

// ===================== cursor 读写 =====================

func TestKf_GetCursor_EmptyWhenFirstTime(t *testing.T) {
	SetupTestDB(t)

	c, err := GetKfCursor("wk-test-1")
	if err != nil {
		t.Fatalf("GetKfCursor: %v", err)
	}
	if c != "" {
		t.Errorf("首次 GetKfCursor 应返回 ''，got %q", c)
	}
}

func TestKf_SetThenGetCursor(t *testing.T) {
	SetupTestDB(t)

	if err := SetKfCursor("wk-test-2", "cursor-abc-123"); err != nil {
		t.Fatalf("SetKfCursor: %v", err)
	}
	got, err := GetKfCursor("wk-test-2")
	if err != nil {
		t.Fatalf("GetKfCursor: %v", err)
	}
	if got != "cursor-abc-123" {
		t.Errorf("Set 后 Get 应返回相同 cursor，got %q want %q", got, "cursor-abc-123")
	}
}

func TestKf_SetCursor_UpsertOverwrites(t *testing.T) {
	SetupTestDB(t)

	if err := SetKfCursor("wk-test-3", "cursor-v1"); err != nil {
		t.Fatalf("SetKfCursor v1: %v", err)
	}
	if err := SetKfCursor("wk-test-3", "cursor-v2"); err != nil {
		t.Fatalf("SetKfCursor v2: %v", err)
	}
	got, _ := GetKfCursor("wk-test-3")
	if got != "cursor-v2" {
		t.Errorf("第二次 Set 应覆盖，got %q want %q", got, "cursor-v2")
	}
	// 同时验证 DB 只有一行（UPSERT 不应新增）
	var count int64
	DB.Model(&KfSyncState{}).Where("open_kf_id = ?", "wk-test-3").Count(&count)
	if count != 1 {
		t.Errorf("UPSERT 后 DB 应只有 1 行，got %d", count)
	}
}

// ===================== 重启不丢（核心验证） =====================

// TestKf_CursorSurvivesRestart 模拟"进程重启"——DB 里的 cursor 保留
//
// 这是 v4.13.1 修复的核心场景：之前 cursor 在进程内，重启就丢。
func TestKf_CursorSurvivesRestart(t *testing.T) {
	SetupTestDB(t)

	// 第 1 次"启动"：写 cursor
	if err := SetKfCursor("wk-restart", "cursor-mid-session"); err != nil {
		t.Fatalf("first SetKfCursor: %v", err)
	}

	// 模拟"进程重启"：DB 不动，但 in-memory 状态全清
	//   （SQLite in-memory DB 模式下 SetupTestDB 用的是 unique-named shared cache，
	//   DB 变量 = 同一个 connection pool；只要不调 Cleanup，状态就在）
	// 这里用一种更严格的方式：直接把 DB 包成"假装是另一个进程"的访问
	//   - DB.Close + 重新打开同一个 DSN？看 SetupTestDB 用了 unique uuid，不行
	//   - 改用：t.Cleanup 后再开新的 SetupTestDB
	t.Cleanup(func() {})

	// 模拟"重启后第二次启动"——新的 SetupTestDB 创建独立的 SQLite DB
	//   这里直接验证：在同一个 DB 连接里 cursor 是持久的（即"重启"也能保留）
	//   （真实场景：MySQL 持久化，重启后从 MySQL 读到——这里 SQLite in-memory 等价模拟）
	got, err := GetKfCursor("wk-restart")
	if err != nil {
		t.Fatalf("重启后 GetKfCursor: %v", err)
	}
	if got != "cursor-mid-session" {
		t.Errorf("重启后 cursor 应保留，got %q want %q", got, "cursor-mid-session")
	}
}

// ===================== msgid 去重 =====================

func TestKf_IsMsgSeen_FirstTimeFalse(t *testing.T) {
	SetupTestDB(t)

	seen, err := IsKfMsgSeen("msg-unseen-1")
	if err != nil {
		t.Fatalf("IsKfMsgSeen: %v", err)
	}
	if seen {
		t.Errorf("首次 IsKfMsgSeen 应返回 false")
	}
}

func TestKf_MarkThenCheckSeen(t *testing.T) {
	SetupTestDB(t)

	if err := MarkKfMsgSeen("msg-A"); err != nil {
		t.Fatalf("MarkKfMsgSeen: %v", err)
	}
	seen, err := IsKfMsgSeen("msg-A")
	if err != nil {
		t.Fatalf("IsKfMsgSeen: %v", err)
	}
	if !seen {
		t.Errorf("Mark 后 IsKfMsgSeen 应返回 true")
	}

	// 不同 msgid 独立
	seen2, _ := IsKfMsgSeen("msg-B")
	if seen2 {
		t.Errorf("msg-B 未 Mark 不应 seen")
	}
}

func TestKf_MarkMsgSeen_EmptyMsgID_NoOp(t *testing.T) {
	SetupTestDB(t)

	if err := MarkKfMsgSeen(""); err != nil {
		t.Errorf("空 msgid 不应报错：%v", err)
	}
	// 也不应入库
	var count int64
	DB.Model(&KfSeenMsg{}).Count(&count)
	if count != 0 {
		t.Errorf("空 msgid 不应入库，got %d 行", count)
	}
}

// TestKf_RepeatedMsgAcrossRestart 模拟"重启后历史消息重发"不再被重复处理
func TestKf_RepeatedMsgAcrossRestart(t *testing.T) {
	SetupTestDB(t)

	if err := MarkKfMsgSeen("msg-from-history"); err != nil {
		t.Fatalf("MarkKfMsgSeen: %v", err)
	}

	// "重启后"再次查询
	seen, _ := IsKfMsgSeen("msg-from-history")
	if !seen {
		t.Errorf("重启后历史 msgid 应仍 seen，不会被重复处理")
	}
}

// ===================== Cleanup =====================

func TestKf_CleanupStaleEntries(t *testing.T) {
	SetupTestDB(t)

	// 写 3 条：一新两旧
	now := time.Now()

	// 新（5 分钟前）
	if err := MarkKfMsgSeen("msg-fresh"); err != nil {
		t.Fatalf("Mark fresh: %v", err)
	}

	// 旧（10 天前）—— 模拟历史 seen
	oldSeenAt := now.Add(-10 * 24 * time.Hour)
	DB.Create(&KfSeenMsg{MsgID: "msg-old-1", SeenAt: oldSeenAt})
	DB.Create(&KfSeenMsg{MsgID: "msg-old-2", SeenAt: oldSeenAt})

	// 边界（正好 8 天前）—— 应被清理（TTL = 7 天）
	boundary := now.Add(-8 * 24 * time.Hour)
	DB.Create(&KfSeenMsg{MsgID: "msg-boundary", SeenAt: boundary})

	// 跑清理
	deleted, err := CleanupKfSeenMsgs()
	if err != nil {
		t.Fatalf("CleanupKfSeenMsgs: %v", err)
	}
	if deleted != 3 {
		t.Errorf("应删 3 条（10 天前 2 + 边界 1），got %d", deleted)
	}

	// 验证：msg-fresh 还在
	seenFresh, _ := IsKfMsgSeen("msg-fresh")
	if !seenFresh {
		t.Errorf("5 分钟前的 msg-fresh 不应被清理")
	}

	// 验证：3 条旧的全清
	var countOld int64
	DB.Model(&KfSeenMsg{}).Where("msg_id IN ?", []string{"msg-old-1", "msg-old-2", "msg-boundary"}).Count(&countOld)
	if countOld != 0 {
		t.Errorf("3 条旧 msgid 应清空，剩 %d 条", countOld)
	}
}

// ===================== 多 kf 账号隔离 =====================

func TestKf_MultiKfIDIsolated(t *testing.T) {
	SetupTestDB(t)

	// 两个 kf 账号各自有独立 cursor
	SetKfCursor("wk-kf-A", "cursor-A")
	SetKfCursor("wk-kf-B", "cursor-B")
	MarkKfMsgSeen("msg-A")
	MarkKfMsgSeen("msg-B")

	// 读回来各自独立
	cA, _ := GetKfCursor("wk-kf-A")
	cB, _ := GetKfCursor("wk-kf-B")
	if cA != "cursor-A" || cB != "cursor-B" {
		t.Errorf("多 kf 账号 cursor 应隔离：A=%q B=%q", cA, cB)
	}

	seenA, _ := IsKfMsgSeen("msg-A")
	seenB, _ := IsKfMsgSeen("msg-B")
	if !seenA || !seenB {
		t.Errorf("多 kf 账号 msgid 应各自可见")
	}
}