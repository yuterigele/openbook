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
	"testing"
)

func TestKeywordMatch_Book(t *testing.T) {
	tests := []string{
		"我想预约明天下午 3 点",
		"可以帮我订一下吗",
		"想去剪头发",
		"I want to book an appointment",
		"明天下午想去剪头发",
	}
	for _, in := range tests {
		intent, word, conf := keywordMatch(in)
		if intent != IntentBook {
			t.Errorf("keywordMatch(%q) intent=%q, want %q (word=%q conf=%v)",
				in, intent, IntentBook, word, conf)
		}
		if conf < keywordThreshold {
			t.Errorf("keywordMatch(%q) conf=%v, want >= %v", in, conf, keywordThreshold)
		}
	}
}

func TestKeywordMatch_Cancel(t *testing.T) {
	tests := []string{
		"帮我取消",
		"我不来了",
		"帮我退掉",
		"cancel my appointment",
	}
	for _, in := range tests {
		intent, _, _ := keywordMatch(in)
		if intent != IntentCancel {
			t.Errorf("keywordMatch(%q) intent=%q, want %q", in, intent, IntentCancel)
		}
	}
}

func TestKeywordMatch_Reschedule(t *testing.T) {
	tests := []string{
		"帮我改到明天",
		"换时间可以吗",
		"推迟到下周",
		"提前到 3 点",
		"想挪到周日",
	}
	for _, in := range tests {
		intent, _, _ := keywordMatch(in)
		if intent != IntentReschedule {
			t.Errorf("keywordMatch(%q) intent=%q, want %q", in, intent, IntentReschedule)
		}
	}
}

func TestKeywordMatch_Complaint(t *testing.T) {
	tests := []string{
		"我要投诉",
		"退款",
		"太差了",
		"骗人",
	}
	for _, in := range tests {
		intent, _, _ := keywordMatch(in)
		if intent != IntentComplaint {
			t.Errorf("keywordMatch(%q) intent=%q, want %q", in, intent, IntentComplaint)
		}
	}
}

func TestKeywordMatch_Handoff(t *testing.T) {
	tests := []string{
		"叫老板来",
		"找人工",
		"叫店长",
		"转人工",
	}
	for _, in := range tests {
		intent, _, _ := keywordMatch(in)
		if intent != IntentHandoff {
			t.Errorf("keywordMatch(%q) intent=%q, want %q", in, intent, IntentHandoff)
		}
	}
}

func TestKeywordMatch_NoMatch(t *testing.T) {
	intent, word, conf := keywordMatch("asdfghjkl qwertyuiop")
	if intent != "" || word != "" || conf != 0 {
		t.Errorf("expected no match, got intent=%q word=%q conf=%v", intent, word, conf)
	}
}

func TestKeywordMatch_MultiHitHigherConf(t *testing.T) {
	// Multiple trigger words for the same intent should give higher confidence.
	// c1: "我想约" — "想约" matches.
	// c2: "我想约明天去预约剪头发" — "想约" + "预约" both match book.
	_, _, c1 := keywordMatch("我想约")
	_, _, c2 := keywordMatch("我想约明天去预约剪头发")
	if c2 <= c1 {
		t.Errorf("multi-hit confidence should be higher: c1=%v, c2=%v", c1, c2)
	}
	if c1 < 0.7 {
		t.Errorf("c1 should be at least 0.7, got %v", c1)
	}
}

func TestClassifier_KeywordOnly(t *testing.T) {
	clf := NewClassifier() // no LLM layer
	r := clf.Classify(context.Background(), "我要预约明天下午 3 点")
	if r.Intent != IntentBook {
		t.Errorf("Intent=%q, want %q", r.Intent, IntentBook)
	}
	if r.Source != "keyword" {
		t.Errorf("Source=%q, want keyword", r.Source)
	}
	if r.Confidence < keywordThreshold {
		t.Errorf("Confidence=%v, want >= %v", r.Confidence, keywordThreshold)
	}
}

func TestClassifier_LLMFallback(t *testing.T) {
	// Layer 1 misses; Layer 2 (mocked) returns IntentQueryOpen with 0.8.
	clf := NewClassifier().WithLLMClassify(func(_ context.Context, _ string, _ []Intent) (Intent, float64, string, error) {
		return IntentQueryOpen, 0.8, "paraphrase", nil
	})
	r := clf.Classify(context.Background(), "asdf qwerty") // no keyword hit
	if r.Intent != IntentQueryOpen {
		t.Errorf("Intent=%q, want %q", r.Intent, IntentQueryOpen)
	}
	if r.Source != "llm" {
		t.Errorf("Source=%q, want llm", r.Source)
	}
	if r.LMRationale != "paraphrase" {
		t.Errorf("Rationale=%q, want %q", r.LMRationale, "paraphrase")
	}
}

func TestClassifier_LLMFallback_LowConfBecomesUnknown(t *testing.T) {
	clf := NewClassifier().WithLLMClassify(func(_ context.Context, _ string, _ []Intent) (Intent, float64, string, error) {
		return IntentBook, 0.3, "low", nil
	})
	r := clf.Classify(context.Background(), "asdf qwerty")
	if r.Intent != IntentUnknown {
		t.Errorf("low-confidence LLM should map to unknown, got %q", r.Intent)
	}
}

func TestParseClassifyResponse(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		intent    string
		conf      float64
		rationale string
		wantErr   bool
	}{
		{
			name:      "clean json",
			raw:       `{"intent": "book", "confidence": 0.9, "rationale": "预约关键字"}`,
			intent:    "book",
			conf:      0.9,
			rationale: "预约关键字",
		},
		{
			name:      "with code fences",
			raw:       "```json\n{\"intent\":\"cancel\",\"confidence\":0.7,\"rationale\":\"x\"}\n```",
			intent:    "cancel",
			conf:      0.7,
			rationale: "x",
		},
		{
			name:      "missing rationale",
			raw:       `{"intent": "book", "confidence": 0.5}`,
			intent:    "book",
			conf:      0.5,
			rationale: "",
		},
		{
			name:      "confidence clamped > 1",
			raw:       `{"intent": "book", "confidence": 1.5}`,
			intent:    "book",
			conf:      1.0,
		},
		{
			name:    "bad json",
			raw:     `not json`,
			wantErr: true,
		},
		{
			name:    "missing intent",
			raw:     `{"confidence": 0.5}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent, conf, rationale, err := parseClassifyResponse(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if string(intent) != tt.intent {
				t.Errorf("intent=%q, want %q", intent, tt.intent)
			}
			if conf != tt.conf {
				t.Errorf("conf=%v, want %v", conf, tt.conf)
			}
			if rationale != tt.rationale {
				t.Errorf("rationale=%q, want %q", rationale, tt.rationale)
			}
		})
	}
}

func TestClassifyTool(t *testing.T) {
	clf := NewClassifier()
	tl := NewClassifyTool(clf)
	info, err := tl.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "classify_intent" {
		t.Errorf("Name=%q, want classify_intent", info.Name)
	}
}
