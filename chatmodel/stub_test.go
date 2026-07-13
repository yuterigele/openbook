/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package chatmodel

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// ---- stubMessageModel (M = *schema.Message) ------------------------

func TestStubMessage_Generate_ReturnsAssistantMessage(t *testing.T) {
	s := &stubMessageModel{}
	out, err := s.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("你好"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out.Role != schema.Assistant {
		t.Errorf("Role = %q, want %q", out.Role, schema.Assistant)
	}
	if out.Content == "" {
		t.Error("Content should be non-empty")
	}
}

func TestStubMessage_Generate_NoToolCalls(t *testing.T) {
	// CRITICAL invariant: the stub must NOT return any tool calls. If
	// it did, the adk agent would invoke a tool (e.g. create_appointment)
	// and write to MySQL even though we're in degraded mode.
	s := &stubMessageModel{}
	out, err := s.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("明天下午 3 点预约剪发 Tony 师傅"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(out.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %v, want empty (stub must not call tools)", out.ToolCalls)
	}
}

func TestStubMessage_Generate_PicksByIntent(t *testing.T) {
	s := &stubMessageModel{}
	cases := []struct {
		input  string
		hasSub string
	}{
		{"你好", "AI 助手"},
		{"转人工", "前台"},
		{"投诉你们的服务态度", "不便"},
		{"明天下午 3 点剪发", "预约"},
		{"价格多少", "查询"},
		{"今天天气真好", "关心"},
		{"asdfghjkl", "暂不可用"},
	}
	for _, c := range cases {
		out, err := s.Generate(context.Background(), []*schema.Message{
			schema.UserMessage(c.input),
		})
		if err != nil {
			t.Errorf("input %q: Generate: %v", c.input, err)
			continue
		}
		if !strings.Contains(out.Content, c.hasSub) {
			t.Errorf("input %q: reply %q does not contain %q", c.input, out.Content, c.hasSub)
		}
	}
}

func TestStubMessage_Generate_EmptyInput(t *testing.T) {
	s := &stubMessageModel{}
	out, err := s.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out.Content == "" {
		t.Error("empty input should still get a canned reply, got empty")
	}
}

func TestStubMessage_Stream_ClosesAfterOneChunk(t *testing.T) {
	s := &stubMessageModel{}
	sr, err := s.Stream(context.Background(), []*schema.Message{
		schema.UserMessage("hello"),
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if sr == nil {
		t.Fatal("Stream returned nil reader")
	}
	// Read the single chunk and ensure the reader closes.
	msg, err := sr.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if msg.Role != schema.Assistant {
		t.Errorf("Role = %q, want %q", msg.Role, schema.Assistant)
	}
	// Second Recv should return EOF.
	_, err = sr.Recv()
	if err == nil {
		t.Error("expected EOF after first chunk, got nil")
	}
}

func TestStubMessage_WithTools_ReturnsSelf(t *testing.T) {
	// WithTools is a no-op: the stub never calls tools, so binding
	// them is a no-op. Returning self is safe because the stub is
	// stateless (only reads request, no shared state).
	s := &stubMessageModel{}
	got, err := s.WithTools([]*schema.ToolInfo{{Name: "irrelevant"}})
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	if got != s {
		t.Errorf("WithTools should return the same instance (stateless), got %p want %p", got, s)
	}
}

// ---- stubAgenticModel (M = *schema.AgenticMessage) ----------------

func TestStubAgentic_Generate_ReturnsAgenticMessage(t *testing.T) {
	s := &stubAgenticModel{}
	out, err := s.Generate(context.Background(), []*schema.AgenticMessage{
		{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{
			{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "你好"}},
		}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out.Role != schema.AgenticRoleTypeAssistant {
		t.Errorf("Role = %q, want %q", out.Role, schema.AgenticRoleTypeAssistant)
	}
	if len(out.ContentBlocks) == 0 {
		t.Fatal("ContentBlocks should not be empty")
	}
	// Verify the text block carries a canned reply.
	var got string
	for _, cb := range out.ContentBlocks {
		if cb.AssistantGenText != nil {
			got += cb.AssistantGenText.Text
		}
	}
	if got == "" {
		t.Error("expected non-empty reply in ContentBlocks")
	}
}

func TestStubAgentic_Generate_EmptyInput(t *testing.T) {
	s := &stubAgenticModel{}
	out, err := s.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out == nil {
		t.Fatal("Generate returned nil message")
	}
}

// ---- PickStubReply (intent-based catalog) --------------------------

func TestPickStubReply_EachIntent(t *testing.T) {
	// Each case must trigger a real intent in the keyword whitelist
	// (intent/keywords.go). The expected substring is the verbatim
	// prefix of the corresponding canned reply.
	cases := []struct {
		input  string
		hasSub string
	}{
		{"asdfghjkl", "暂不可用"},  // default fallback
		{"你好", "AI 助手"},          // greeting
		{"hi there", "AI 助手"},       // greeting (English "hi")
		{"转人工", "前台"},             // handoff
		{"投诉你们服务差", "不便"},     // complaint
		{"预约剪发", "预约"},           // book
		{"取消预约", "预约"},           // cancel
		{"改时间到下周", "预约"},       // reschedule
		{"明天有档期吗", "预约"},       // query_open
		{"有哪些师傅", "查询"},        // list_barbers
		{"有什么服务", "查询"},        // list_service
		{"营业时间几点", "查询"},      // list_holiday
		{"今天天气真好", "关心"},      // chitchat
	}
	for _, c := range cases {
		got := PickStubReply(c.input)
		if !strings.Contains(got, c.hasSub) {
			t.Errorf("input %q: reply %q does not contain %q", c.input, got, c.hasSub)
		}
	}
}
