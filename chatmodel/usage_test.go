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
	"strings"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// helper: build a model CallbackOutput (chat path) with the given
// usage wired in.
func makeChatOutput(prompt, completion, total int) callbacks.CallbackOutput {
	msg := &schema.Message{
		Role: schema.Assistant,
		ResponseMeta: &schema.ResponseMeta{
			Usage: &schema.TokenUsage{
				PromptTokens:     prompt,
				CompletionTokens: completion,
				TotalTokens:      total,
			},
		},
	}
	return msg // einomodel.ConvCallbackOutput accepts *schema.Message directly
}

// helper: build an agentic CallbackOutput with the given usage.
func makeAgenticOutput(prompt, completion, total int) callbacks.CallbackOutput {
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ResponseMeta: &schema.AgenticResponseMeta{
			TokenUsage: &schema.TokenUsage{
				PromptTokens:     prompt,
				CompletionTokens: completion,
				TotalTokens:      total,
			},
		},
	}
	return msg
}

func TestUsageTracker_AddUsageFromMessage(t *testing.T) {
	DefaultUsageTracker.Reset()
	addUsageFromMessage(DefaultUsageTracker, &schema.TokenUsage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	})
	snap := DefaultUsageTracker.Snapshot()
	if snap.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", snap.PromptTokens)
	}
	if snap.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d, want 50", snap.CompletionTokens)
	}
	if snap.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", snap.TotalTokens)
	}
}

func TestUsageTracker_AddUsageFromMessage_NilUsage(t *testing.T) {
	// nil usage should be a no-op (defensive against providers that
	// return messages without ResponseMeta).
	DefaultUsageTracker.Reset()
	addUsageFromMessage(DefaultUsageTracker, nil)
	snap := DefaultUsageTracker.Snapshot()
	if snap.TotalTokens != 0 {
		t.Errorf("nil usage should be no-op, got TotalTokens=%d", snap.TotalTokens)
	}
}

func TestUsageHandler_OnEnd_ChatPath(t *testing.T) {
	DefaultUsageTracker.Reset()
	h := NewUsageHandler()

	// Filter: only "ChatModel" name is processed.
	ctx := h.OnEnd(t.Context(), &callbacks.RunInfo{Name: "ChatModel"},
		makeChatOutput(200, 80, 280))
	if ctx == nil {
		t.Fatal("OnEnd returned nil ctx")
	}

	snap := DefaultUsageTracker.Snapshot()
	if snap.PromptTokens != 200 || snap.CompletionTokens != 80 || snap.TotalTokens != 280 {
		t.Errorf("usage not extracted: %+v", snap)
	}
	if snap.Calls != 1 {
		t.Errorf("Calls = %d, want 1", snap.Calls)
	}
	if snap.NonStreamingCalls != 1 {
		t.Errorf("NonStreamingCalls = %d, want 1", snap.NonStreamingCalls)
	}
}

func TestUsageHandler_OnEnd_AgenticPath(t *testing.T) {
	DefaultUsageTracker.Reset()
	h := NewUsageHandler()

	ctx := h.OnEnd(t.Context(), &callbacks.RunInfo{Name: "ChatModel"},
		makeAgenticOutput(300, 120, 420))
	if ctx == nil {
		t.Fatal("OnEnd returned nil ctx")
	}

	snap := DefaultUsageTracker.Snapshot()
	if snap.PromptTokens != 300 || snap.CompletionTokens != 120 || snap.TotalTokens != 420 {
		t.Errorf("agentic usage not extracted: %+v", snap)
	}
}

func TestUsageHandler_OnEnd_IgnoresNonChatModel(t *testing.T) {
	// Tool calls, agent invocations, etc. should NOT count toward
	// LLM token usage. Filter via RunInfo.Name.
	DefaultUsageTracker.Reset()
	h := NewUsageHandler()

	for _, name := range []string{"Tool", "Agent", "Retriever", ""} {
		h.OnEnd(t.Context(), &callbacks.RunInfo{Name: name},
			makeChatOutput(999, 999, 999))
	}
	snap := DefaultUsageTracker.Snapshot()
	if snap.Calls != 0 || snap.TotalTokens != 0 {
		t.Errorf("non-ChatModel components should be filtered: %+v", snap)
	}
}

