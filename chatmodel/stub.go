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

// Package chatmodel stub: degraded-mode fallback model.
//
// When EVERY real provider (DeepSeek / OpenAI / Ark) fails to
// initialize at startup, NewModelWithFallback returns one of the
// stub models defined here instead of an error. The stub is
// chat-only: it never makes a network call, never calls a tool, never
// writes the database. It picks a canned reply by running the user's
// last message through the intent.Classifier keyword layer, then
// returns an assistant-role message with empty ToolCalls.
//
// The adk.ChatModelAgent treats a no-tool-calls assistant message
// as a final turn, so the process keeps running and the customer
// gets a human-readable "AI 助手暂不可用" reply instead of a
// connection-refused error.
//
// Why two types: eino defines two parallel model interfaces for the
// two message kinds:
//
//   - ToolCallingChatModel — for *schema.Message, has WithTools()
//   - AgenticModel         — for *schema.AgenticMessage, NO WithTools
//                            (tools are passed via model.WithTools
//                            request option at call time)
//
// So we need two concrete stubs. Each is registered as a
// BaseModel[M] in the NewModelWithFallback type switch.
package chatmodel

import (
	"context"
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ----- M = *schema.Message (the default, non-agentic path) ----------

// stubMessageModel is the chat-only fallback for the M = *schema.Message
// path. It implements both einomodel.BaseChatModel and
// einomodel.ToolCallingChatModel so the eino adk agent can use it
// (the agent binds tools via model.WithTools request option).
type stubMessageModel struct{}

// Compile-time interface satisfaction checks.
var (
	_ einomodel.BaseModel[*schema.Message] = (*stubMessageModel)(nil)
	_ einomodel.BaseChatModel              = (*stubMessageModel)(nil)
	_ einomodel.ToolCallingChatModel       = (*stubMessageModel)(nil)
)

// Generate implements einomodel.BaseModel[*schema.Message].
func (s *stubMessageModel) Generate(_ context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	userText := lastUserTextFromMessages(input)
	return schema.AssistantMessage(PickStubReply(userText), nil), nil
}

// Stream implements einomodel.BaseModel[*schema.Message]. Returns a
// single-chunk stream that closes immediately.
func (s *stubMessageModel) Stream(_ context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	userText := lastUserTextFromMessages(input)
	msg := schema.AssistantMessage(PickStubReply(userText), nil)
	sr, sw := schema.Pipe[*schema.Message](1)
	sw.Send(msg, nil)
	sw.Close()
	return sr, nil
}

// WithTools is a no-op. The stub ignores tool definitions — the
// reply never contains a ToolCall, so binding tools changes nothing.
func (s *stubMessageModel) WithTools(_ []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return s, nil
}

// ----- M = *schema.AgenticMessage (the agentic path) ----------------

// stubAgenticModel is the chat-only fallback for the M = *schema.AgenticMessage
// path. Implements einomodel.AgenticModel (= BaseModel[*schema.AgenticMessage]).
// Note: AgenticModel has NO WithTools method — tools are passed via
// the model.WithTools request option at call time.
type stubAgenticModel struct{}

// Compile-time interface satisfaction check.
var (
	_ einomodel.BaseModel[*schema.AgenticMessage] = (*stubAgenticModel)(nil)
	_ einomodel.AgenticModel                      = (*stubAgenticModel)(nil)
)

// Generate implements einomodel.BaseModel[*schema.AgenticMessage].
func (s *stubAgenticModel) Generate(_ context.Context, input []*schema.AgenticMessage, _ ...einomodel.Option) (*schema.AgenticMessage, error) {
	userText := lastUserTextFromAgentic(input)
	return assistantAgenticMessage(PickStubReply(userText)), nil
}

// Stream implements einomodel.BaseModel[*schema.AgenticMessage].
func (s *stubAgenticModel) Stream(_ context.Context, input []*schema.AgenticMessage, _ ...einomodel.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	userText := lastUserTextFromAgentic(input)
	msg := assistantAgenticMessage(PickStubReply(userText))
	sr, sw := schema.Pipe[*schema.AgenticMessage](1)
	sw.Send(msg, nil)
	sw.Close()
	return sr, nil
}

// ----- shared reply-selection helpers --------------------------------

// lastUserTextFromMessages returns the content of the most recent
// user-role message. Empty string if input is empty / all-nil.
func lastUserTextFromMessages(input []*schema.Message) string {
	for i := len(input) - 1; i >= 0; i-- {
		if input[i] == nil {
			continue
		}
		if input[i].Role == schema.User {
			return input[i].Content
		}
	}
	for i := len(input) - 1; i >= 0; i-- {
		if input[i] != nil {
			return input[i].Content
		}
	}
	return ""
}

// lastUserTextFromAgentic does the same for the AgenticMessage
// variant. An AgenticMessage's role is schema.AgenticRoleType.
func lastUserTextFromAgentic(input []*schema.AgenticMessage) string {
	for i := len(input) - 1; i >= 0; i-- {
		if input[i] == nil {
			continue
		}
		if input[i].Role == schema.AgenticRoleTypeUser {
			return agenticMessageText(input[i])
		}
	}
	for i := len(input) - 1; i >= 0; i-- {
		if input[i] != nil {
			return agenticMessageText(input[i])
		}
	}
	return ""
}

// agenticMessageText flattens a *schema.AgenticMessage's content
// blocks into a single string. Most messages have a single text
// block; we join with newlines if there are more.
//
// Returns "" if there is no assistant text in any block. We avoid
// fmt.Sprintf("%v", m) here because eino's AgenticMessage contains
// pointer-rich substructures whose default formatter walks a cycle
// in some configurations and triggers a stack overflow.
func agenticMessageText(m *schema.AgenticMessage) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	for _, cb := range m.ContentBlocks {
		if cb == nil || cb.AssistantGenText == nil {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(cb.AssistantGenText.Text)
	}
	return b.String()
}

// assistantAgenticMessage builds an assistant-role AgenticMessage
// with a single text block. Mirrors the structure of messages
// produced by OpenAI Responses / Ark agentic models.
func assistantAgenticMessage(text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			{
				Type:             schema.ContentBlockTypeAssistantGenText,
				AssistantGenText: &schema.AssistantGenText{Text: text},
			},
		},
	}
}
