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

// ---- Trie.Insert ----------------------------------------------------

func TestTrie_InsertEmptyIgnored(t *testing.T) {
	tr := newTrie()
	tr.Insert("", CategoryPorn)
	tr.Insert("real", CategoryPorn)
	if tr.Size() != 1 {
		t.Errorf("empty insert should be ignored, got Size=%d", tr.Size())
	}
}

func TestTrie_InsertReplacesCategory(t *testing.T) {
	// Re-inserting the same word under a different category should
	// update the category in place (used by JSON hot-reload when the
	// word moves from one category to another upstream).
	tr := newTrie()
	tr.Insert("foo", CategoryPorn)
	tr.Insert("foo", CategoryViolence)
	cat, _, hit := tr.Match("contains foo")
	if !hit {
		t.Fatal("expected hit")
	}
	if cat != CategoryViolence {
		t.Errorf("category = %q, want %q (second insert should win)", cat, CategoryViolence)
	}
}

func TestTrie_InsertSharedPrefix(t *testing.T) {
	// "中" and "中国" share a prefix; both must be findable.
	tr := newTrie()
	tr.Insert("中", CategoryPolitics)
	tr.Insert("中国", CategoryPolitics)

	// Scan "中国人" — should find the shorter word "中" first (left-to-right).
	cat, word, hit := tr.Match("我来自中国人")
	if !hit {
		t.Fatal("expected hit")
	}
	if word != "中" {
		t.Errorf("word = %q, want %q (earliest match wins)", word, "中")
	}
	if cat != CategoryPolitics {
		t.Errorf("category = %q, want %q", cat, CategoryPolitics)
	}
}

// ---- Trie.Match -----------------------------------------------------

func TestTrie_MatchEmptyText(t *testing.T) {
	tr := newTrie()
	tr.Insert("x", CategoryPorn)
	_, _, hit := tr.Match("")
	if hit {
		t.Error("empty text should not match")
	}
}

func TestTrie_MatchMiss(t *testing.T) {
	tr := newTrie()
	tr.Insert("badword", CategoryPorn)
	_, _, hit := tr.Match("this is clean text")
	if hit {
		t.Error("clean text should not match")
	}
}

func TestTrie_MatchCaseInsensitive(t *testing.T) {
	// Trie is case-insensitive — caller passes the lower-cased text.
	tr := newTrie()
	tr.Insert("badword", CategoryPorn)
	_, _, hit := tr.Match(strings.ToLower("this contains BADWORD inside"))
	if !hit {
		t.Error("expected case-insensitive hit")
	}
}

func TestTrie_MatchPriorityViolenceOverPolitics(t *testing.T) {
	// Two words starting at the same position; violence must win
	// because it has higher priority than politics.
	tr := newTrie()
	tr.Insert("暴力", CategoryViolence)
	tr.Insert("暴力政治", CategoryPolitics)
	cat, _, hit := tr.Match("我反对暴力政治")
	if !hit {
		t.Fatal("expected hit")
	}
	if cat != CategoryViolence {
		t.Errorf("category = %q, want %q (higher priority wins on same start)",
			cat, CategoryViolence)
	}
}

func TestTrie_MatchEarliestPositionWins(t *testing.T) {
	// Two words hit at different positions; the earlier one wins
	// (matches the prior strings.Contains semantics: first substring
	// hit).
	tr := newTrie()
	tr.Insert("中", CategoryPolitics)
	tr.Insert("暴力", CategoryViolence)
	_, word, hit := tr.Match("我来自中国，反暴力")
	if !hit {
		t.Fatal("expected hit")
	}
	if word != "中" {
		t.Errorf("word = %q, want %q (earlier position wins)", word, "中")
	}
}

func TestTrie_MatchChineseEnglishMixed(t *testing.T) {
	// Chinese + ASCII English in the same word.
	tr := newTrie()
	tr.Insert("qq", CategoryIllegal)
	tr.Insert("黄色", CategoryPorn)
	cat, word, hit := tr.Match("联系方式 qq 12345 内容黄色")
	if !hit {
		t.Fatal("expected hit")
	}
	// "qq" appears first in the text.
	if word != "qq" {
		t.Errorf("word = %q, want %q (earlier start position wins)", word, "qq")
	}
	if cat != CategoryIllegal {
		t.Errorf("category = %q, want %q", cat, CategoryIllegal)
	}
}

// ---- Trie.Categories / Words ---------------------------------------

func TestTrie_Categories(t *testing.T) {
	tr := newTrie()
	tr.Insert("foo", CategoryPorn)
	tr.Insert("bar", CategoryViolence)
	tr.Insert("baz", CategoryPorn)
	cats := tr.Categories()
	if len(cats) != 2 {
		t.Errorf("Categories() returned %d entries, want 2 (porn, violence): %v", len(cats), cats)
	}
}

func TestTrie_Words(t *testing.T) {
	tr := newTrie()
	tr.Insert("foo", CategoryPorn)
	tr.Insert("bar", CategoryViolence)
	tr.Insert("中", CategoryPolitics)
	words := tr.Words()
	if len(words) != 3 {
		t.Errorf("Words() returned %d, want 3: %v", len(words), words)
	}
}

// ---- Cross-check: trie vs the old behavior in sensitive_test.go ----

func TestTrie_BehavesLikeStringsContains(t *testing.T) {
	// Re-implement the old check (strings.Contains over each word)
	// inline and confirm the trie returns the same category for a
	// battery of inputs.
	const testCat Category = CategoryPorn
	words := []string{"裸聊", "色情", "黄图", "招嫖", "一夜情"}
	tr := newTrie()
	oldWords := map[Category][]string{testCat: words}
	tr2 := newTrie()
	for _, w := range words {
		tr.Insert(w, testCat)
		tr2.Insert(w, testCat)
	}
	_ = oldWords
	_ = tr2

	cases := []string{
		"今天想裸聊 见面",
		"我发的色情图片",
		"加微信黄图",
		"网上招嫖 怎么收费",
		"想找一夜情 今晚",
		"完全不相关的正常对话",
		"",
		"x",
	}
	for _, in := range cases {
		// Old behavior: walk each word, substring match; first hit wins.
		oldHit := false
		for _, w := range words {
			if strings.Contains(in, w) {
				oldHit = true
				break
			}
		}
		// Trie: Match.
		_, _, newHit := tr.Match(strings.ToLower(in))
		if oldHit != newHit {
			t.Errorf("disagreement on %q: old=%v trie=%v", in, oldHit, newHit)
		}
	}
}
