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
	"sync"

	"github.com/yuterigele/openbook/intent"
)

// defaultStubClassifier returns a process-wide intent classifier
// configured for keyword-only classification (no LLM layer). The stub
// reply path is precisely the case where the LLM is unavailable, so
// wiring the LLM layer in would defeat the purpose.
//
// The instance is created once and reused (Classifier is goroutine
// safe for the keyword path — it has no mutable state once built).
var (
	stubClfOnce sync.Once
	stubClf     *intent.Classifier
)

func defaultStubClassifier() *intent.Classifier {
	stubClfOnce.Do(func() {
		stubClf = intent.NewClassifier() // keyword-only
	})
	return stubClf
}

// PickStubReply selects a reply string for the given user text. It
// re-uses the intent.Classifier (Layer-1 keyword whitelist) so the
// stub replies are aligned with the same intent taxonomy the real
// agent uses — only the destination of the reply differs (here:
// canned text; there: tool calls).
//
// The reply catalog is deliberately short and human-friendly. The
// fallback ("AI 助手暂不可用...") is what 99% of customers see when
// every LLM provider is down; the per-intent variants are nice
// touches that make the bot feel "alive" even in degraded mode.
//
// Exported so operators can unit-test the reply catalog.
func PickStubReply(userText string) string {
	clf := defaultStubClassifier()
	r := clf.Classify(context.Background(), userText)
	switch r.Intent {
	case "greeting":
		return "你好，当前 AI 助手暂时不可用。如需预约请回复「人工」转接前台，或工作日 9:00-18:00 来电咨询。"
	case "handoff":
		return "好的，我帮您转接前台。由于 AI 助手当前不可用，请稍等，工作时间 5 分钟内回您。"
	case "complaint":
		return "非常抱歉给您带来不便。AI 助手当前暂不可用，已记录您的反馈，工作时间会有专人跟进。"
	case "book", "cancel", "reschedule", "query_open":
		return "抱歉，当前 AI 助手暂不可用，暂时无法为您办理预约/改约/查档。如需人工服务请回复「人工」。"
	case "list_barbers", "list_service", "list_holiday":
		return "抱歉，当前 AI 助手暂不可用，相关信息暂时无法查询。如需人工服务请回复「人工」。"
	case "chitchat":
		return "感谢您的关心 :) 当前 AI 助手暂不可用，如需帮助请直接描述问题或回复「人工」。"
	default:
		return "抱歉，当前 AI 助手暂不可用。如需预约或咨询，请工作日 9:00-18:00 来电，或回复「人工」转接前台。"
	}
}
