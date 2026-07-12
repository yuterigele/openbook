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

package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ChatModel is the minimal interface the LLM classifier needs. Satisfied
// by *openai.ChatModel, *ark.ChatModel, and any other eino chat model
// (any einomodel.ToolCallingChatModel). Keeping the surface small here
// lets tests stub it without pulling in the full eino ecosystem.
type ChatModel interface {
	Generate(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error)
}

// NewLLMClassifyFunc returns an LLMClassifyFunc that uses the given chat
// model. The function builds a strict prompt, calls the model, and parses
// the JSON reply.
//
// Model choice: any chat model works. The prompt is small (~150 tokens)
// and the expected response is ~30 tokens, so even a slow model is fast.
//
// The model is expected to respond with strict JSON:
//
//	{"intent": "book", "confidence": 0.85, "rationale": "..."}
//
// Parsing is lenient: missing fields fall back to defaults; bad JSON
// returns an error so the caller can mark the result as low-confidence.
func NewLLMClassifyFunc(cm ChatModel) LLMClassifyFunc {
	return func(ctx context.Context, userText string, intents []Intent) (Intent, float64, string, error) {
		if cm == nil {
			return "", 0, "", fmt.Errorf("nil chat model")
		}
		prompt := buildClassifyPrompt(userText, intents)
		resp, err := cm.Generate(ctx, []*schema.Message{
			schema.UserMessage(prompt),
		})
		if err != nil {
			return "", 0, "", fmt.Errorf("llm call: %w", err)
		}
		intentStr, conf, rationale, err := parseClassifyResponse(extractText(resp))
		if err != nil {
			return "", 0, "", err
		}
		// Validate that the returned intent is in the allowed set.
		for _, allowed := range intents {
			if string(allowed) == intentStr {
				return allowed, conf, rationale, nil
			}
		}
		return IntentUnknown, 0.3, "out-of-set: " + intentStr, nil
	}
}

// extractText pulls the text out of a *schema.Message. The content is
// always a string in our use case (we use schema.UserMessage(prompt)).
func extractText(m *schema.Message) string {
	if m == nil {
		return ""
	}
	return m.Content
}

// NewLLMClassifyFuncFromEino adapts an eino *schema.Message chat model to
// the LLMClassifyFunc signature, hiding the generic M plumbing from
// callers. Use this when you have an einomodel.BaseModel[*schema.Message]
// (the typical case for the agent).
func NewLLMClassifyFuncFromEino(cm interface {
	Generate(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error)
}) LLMClassifyFunc {
	return NewLLMClassifyFunc(cm)
}

// buildClassifyPrompt is the LLM-side prompt for intent classification.
//
// Constraints communicated to the model:
//   - closed intent set (returns ONLY intents from the list)
//   - JSON-only output (no prose around it)
//   - 0-1 confidence (the higher the more certain)
func buildClassifyPrompt(userText string, intents []Intent) string {
	intentList := make([]string, 0, len(intents))
	for _, i := range intents {
		intentList = append(intentList, string(i))
	}
	return fmt.Sprintf(`You are an intent classifier for a Chinese hair-salon appointment booking assistant.

Allowed intents (pick EXACTLY ONE, or "unknown" if nothing fits):
%s

Customer message: %q

Respond with strict JSON only — no prose, no markdown fences. Schema:
{"intent": "<one of the allowed>", "confidence": <0.0-1.0>, "rationale": "<one short sentence>"}`,
		strings.Join(intentList, ", "), userText)
}

// parseClassifyResponse extracts intent, confidence, rationale from the
// model's reply. Lenient on whitespace / surrounding prose.
func parseClassifyResponse(raw string) (string, float64, string, error) {
	raw = strings.TrimSpace(raw)
	// Strip code fences if the model added them.
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var out struct {
		Intent     string  `json:"intent"`
		Confidence float64 `json:"confidence"`
		Rationale  string  `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return "", 0, "", fmt.Errorf("parse llm reply %q: %w", raw, err)
	}
	if out.Intent == "" {
		return "", 0, "", fmt.Errorf("llm reply missing intent field: %q", raw)
	}
	if out.Confidence < 0 {
		out.Confidence = 0
	}
	if out.Confidence > 1 {
		out.Confidence = 1
	}
	return out.Intent, out.Confidence, out.Rationale, nil
}
