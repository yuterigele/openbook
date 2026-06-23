package server

// v4.10.1 persist 过滤测试
//
// 覆盖 shouldPersistIntermediate 的所有场景：
//   - 纯文本 assistant（中间 chatter）→ false
//   - 带 tool_calls 的 assistant → true
//   - tool result → true
//   - user 消息 → true
//   - system 消息 → true
//
// 为什么重要：
//   - 修前：所有 intermediate 都 append → session history 有"我帮您查一下"等 chatter
//     → 下次 LLM 看到自己说过的 → 重复 / 不相干回复
//   - 修后：只保留 tool 相关 + user，caller 用 lastContent 补最终回复
//     → history 干净 → LLM 不再"接着说"

import (
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/msgops"
)

func TestShouldPersistIntermediate_Legacy(t *testing.T) {
	t.Setenv("MESSAGE_KIND", "message")
	type tc struct {
		name string
		msg  *schema.Message
		want bool
	}
	cases := []tc{
		{
			"user message",
			&schema.Message{Role: schema.User, Content: "hi"},
			true,
		},
		{
			"system message",
			&schema.Message{Role: schema.System, Content: "you are a helper"},
			true,
		},
		{
			"pure text assistant (chatter) — should skip",
			&schema.Message{Role: schema.Assistant, Content: "好的，我来查一下"},
			false,
		},
		{
			"empty assistant (no text no tools) — should skip",
			&schema.Message{Role: schema.Assistant},
			false,
		},
		{
			"tool result — should keep",
			&schema.Message{Role: schema.Tool, Content: "[]"},
			true,
		},
		{
			"assistant with tool calls — should keep",
			&schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{ID: "tc1", Function: schema.FunctionCall{Name: "query_schedule", Arguments: "{}"}},
				},
			},
			true,
		},
		{
			"assistant with both text and tool calls — should keep",
			&schema.Message{
				Role:    schema.Assistant,
				Content: "我帮您查一下",
				ToolCalls: []schema.ToolCall{
					{ID: "tc1", Function: schema.FunctionCall{Name: "query_schedule", Arguments: "{}"}},
				},
			},
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shouldPersistIntermediate[*schema.Message](c.msg)
			if got != c.want {
				t.Errorf("shouldPersistIntermediate = %v, want %v", got, c.want)
			}
		})
	}
}

func TestShouldPersistIntermediate_Agentic(t *testing.T) {
	t.Setenv("MESSAGE_KIND", "agentic")
	// AgenticMessage 中间步骤的纯文本 assistant 也会被过滤
	//   - "我帮您查一下" 纯文本 → false
	//   - 带 FunctionToolCall block 的 → true
	//   - 带 FunctionToolResult block 的 tool result（虽然 role 是 assistant 在 agentic 里）→ 看具体 block

	// 纯文本 assistant（content blocks 只有 AssistantGenText，没有 tool call）
	plain := msgops.NewAssistant[*schema.AgenticMessage]("好的，我来查一下", nil)
	if got := shouldPersistIntermediate[*schema.AgenticMessage](plain); got != false {
		t.Errorf("纯文本 assistant should be filtered (got=%v, want=false)", got)
	}

	// 带 tool call 的 assistant
	withTC := msgops.NewAssistant[*schema.AgenticMessage]("", []msgops.ToolCall{
		{ID: "tc1", Name: "query_schedule", Args: "{}", Index: 0},
	})
	if got := shouldPersistIntermediate[*schema.AgenticMessage](withTC); got != true {
		t.Errorf("带 tool call assistant should be kept (got=%v, want=true)", got)
	}

	// 既有文本又有 tool call
	both := msgops.NewAssistant[*schema.AgenticMessage]("我帮您查一下", []msgops.ToolCall{
		{ID: "tc1", Name: "query_schedule", Args: "{}", Index: 0},
	})
	if got := shouldPersistIntermediate[*schema.AgenticMessage](both); got != true {
		t.Errorf("混合 assistant should be kept (got=%v, want=true)", got)
	}
}

func TestShouldPersistIntermediate_NilSafe(t *testing.T) {
	t.Setenv("MESSAGE_KIND", "message")
	// nil message — 应该不 panic，返回 true（保守：避免吞掉有效消息）
	if got := shouldPersistIntermediate[*schema.Message](nil); !got {
		t.Errorf("nil message should default to true (not filtered)")
	}
}