func TestUsageHandler_OnError(t *testing.T) {
	DefaultUsageTracker.Reset()
	h := NewUsageHandler()

	h.OnError(t.Context(), &callbacks.RunInfo{Name: "ChatModel"}, nil)
	snap := DefaultUsageTracker.Snapshot()
	if snap.Calls != 1 {
		t.Errorf("Calls = %d, want 1", snap.Calls)
	}
	if snap.ErroredCalls != 1 {
		t.Errorf("ErroredCalls = %d, want 1", snap.ErroredCalls)
	}
	if snap.TotalTokens != 0 {
		t.Errorf("error should not contribute to token count, got %d", snap.TotalTokens)
	}
}

func TestUsageSnapshot_AvgTokensPerCall(t *testing.T) {
	DefaultUsageTracker.Reset()
	// 3 calls totaling 300 tokens → avg 100. We bump Calls manually
	// because the test exercises addUsageFromMessage directly, not
	// via the callback handler (which would increment Calls itself).
	for i := 0; i < 3; i++ {
		addUsageFromMessage(DefaultUsageTracker, &schema.TokenUsage{
			PromptTokens: 60, CompletionTokens: 40, TotalTokens: 100,
		})
		DefaultUsageTracker.Calls.Add(1)
	}
	snap := DefaultUsageTracker.Snapshot()
	if snap.AvgTokensPerCall != 100 {
		t.Errorf("AvgTokensPerCall = %d, want 100", snap.AvgTokensPerCall)
	}
}

func TestUsageSnapshot_TokenAlertWindow(t *testing.T) {
	t.Setenv("LLM_TOKEN_ALERT_5M", "100")
	tracker := &UsageTracker{}
	tracker.addUsage(&schema.TokenUsage{TotalTokens: 100})
	snap := tracker.Snapshot()
	if snap.TokensLast5m != 100 {
		t.Fatalf("TokensLast5m = %d, want 100", snap.TokensLast5m)
	}
	if !snap.TokenAlert5m {
		t.Fatal("token alert should trigger at the configured 5-minute threshold")
	}
}

func TestUsageTracker_PrometheusText_AllSeriesPresent(t *testing.T) {
	DefaultUsageTracker.Reset()
	DefaultUsageTracker.Calls.Store(7)
	DefaultUsageTracker.TotalTokens.Store(1234)
	out := DefaultUsageTracker.PrometheusText()

	wantSeries := []string{
		"openbook_llm_prompt_tokens_total",
		"openbook_llm_completion_tokens_total",
		"openbook_llm_total_tokens_total",
		"openbook_llm_calls_total",
		"openbook_llm_errored_calls_total",
		"openbook_llm_streaming_calls_total",
		"openbook_llm_non_streaming_calls_total",
		"openbook_llm_avg_tokens_per_call",
	}
	for _, s := range wantSeries {
		if !strings.Contains(out, s) {
			t.Errorf("PrometheusText missing series %q", s)
		}
	}
}

// Sanity: a model.CallOption slice is what OnEnd / OnEndWithStreamOutput
// expect. We don't call those code paths here (we'd need a real
// ChatModel client), but the build should at least type-check.
var _ model.Option

func TestUsageHandler_NilInfo(t *testing.T) {
	// Defensive: nil RunInfo should be a no-op, not panic.
	DefaultUsageTracker.Reset()
	h := NewUsageHandler()

	ctx := h.OnEnd(t.Context(), nil, makeChatOutput(1, 1, 2))
	if ctx == nil {
		t.Fatal("OnEnd returned nil ctx")
	}
	snap := DefaultUsageTracker.Snapshot()
	if snap.TotalTokens != 0 {
		t.Errorf("nil RunInfo should be filtered, got %+v", snap)
	}
}
