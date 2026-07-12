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

// Package intent provides user-intent classification for the agent.
//
// Two-layer design (matches the resume's "关键词白名单 + LLM 分类双保险"):
//
//	Layer 1 — keyword whitelist: substring match against per-intent
//	          trigger words. Cheap, deterministic, ~0 cost, catches the
//	          obvious cases ("预约" → book, "取消" → cancel).
//
//	Layer 2 — LLM classifier: when Layer 1 has no confident match, ask
//	          a small LLM call to classify from a closed intent set. This
//	          catches colloquial / paraphrased inputs the keywords miss
//	          ("明天下午想去找个师傅剪个头发" → book).
//
// The classifier is exposed to the Agent as a Tool (intent.ClassifyTool)
// so the Agent can branch on the result explicitly. It can also be called
// directly from server.go for analytics / routing.
package intent

import "context"

// Intent is the closed set of user intents the agent handles.
type Intent string

const (
	// Booking-related
	IntentBook        Intent = "book"         // 创建预约
	IntentCancel      Intent = "cancel"       // 取消预约
	IntentReschedule  Intent = "reschedule"   // 改时间 / 改师傅
	IntentQueryOpen   Intent = "query_open"   // 查可约时段 / 师傅档期

	// Info
	IntentListBarbers Intent = "list_barbers" // 列本店师傅
	IntentListService Intent = "list_service" // 列服务 / 价格
	IntentListHoliday Intent = "list_holiday" // 节假日 / 营业时间

	// Personal
	IntentGreeting   Intent = "greeting"   // 你好 / 在吗
	IntentComplaint  Intent = "complaint"  // 投诉 / 退款 / 差评
	IntentHandoff    Intent = "handoff"    // 找人工 / 叫老板
	IntentCancelAppt Intent = "cancel_appt" // 不再使用，留作 placeholder

	// Out-of-domain / unclear
	IntentChitchat Intent = "chitchat"  // 闲聊
	IntentUnknown  Intent = "unknown"   // 真·不知道
)

// AllIntents lists every known intent (used by Layer 2 prompt + tests).
// Keep sorted for stable order in JSON output.
var AllIntents = []Intent{
	IntentBook, IntentCancel, IntentReschedule, IntentQueryOpen,
	IntentListBarbers, IntentListService, IntentListHoliday,
	IntentGreeting, IntentComplaint, IntentHandoff,
	IntentChitchat, IntentUnknown,
}

// ClassifyResult is the structured output of Classify.
type ClassifyResult struct {
	Intent     Intent   // the chosen intent
	Confidence float64  // 0.0 - 1.0
	Source     string   // "keyword" / "llm" / "default"
	// TriggerWord is the keyword that matched (Layer 1 only).
	TriggerWord string
	// LMRationale is the model's one-line explanation (Layer 2 only).
	LMRationale string
}

// Classifier combines a keyword layer and an LLM layer.
//
// The LLM layer is set via WithLLMClassify. If nil, only keywords are used
// (which is fine for tests and offline use).
type Classifier struct {
	llm LLMClassifyFunc
}

// LLMClassifyFunc is the LLM-side signature. Implementations are thin
// adapters over the chatmodel package (kept in a separate file to avoid
// pulling chatmodel into the keyword-only tests).
type LLMClassifyFunc func(ctx context.Context, userText string, intents []Intent) (Intent, float64, string, error)

// NewClassifier returns a new classifier.
func NewClassifier() *Classifier {
	return &Classifier{}
}

// WithLLMClassify sets the LLM layer. Pass nil to disable.
func (c *Classifier) WithLLMClassify(fn LLMClassifyFunc) *Classifier {
	c.llm = fn
	return c
}

// Classify runs Layer 1 (keyword). If it returns a high-confidence hit
// (≥ keywordThreshold), it returns immediately. Otherwise it falls
// through to Layer 2 (LLM) if available; otherwise returns the keyword
// pick with reduced confidence.
func (c *Classifier) Classify(ctx context.Context, userText string) ClassifyResult {
	if userText == "" {
		return ClassifyResult{Intent: IntentUnknown, Confidence: 0, Source: "default"}
	}
	// Layer 1: keyword
	intent, word, conf := keywordMatch(userText)
	if conf >= keywordThreshold {
		return ClassifyResult{
			Intent: intent, Confidence: conf, Source: "keyword",
			TriggerWord: word,
		}
	}
	// Layer 2: LLM
	if c.llm == nil {
		// No LLM layer configured: return the best keyword guess with low
		// confidence so the agent knows it was uncertain.
		return ClassifyResult{
			Intent: intent, Confidence: conf * 0.5, Source: "keyword",
			TriggerWord: word,
		}
	}
	picked, lconf, rationale, err := c.llm(ctx, userText, AllIntents)
	if err != nil || picked == "" {
		return ClassifyResult{
			Intent: IntentUnknown, Confidence: 0.3, Source: "llm",
			LMRationale: "llm error: " + errString(err),
		}
	}
	if lconf < 0.5 {
		picked = IntentUnknown
	}
	return ClassifyResult{
		Intent: picked, Confidence: lconf, Source: "llm",
		LMRationale: rationale,
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// keywordThreshold is the confidence above which we trust the keyword
// layer and skip the LLM. Set to 0.7 — keyword matches with multiple
// trigger words earn 0.9+, single hit earns 0.7.
const keywordThreshold = 0.7
