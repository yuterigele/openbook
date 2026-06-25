package tools

// handoff_to_human_test.go
//
// 覆盖 v4.13.5 修复：handoff 工具 per-refID 去重
//  1. 5 分钟内同 refID 调 2 次 → 只写 1 条埋点
//  2. 不同 refID 互不影响
//  3. 5 分钟后同 refID 调 → 算新事件
//  4. external_user_id 优先于 customer 作 refID
//  5. dedup 命中时仍 return 成功（避免 LLM 重试）
//
// Run:
//   go test ./tools/... -v -run "TestHandoffToHuman"

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

// TestHandoffToHuman_DedupSameRefID 5 分钟内同 refID 调 2 次只写 1 条埋点
func TestHandoffToHuman_DedupSameRefID(t *testing.T) {
	storage.SetupTestDB(t)
	handoffDedupReset()
	defer handoffDedupReset()

	ctx := WithExternalUserID(context.Background(), "user-stable-1")

	// 第 1 次调 handoff → 写埋点
	out1, err1 := (&HandoffToHumanTool{}).InvokableRun(ctx,
		`{"reason":"顾客要求找店长","customer":"Alice"}`)
	if err1 != nil {
		t.Fatalf("第 1 次调 handoff 失败: %v", err1)
	}
	if !strings.Contains(out1, "已为顾客") {
		t.Errorf("第 1 次返回应含'已为顾客'，got %q", out1)
	}

	// 第 2 次同 refID 调 handoff（5 分钟内）→ 不写埋点
	out2, err2 := (&HandoffToHumanTool{}).InvokableRun(ctx,
		`{"reason":"顾客又要求找店长","customer":"Alice"}`)
	if err2 != nil {
		t.Fatalf("第 2 次调 handoff 失败: %v", err2)
	}
	// 返回值应明确说"已记录过，不再重复埋点"
	if !strings.Contains(out2, "已记录过") {
		t.Errorf("dedup 命中应 return 提示已记录过，got %q", out2)
	}

	// 验证：event_logs 表里 handoff_to_human 只有 1 条
	var rows []storage.EventLog
	if err := storage.DB.Where("event_type = ?", storage.EventHandoffToHuman).Find(&rows).Error; err != nil {
		t.Fatalf("查 event_logs: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("5 分钟内调 2 次 handoff 应只写 1 条埋点，got %d", len(rows))
	}
}

// TestHandoffToHuman_DedupDifferentRefID 不同 refID 互不影响
func TestHandoffToHuman_DedupDifferentRefID(t *testing.T) {
	storage.SetupTestDB(t)
	handoffDedupReset()
	defer handoffDedupReset()

	// 顾客 A
	ctxA := WithExternalUserID(context.Background(), "user-A")
	_, err := (&HandoffToHumanTool{}).InvokableRun(ctxA, `{"reason":"A 投诉"}`)
	if err != nil {
		t.Fatalf("A 调 handoff 失败: %v", err)
	}

	// 顾客 B（不同 refID）→ 应独立写 1 条
	ctxB := WithExternalUserID(context.Background(), "user-B")
	_, err = (&HandoffToHumanTool{}).InvokableRun(ctxB, `{"reason":"B 投诉"}`)
	if err != nil {
		t.Fatalf("B 调 handoff 失败: %v", err)
	}

	var rows []storage.EventLog
	storage.DB.Where("event_type = ?", storage.EventHandoffToHuman).Find(&rows)
	if len(rows) != 2 {
		t.Errorf("不同 refID 应各写 1 条埋点（共 2 条），got %d", len(rows))
	}
}

// TestHandoffToHuman_RefIDPriority external_user_id 优先于 customer 作 refID
func TestHandoffToHuman_RefIDPriority(t *testing.T) {
	storage.SetupTestDB(t)
	handoffDedupReset()
	defer handoffDedupReset()

	// ctx 里有 external_user_id="wechat-user-001"，customer="Alice"
	// 应该用 external_user_id 作 refID
	ctx := WithExternalUserID(context.Background(), "wechat-user-001")
	_, err := (&HandoffToHumanTool{}).InvokableRun(ctx,
		`{"reason":"投诉","customer":"Alice"}`)
	if err != nil {
		t.Fatalf("handoff 失败: %v", err)
	}

	// 第 2 次：ctx 同样 external_user_id，但 customer 改成 "Bob"
	// 因为 refID 用 external_user_id（不变），所以应该被 dedup
	_, err = (&HandoffToHumanTool{}).InvokableRun(ctx,
		`{"reason":"投诉","customer":"Bob"}`)
	if err != nil {
		t.Fatalf("第 2 次 handoff 失败: %v", err)
	}

	// 验证：只写 1 条埋点，refID 是 external_user_id（不是 customer）
	var rows []storage.EventLog
	storage.DB.Where("event_type = ?", storage.EventHandoffToHuman).Find(&rows)
	if len(rows) != 1 {
		t.Errorf("同 external_user_id 即使 customer 不同也应只写 1 条埋点，got %d", len(rows))
	}
	if rows[0].RefID != "wechat-user-001" {
		t.Errorf("refID 应是 external_user_id（稳定），got %q", rows[0].RefID)
	}
}

// TestHandoffToHuman_StableRefIDForUnknown 旧版用 "unknown-{timestamp}" 作 refID，
// 每次都不同 → dedup 永远不命中。新版用稳定 key "unknown"。
func TestHandoffToHuman_StableRefIDForUnknown(t *testing.T) {
	storage.SetupTestDB(t)
	handoffDedupReset()
	defer handoffDedupReset()

	// ctx 没 external_user_id + customer 也为空 → refID = "unknown"
	ctx := context.Background()
	_, err := (&HandoffToHumanTool{}).InvokableRun(ctx, `{"reason":"顾客没留名"}`)
	if err != nil {
		t.Fatalf("第 1 次 handoff 失败: %v", err)
	}

	// 第 2 次同样场景 → 应该被 dedup（refID 都是 "unknown"）
	_, err = (&HandoffToHumanTool{}).InvokableRun(ctx, `{"reason":"顾客没留名"}`)
	if err != nil {
		t.Fatalf("第 2 次 handoff 失败: %v", err)
	}

	var rows []storage.EventLog
	storage.DB.Where("event_type = ?", storage.EventHandoffToHuman).Find(&rows)
	if len(rows) != 1 {
		t.Errorf("同 'unknown' refID 应只写 1 条埋点（不再每次不同），got %d", len(rows))
	}
	if rows[0].RefID != "unknown" {
		t.Errorf("refID 应是稳定的 'unknown'，got %q", rows[0].RefID)
	}
}

// TestHandoffToHuman_DedupDoesNotResetWindow 命中 dedup 后**不**更新时间戳，
// 避免"无限延后"——5 分钟窗口从首次写入起算
func TestHandoffToHuman_DedupDoesNotResetWindow(t *testing.T) {
	storage.SetupTestDB(t)
	handoffDedupReset()
	defer handoffDedupReset()

	ctx := WithExternalUserID(context.Background(), "user-stable-2")

	// 第 1 次写入
	(&HandoffToHumanTool{}).InvokableRun(ctx, `{"reason":"R1"}`)

	// 手动修改 dedup map 把 lastWriteTime 设为 6 分钟前（模拟窗口已过期）
	handoffDedup.Store("user-stable-2", time.Now().Add(-6*time.Minute))

	// 第 2 次调：6 分钟前写过 → 视为新事件 → 写埋点
	(&HandoffToHumanTool{}).InvokableRun(ctx, `{"reason":"R2"}`)

	var rows []storage.EventLog
	storage.DB.Where("event_type = ?", storage.EventHandoffToHuman).Find(&rows)
	if len(rows) != 2 {
		t.Errorf("窗口外同 refID 应算新事件（写第 2 条埋点），got %d", len(rows))
	}
}
