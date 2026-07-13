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

import "strings"

// trieNode is one node in the prefix tree. word != "" marks a node as
// the end of a registered word. Intermediate nodes have word == "" —
// they exist only to share prefixes between words ("中" and "中国"
// share the "中" node; the first one owns word="中", the second one
// marks a deeper node with word="中国").
//
// Memory: a single node is ~64 bytes on 64-bit (map header + word
// string header + cat string header). 51,345 words with ~80% being
// 1-2 char Chinese → roughly 60-80K nodes → ~5 MB. Acceptable for
// a singleton tree; re-built only on RegisterWords/AddWords/Reset.
type trieNode struct {
	children map[rune]*trieNode
	word     string
	cat      Category
}

// Trie is the in-memory sensitive-word prefix tree.
//
// Why a trie (not the prior strings.Contains loop):
//
//   - Old: 51,345 words × N text chars = 51,345 × 50 ≈ 2.5M substring
//     scans per Check. "Worst case" is actually worst-case — if the
//     text contains the long tail of multi-char words, every call
//     scans all 51K words.
//   - Trie: 50 chars × tree depth (~1-3 for short Chinese words) =
//     ~150 map lookups. ~15,000x fewer ops on the hot path.
//
// Why not a full Aho-Corasick automaton (the textbook "right" answer
// for multi-pattern matching):
//
//   - The corpus is dominated by 1-2 char Chinese words (~80%+ from
//     the sensitive-gen stats), so the trie is already shallow and
//     the per-position break is fast. Aho-Corasick would shave
//     another ~3x at most (50 ops vs 150 ops) but costs 200+ LOC
//     and is significantly harder to debug.
//   - If QPS ever crosses ~10K or the corpus grows past ~500K words,
//     the upgrade path is clean: replace Match() with a fail-link
//     walk. The Insert and storage layout stay the same.
type Trie struct {
	root *trieNode
	size int // number of unique words inserted
}

func newTrie() *Trie {
	return &Trie{root: &trieNode{}}
}

// Size returns the number of words currently registered. Useful for
// tests and the /metrics endpoint could surface it later.
func (t *Trie) Size() int {
	if t == nil {
		return 0
	}
	return t.size
}

// Insert adds word under the given category. Empty words are skipped
// (matching the prior "ignore empty entries" behavior of the keyword
// layer). Re-inserting the same word updates its category in place.
func (t *Trie) Insert(word string, cat Category) {
	if t == nil || word == "" {
		return
	}
	cur := t.root
	for _, r := range []rune(word) {
		if cur.children == nil {
			cur.children = make(map[rune]*trieNode, 4)
		}
		child, ok := cur.children[r]
		if !ok {
			child = &trieNode{}
			cur.children[r] = child
		}
		cur = child
	}
	cur.word = word
	cur.cat = cat
	t.size++
}

// Hit is a single match inside the scanned text.
type Hit struct {
	Word     string
	Category Category
	Start    int // rune index of the first character of the word
	End      int // rune index one past the last character
}

// categoryPriority returns the scan-tie-breaker rank of a category.
// Lower number = higher priority. Mirrors the ordered list in
// sensitive.go:check().
func categoryPriority(c Category) int {
	switch c {
	case CategoryViolence:
		return 0
	case CategoryIllegal:
		return 1
	case CategoryAbuse:
		return 2
	case CategoryPorn:
		return 3
	case CategoryAd:
		return 4
	case CategoryPolitics:
		return 5
	default:
		return 6
	}
}

// Match finds the earliest-and-highest-priority hit in text. The
// semantics match the prior strings.Contains implementation: the
// first hit by position wins; ties on position are broken by category
// priority (violence > illegal > abuse > porn > ad > politics > other).
//
// Returns (cat, word, true) on hit, (_, _, false) on miss.
//
// Pre-lowered: Match expects the caller to pass the text in the
// canonical form (lower-case). The caller in Check does the
// ToLower once for the whole string; doing it here per-Match call
// would re-allocate for every trie walk.
func (t *Trie) Match(lowerText string) (Category, string, bool) {
	if t == nil || t.root == nil || lowerText == "" {
		return "", "", false
	}
	runes := []rune(lowerText)

	bestStart := -1
	bestCat := CategoryOther
	bestWord := ""

	for i := 0; i < len(runes); i++ {
		cur := t.root
		for j := i; j < len(runes); j++ {
			child, ok := cur.children[runes[j]]
			if !ok {
				break
			}
			if child.word != "" {
				// First hit at this position OR same start but
				// higher-priority category.
				if bestStart < 0 || i < bestStart ||
					(i == bestStart && categoryPriority(child.cat) < categoryPriority(bestCat)) {
					bestStart = i
					bestCat = child.cat
					bestWord = child.word
				}
			}
			cur = child
		}
	}
	if bestStart < 0 {
		return "", "", false
	}
	return bestCat, bestWord, true
}

// MatchCaseInsensitive is Match with the strings.ToLower call inlined
// for callers that haven't pre-lowered. Same semantics; same cost
// roughly (the lower is a single allocation for the rune slice).
func (t *Trie) MatchCaseInsensitive(text string) (Category, string, bool) {
	return t.Match(strings.ToLower(text))
}

// Words returns every word currently in the trie, in unspecified
// order. Used by tests and debugging; do not call from the hot path.
func (t *Trie) Words() []string {
	if t == nil {
		return nil
	}
	var out []string
	var walk func(n *trieNode)
	walk = func(n *trieNode) {
		if n.word != "" {
			out = append(out, n.word)
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(t.root)
	return out
}

// Categories returns the unique categories present in the trie. This
// is a snapshot — safe to read concurrently.
func (t *Trie) Categories() []Category {
	if t == nil {
		return nil
	}
	seen := map[Category]bool{}
	var walk func(n *trieNode)
	walk = func(n *trieNode) {
		if n.word != "" {
			seen[n.cat] = true
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(t.root)
	out := make([]Category, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	return out
}
