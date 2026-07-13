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

package sensitive

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// SensitiveCheckTool exposes the word filter as an Agent-callable tool.
//
// The Agent calls this on every user message at the start of a turn. The
// tool returns a structured result the Agent can branch on:
//
//	{"blocked": true,  "category": "porn",  "reason": "..."}  →  Agent
//	                                                         returns `reason`
//	                                                         verbatim to the
//	                                                         customer (do not
//	                                                         retry, do not
//	                                                         rephrase).
//
//	{"blocked": false, "category": "",      "reason": ""}      →  continue
//	                                                         normal flow.
//
// Under the hood the tool runs a two-layer filter:
//   - Layer 1: keyword match (always on, <1ms, 51,345 production words)
//   - Layer 2: LLM fallback (optional, wired in via WithLLMClassify)
//     catches paraphrased / slang attacks the keyword list misses.
//
// Why a tool (not a pre-model middleware): the LLM needs to *see* the
// blocked reason so it can pass it to the customer in the right tone and
// also record a handoff_to_human event for compliance audit. A pre-model
// middleware would short-circuit the LLM and lose both signals.
type SensitiveCheckTool struct{}

// Info returns the tool schema (consumed by the ChatModel for tool calling).
func (t *SensitiveCheckTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "sensitive_check",
		Desc: `Pre-check the user's message for sensitive content before further processing.
Call this on EVERY user message at the start of a turn.
Input: {"text": "the user's most recent message"}.
Output: {"blocked": bool, "category": "politics|porn|violence|ad|abuse|illegal|", "word": "...", "reason": "user-facing message when blocked", "source": "keyword|llm"}.

If blocked=true, return the value of "reason" VERBATIM to the customer and stop processing this turn. Do NOT retry, do NOT rephrase, do NOT call any other tool.
If blocked=false, continue the normal flow (call query_schedule / create_appointment / etc.).

The filter runs two layers: (1) keyword match, (2) LLM semantic fallback (only when keyword misses). The "source" field tells you which layer produced the block.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"text": {
				Type:     "string",
				Desc:     "the user's most recent message text",
				Required: true,
			},
		}),
	}, nil
}

type sensitiveCheckInput struct {
	Text string `json:"text"`
}

type sensitiveCheckOutput struct {
	Blocked  bool     `json:"blocked"`
	Category Category `json:"category"`
	Word     string   `json:"word,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	// Source is "keyword" / "llm" / "" (empty when not blocked). Useful
	// for observability: a flood of LLM-source blocks is a strong
	// signal the keyword list needs expanding.
	Source string `json:"source,omitempty"`
}

// InvokableRun executes the filter and returns JSON to the LLM.
func (t *SensitiveCheckTool) InvokableRun(ctx context.Context, argsIn string, opts ...tool.Option) (string, error) {
	var in sensitiveCheckInput
	if err := json.Unmarshal([]byte(argsIn), &in); err != nil {
		return "", fmt.Errorf("sensitive_check: invalid input: %w", err)
	}
	if in.Text == "" {
		return "", fmt.Errorf("sensitive_check: text is empty")
	}
	r := CheckCtx(ctx, in.Text)
	out := sensitiveCheckOutput{
		Blocked:  r.Blocked,
		Category: r.Category,
		Word:     r.Word,
		Reason:   r.Reason,
		Source:   r.Source,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("sensitive_check: marshal output: %w", err)
	}
	return string(b), nil
}

// Compile-time check.
var _ tool.InvokableTool = (*SensitiveCheckTool)(nil)
