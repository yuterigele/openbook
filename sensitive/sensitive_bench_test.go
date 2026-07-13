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
	"testing"
)

// benchText is a realistic ~50-char Chinese customer-service message.
// Same shape as the actual chat traffic; the trie must handle it
// cheaply.
const benchText = "你好我想预约明天下午三点的剪发Tony 师傅大概多少钱"

// benchMissText is a clean ~40-char text guaranteed not to contain
// any sensitive word. Used for the "worst case" (every rune position
// explored) benchmark.
const benchMissText = "今天天气不错适合出门散步我打算去公园看看风景买杯咖啡"

func BenchmarkCheck_Hit(b *testing.B) {
	// Uses the production trie (loaded by the package init via
	// LoadProductionWords in init tests, or by main.go at startup).
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Check(benchText)
	}
}

func BenchmarkCheck_Miss(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Check(benchMissText)
	}
}

func BenchmarkTrie_Insert(b *testing.B) {
	// Snapshot the current words map.
	checkerMu.RLock()
	src := make(map[Category][]string, len(checker.words))
	for cat, ws := range checker.words {
		src[cat] = append([]string(nil), ws...)
	}
	checkerMu.RUnlock()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr := newTrie()
		for cat, ws := range src {
			for _, w := range ws {
				tr.Insert(w, cat)
			}
		}
	}
}
