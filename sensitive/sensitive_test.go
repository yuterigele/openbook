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
	"strings"
	"testing"
)

func TestCheck_Empty(t *testing.T) {
	r := Check("")
	if r.Blocked {
		t.Errorf("empty text should not be blocked, got %+v", r)
	}
}

func TestCheck_CleanText(t *testing.T) {
	// Realistic customer service inputs that should always pass.
	clean := []string{
		"我想预约明天下午 3 点的剪发",
		"Tony 师傅什么时候有空？",
		"价格多少？",
		"我之前那个预约想改到下周",
		"Hello, can I book an appointment?",
		"剪发加染发一共多少钱？",
	}
	for _, in := range clean {
		if r := Check(in); r.Blocked {
			t.Errorf("expected clean text %q to pass, got blocked: %+v", in, r)
		}
	}
}

func TestCheck_RegisteredWords(t *testing.T) {
	// Use a fake category for the test to keep test data self-contained.
	const testCat Category = "test"
	defer Reset()

	AddWords(testCat, []string{"badword1", "badword2"})

	tests := []struct {
		input   string
		blocked bool
		word    string
	}{
		{"this contains badword1 inside", true, "badword1"},
		{"BADWORD2 upper", true, "badword2"}, // case-insensitive
		{"completely unrelated text", false, ""},
		{"badword1badword2 both", true, "badword1"}, // first hit wins
	}
	for _, tt := range tests {
		r := Check(tt.input)
		if r.Blocked != tt.blocked {
			t.Errorf("Check(%q) blocked=%v, want %v (full: %+v)",
				tt.input, r.Blocked, tt.blocked, r)
		}
		if tt.blocked && r.Word != tt.word {
			t.Errorf("Check(%q) word=%q, want %q", tt.input, r.Word, tt.word)
		}
	}
}

func TestCheck_RegisterReplaces(t *testing.T) {
	const testCat Category = "test"
	defer Reset()

	AddWords(testCat, []string{"old1", "old2"})
	if r := Check("contains old1"); !r.Blocked {
		t.Errorf("old1 should be blocked before replace")
	}

	RegisterWords(testCat, []string{"new1"})
	if r := Check("contains old1"); r.Blocked {
		t.Errorf("old1 should NOT be blocked after replace, got %+v", r)
	}
	if r := Check("contains new1"); !r.Blocked {
		t.Errorf("new1 should be blocked, got %+v", r)
	}
}

func TestCheck_ReasonProvided(t *testing.T) {
	// Verify all categories return a non-empty Reason string.
	cats := []Category{
		CategoryPolitics, CategoryPorn, CategoryViolence,
		CategoryAd, CategoryAbuse, CategoryIllegal,
	}
	for _, c := range cats {
		r := reasonFor(c)
		if strings.TrimSpace(r) == "" {
			t.Errorf("reasonFor(%q) returned empty string", c)
		}
	}
}

func TestReset(t *testing.T) {
	// After Reset, words added by the test should be gone.
	const testCat Category = "test"
	AddWords(testCat, []string{"ephemeral"})
	Reset()
	if r := Check("contains ephemeral"); r.Blocked {
		t.Errorf("after Reset, ephemeral should not be blocked, got %+v", r)
	}
}

func TestCategories(t *testing.T) {
	cats := Categories()
	if len(cats) == 0 {
		t.Error("expected at least one category, got 0")
	}
	// Make sure standard categories are present.
	have := map[Category]bool{}
	for _, c := range cats {
		have[c] = true
	}
	for _, want := range []Category{
		CategoryPolitics, CategoryPorn, CategoryViolence,
		CategoryAd, CategoryAbuse, CategoryIllegal,
	} {
		if !have[want] {
			t.Errorf("missing standard category %q", want)
		}
	}
}
