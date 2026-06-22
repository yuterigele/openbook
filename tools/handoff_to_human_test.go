package tools

// handoff_to_human_test.go
//
// Tests for HandoffToHumanTool:
//   - TestHandoffToHumanTool_BasicSuccess: writes event row, returns success string
//   - TestHandoffToHumanTool_EmptyReason_Errors: rejects missing reason
//   - TestHandoffToHumanTool_NoShopID_Fallback: falls back to "default" shop, no panic
//   - TestHandoffToHumanTool_NoCustomer_GeneratesRefID: generates synthetic ref_id
//   - TestHandoffToHumanTool_LongMessage_Truncated: truncates last_user_message to ~200 chars
//
// The tool only writes to event_logs + returns a string (no external side effects),
// so all assertions are DB reads + return-string checks.
//
// Run:
//   go test ./tools/... -v -run TestHandoffToHumanTool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yuterigele/openbook/storage"
)

// handoffEventRows 读出指定 shop 的所有 handoff 事件行
func handoffEventRows(t *testing.T, shopID string) []storage.EventLog {
	t.Helper()
	var rows []storage.EventLog
	if err := storage.DB.Where("shop_id = ? AND event_type = ?", shopID, storage.EventHandoffToHuman).
		Find(&rows).Error; err != nil {
		t.Fatalf("query event_logs: %v", err)
	}
	return rows
}

// TestHandoffToHumanTool_BasicSuccess 正常路径：写埋点 + 返回成功摘要
func TestHandoffToHumanTool_BasicSuccess(t *testing.T) {
	setupToolsTestDB(t)
	tool := &HandoffToHumanTool{}

	ctx := WithShopID(context.Background(), "shop-1")
	args, _ := json.Marshal(map[string]string{
		"customer":          "Alice",
		"reason":            "顾客要求找店长",
		"last_user_message": "我要投诉 Tony",
	})

	out, err := tool.InvokableRun(ctx, string(args))
	if err != nil {
		t.Fatalf("InvokableRun returned error: %v", err)
	}
	if !strings.Contains(out, "Alice") || !strings.Contains(out, "已为顾客") {
		t.Errorf("返回文案应含顾客名和成功提示，实际: %q", out)
	}

	rows := handoffEventRows(t, "shop-1")
	if len(rows) != 1 {
		t.Fatalf("expected 1 handoff event, got %d", len(rows))
	}
	if rows[0].RefID != "Alice" {
		t.Errorf("RefID = %q, want %q", rows[0].RefID, "Alice")
	}
	if rows[0].EventType != storage.EventHandoffToHuman {
		t.Errorf("EventType = %q, want %q", rows[0].EventType, storage.EventHandoffToHuman)
	}
	// Meta 应含 reason / last_user_message / via
	for _, key := range []string{"reason", "last_user_message", "via"} {
		if !strings.Contains(rows[0].Meta, key) {
			t.Errorf("meta should contain key %q, got: %s", key, rows[0].Meta)
		}
	}
	if !strings.Contains(rows[0].Meta, "顾客要求找店长") {
		t.Errorf("meta should contain reason text, got: %s", rows[0].Meta)
	}
}

// TestHandoffToHumanTool_EmptyReason_Errors 缺 reason 必报
func TestHandoffToHumanTool_EmptyReason_Errors(t *testing.T) {
	setupToolsTestDB(t)
	tool := &HandoffToHumanTool{}

	ctx := WithShopID(context.Background(), "shop-1")
	args, _ := json.Marshal(map[string]string{
		"customer": "Bob",
		// reason 故意留空
	})

	_, err := tool.InvokableRun(ctx, string(args))
	if err == nil {
		t.Fatal("expected error for empty reason, got nil")
	}
	if !strings.Contains(err.Error(), "reason") {
		t.Errorf("error should mention 'reason', got: %v", err)
	}

	// 不应有埋点写入
	if rows := handoffEventRows(t, "shop-1"); len(rows) != 0 {
		t.Errorf("expected no event rows when reason is empty, got %d", len(rows))
	}
}

// TestHandoffToHumanTool_NoShopID_Fallback ctx 无 shop_id 时 fallback 到 "default"，不 panic
func TestHandoffToHumanTool_NoShopID_Fallback(t *testing.T) {
	setupToolsTestDB(t)
	tool := &HandoffToHumanTool{}

	// 注意：context.Background() 不带 shop_id
	args, _ := json.Marshal(map[string]string{
		"customer": "Carol",
		"reason":   "无法识别顾客意图",
	})

	out, err := tool.InvokableRun(context.Background(), string(args))
	if err != nil {
		t.Fatalf("InvokableRun with no shopID should fallback, got error: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty success summary")
	}

	// 埋点应写入 "default" shop
	if rows := handoffEventRows(t, "default"); len(rows) != 1 {
		t.Fatalf("expected 1 event under shop=default, got %d", len(rows))
	}
}

// TestHandoffToHumanTool_NoCustomer_GeneratesRefID 没 customer 时 ref_id 用 unknown-<nano> 兜底
func TestHandoffToHumanTool_NoCustomer_GeneratesRefID(t *testing.T) {
	setupToolsTestDB(t)
	tool := &HandoffToHumanTool{}

	ctx := WithShopID(context.Background(), "shop-1")
	args, _ := json.Marshal(map[string]string{
		"reason": "连续 2 轮无法识别意图",
	})

	out, err := tool.InvokableRun(ctx, string(args))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	// 返回文案里应体现 fallback ref_id
	if !strings.Contains(out, "unknown-") {
		t.Errorf("返回文案应含 fallback ref_id 'unknown-...', 实际: %q", out)
	}

	rows := handoffEventRows(t, "shop-1")
	if len(rows) != 1 {
		t.Fatalf("expected 1 event, got %d", len(rows))
	}
	if !strings.HasPrefix(rows[0].RefID, "unknown-") {
		t.Errorf("RefID should start with 'unknown-', got %q", rows[0].RefID)
	}
}

// TestHandoffToHumanTool_LongMessage_Truncated 超长 last_user_message 应被截断到 ~200 字符
func TestHandoffToHumanTool_LongMessage_Truncated(t *testing.T) {
	setupToolsTestDB(t)
	tool := &HandoffToHumanTool{}

	longMsg := strings.Repeat("X", 500) // 远超 200
	ctx := WithShopID(context.Background(), "shop-1")
	args, _ := json.Marshal(map[string]string{
		"customer":          "Dave",
		"reason":            "测试长消息",
		"last_user_message": longMsg,
	})

	if _, err := tool.InvokableRun(ctx, string(args)); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}

	rows := handoffEventRows(t, "shop-1")
	if len(rows) != 1 {
		t.Fatalf("expected 1 event, got %d", len(rows))
	}
	// meta 里的 last_user_message 应被截断（200 + 省略号 = 201 字符 + JSON 引号/转义）
	// 简单判定：不应含 500 个连续 X
	if strings.Contains(rows[0].Meta, strings.Repeat("X", 500)) {
		t.Errorf("meta should NOT contain untruncated 500 X's, got meta length=%d", len(rows[0].Meta))
	}
	// 也不应比 500 字符原文长太多（JSON 包装 + 截断后大概 250-300 字符）
	if len(rows[0].Meta) > 400 {
		t.Errorf("meta seems too long after truncation, len=%d", len(rows[0].Meta))
	}
}