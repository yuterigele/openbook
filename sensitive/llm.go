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
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ChatModel is the minimal interface the LLM fallback needs. Satisfied by
// *openai.ChatModel, *ark.ChatModel, and any other eino chat model that
// operates on *schema.Message. Keeping the surface small here lets tests
// stub it without pulling in the full eino ecosystem.
//
// The shape mirrors intent/llm.go so the two LLM-based classifiers share
// the same adapter pattern.
type ChatModel interface {
	Generate(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error)
}

// LLMClassifyFunc is the LLM-side signature for the sensitive-word
// fallback. The classifier is expected to return:
//
//   - cat:      the detected Category, or CategoryOther if clean
//   - blocked:  true if the model thinks the text is sensitive
//   - conf:     0.0-1.0 confidence
//   - err:      non-nil on model error / parse error / etc.
//
// Returning (CategoryOther, false, 0, nil) means "clean, no decision".
// The caller (CheckCtx) decides whether to block based on `blocked` and
// the configured LLMConfidenceThreshold.
type LLMClassifyFunc func(ctx context.Context, text string) (cat Category, blocked bool, conf float64, err error)

// NewLLMClassifyFuncFromEino adapts an eino chat model to LLMClassifyFunc.
// The function builds a strict prompt, calls the model, and parses the
// JSON reply.
//
// Cost: ~150 input tokens + ~30 output tokens per call. The fallback
// path is only triggered when the keyword layer (Layer 1) misses AND
// the text is at least LLMMinLength runes — so empty messages and
// "hi" do not pay the LLM cost.
//
// Failure mode: if the model call or JSON parse fails, the returned
// error is propagated. CheckCtx treats errors as fail-open (text
// passes) so a degraded LLM does not block legitimate users.
func NewLLMClassifyFuncFromEino(cm ChatModel) LLMClassifyFunc {
	return func(ctx context.Context, text string) (Category, bool, float64, error) {
		if cm == nil {
			return CategoryOther, false, 0, fmt.Errorf("sensitive llm: nil chat model")
		}
		if len([]rune(text)) < LLMMinLength {
			return CategoryOther, false, 0, nil
		}
		prompt := buildLLMPrompt(text)
		resp, err := cm.Generate(ctx, []*schema.Message{
			schema.UserMessage(prompt),
		})
		if err != nil {
			return CategoryOther, false, 0, fmt.Errorf("sensitive llm call: %w", err)
		}
		return parseLLMResponse(extractText(resp))
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

// buildLLMPrompt is the LLM-side prompt for sensitive-word classification.
//
// Constraints communicated to the model:
//   - closed category set (returns ONLY categories from the list)
//   - JSON-only output (no prose, no markdown fences)
//   - 0-1 confidence (the higher the more certain)
//   - "none" is the safe-by-default escape hatch
//   - explicit instruction to NOT over-classify (a normal haircut
//     appointment should pass), which addresses the main failure mode
//     we saw in early testing: model flagging benign customer service
//     inputs as ad.
func buildLLMPrompt(text string) string {
	return fmt.Sprintf(`你是一个内容安全审核助手。判断下面的用户消息是否包含以下任一敏感内容：
- politics（政治敏感、领导人相关）
- porn（色情、低俗）
- violence（暴力、血腥、武器）
- ad（广告、营销、推广、引流到其他平台）
- abuse（辱骂、人身攻击）
- illegal（违法、犯罪指导）

重要：只考虑消息内容本身，不要过度联想。正常的理发店预约对话（问价格、问档期、问候、改约等）应该被放行，不要把"我朋友推荐"这种自然提到别人的话误判为 ad。

用户消息：%q

请严格按 JSON 输出（不要解释、不要 markdown 代码块、不要前后缀）：
{"blocked": true|false, "category": "politics|porn|violence|ad|abuse|illegal|none", "confidence": 0.0-1.0}`,
		text)
}

// parseLLMResponse extracts category / blocked / confidence from the
// model's reply. Lenient on whitespace and surrounding code fences.
//
// The model may return the safe literal "none" (or empty) for category
// when not blocked; we normalize both to CategoryOther so downstream
// code does not have to special-case them.
func parseLLMResponse(raw string) (Category, bool, float64, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var out struct {
		Blocked    bool    `json:"blocked"`
		Category   string  `json:"category"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return CategoryOther, false, 0, fmt.Errorf("parse sensitive llm reply %q: %w", raw, err)
	}
	cat := Category(strings.TrimSpace(strings.ToLower(out.Category)))
	if cat == "none" || cat == "" {
		cat = CategoryOther
	}
	if out.Confidence < 0 {
		out.Confidence = 0
	}
	if out.Confidence > 1 {
		out.Confidence = 1
	}
	return cat, out.Blocked, out.Confidence, nil
}
