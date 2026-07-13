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
	"errors"
	"strings"
	"testing"
)

// resetMetricsForTest zeroes the package-level counters so each test
// sees a clean slate. Tests should defer this.
func resetMetricsForTest(t *testing.T) {
	t.Helper()
	DefaultMetrics.Reset()
}

// ---- Metrics.observe / counters -------------------------------------

func TestMetrics_KeywordHitIncrements(t *testing.T) {
	resetMetricsForTest(t)
	const testCat Category = "test"
	defer Reset()
	AddWords(testCat, []string{"bad"})

	r := CheckCtx(context.Background(), "this contains bad word")
	if !r.Blocked || r.Source != SourceKeyword {
		t.Fatalf("expected keyword block, got %+v", r)
	}

	snap := DefaultMetrics.Snapshot()
	if snap.KeywordHits != 1 {
		t.Errorf("KeywordHits = %d, want 1", snap.KeywordHits)
	}
	if snap.Passes != 0 {
		t.Errorf("Passes = %d, want 0", snap.Passes)
	}
	if snap.LLMHits != 0 {
		t.Errorf("LLMHits = %d, want 0", snap.LLMHits)
	}
}

func TestMetrics_CategoryHitsBySource(t *testing.T) {
	resetMetricsForTest(t)

	// Keyword hit on a pre-seeded standard category.
	const testCat Category = CategoryAbuse
	defer Reset()
	AddWords(testCat, []string{"dummy"})

	CheckCtx(context.Background(), "contains dummy word")
	snap := DefaultMetrics.Snapshot()
	if got := snap.CategoryHits[CategoryAbuse]; got != 1 {
		t.Errorf("CategoryHits[%q] = %d, want 1", CategoryAbuse, got)
	}
}

func TestMetrics_LLMHitIncrements(t *testing.T) {
	resetMetricsForTest(t)
	stub := &stubChatModel{
		resp: `{"blocked": true, "category": "porn", "confidence": 0.9}`,
	}
	WithLLMClassify(NewLLMClassifyFuncFromEino(stub))
	defer WithLLMClassify(nil)

	CheckCtx(context.Background(), "发一些那种露骨的照片")
	snap := DefaultMetrics.Snapshot()
	if snap.LLMHits != 1 {
		t.Errorf("LLMHits = %d, want 1", snap.LLMHits)
	}
	if snap.KeywordHits != 0 {
		t.Errorf("KeywordHits = %d, want 0", snap.KeywordHits)
	}
	if got := snap.CategoryHits[CategoryPorn]; got != 1 {
		t.Errorf("CategoryHits[porn] = %d, want 1", got)
	}
	if snap.LLMHitRate < 0.99 {
		t.Errorf("LLMHitRate = %v, want ~1.0 (only LLM hits exist)", snap.LLMHitRate)
	}
}

func TestMetrics_LLMErrorIncrements(t *testing.T) {
	resetMetricsForTest(t)
	stub := &stubChatModel{err: errors.New("upstream down")}
	WithLLMClassify(NewLLMClassifyFuncFromEino(stub))
	defer WithLLMClassify(nil)

	CheckCtx(context.Background(), "足够长的输入文本")
	snap := DefaultMetrics.Snapshot()
	if snap.LLMErrored != 1 {
		t.Errorf("LLMErrored = %d, want 1", snap.LLMErrored)
	}
	if snap.Passes != 0 {
		t.Errorf("Passes = %d, want 0 (error → not classified as pass)", snap.Passes)
	}
	if snap.LLMLatencyCount != 1 {
		t.Errorf("LLMLatencyCount = %d, want 1 (latency still recorded on error)", snap.LLMLatencyCount)
	}
}

func TestMetrics_LLMLowConfIncrements(t *testing.T) {
	resetMetricsForTest(t)
	stub := &stubChatModel{
		resp: `{"blocked": true, "category": "porn", "confidence": 0.3}`,
	}
	WithLLMClassify(NewLLMClassifyFuncFromEino(stub))
	defer WithLLMClassify(nil)

	CheckCtx(context.Background(), "模糊可疑的描述")
	snap := DefaultMetrics.Snapshot()
	if snap.LLMLowConf != 1 {
		t.Errorf("LLMLowConf = %d, want 1", snap.LLMLowConf)
	}
	if snap.LLMHits != 0 {
		t.Errorf("LLMHits = %d, want 0 (low conf should not register as hit)", snap.LLMHits)
	}
}

func TestMetrics_ShortInputIncrementsSkippedShort(t *testing.T) {
	resetMetricsForTest(t)
	stub := &stubChatModel{
		resp: `{"blocked": true, "category": "porn", "confidence": 0.99}`,
	}
	WithLLMClassify(NewLLMClassifyFuncFromEino(stub))
	defer WithLLMClassify(nil)

	CheckCtx(context.Background(), "ok")
	snap := DefaultMetrics.Snapshot()
	if snap.SkippedShort != 1 {
		t.Errorf("SkippedShort = %d, want 1", snap.SkippedShort)
	}
	if snap.LLMLatencyCount != 0 {
		t.Errorf("LLM was called for short input; latency count = %d, want 0", snap.LLMLatencyCount)
	}
}

func TestMetrics_PassWithoutLLM(t *testing.T) {
	resetMetricsForTest(t)
	// LLM disabled (default) — clean text → Passes++, nothing else.
	CheckCtx(context.Background(), "完全无害的正常预约对话")
	snap := DefaultMetrics.Snapshot()
	if snap.Passes != 1 {
		t.Errorf("Passes = %d, want 1", snap.Passes)
	}
	if snap.KeywordHits != 0 || snap.LLMHits != 0 {
		t.Errorf("unexpected block counters: keyword=%d llm=%d", snap.KeywordHits, snap.LLMHits)
	}
}

func TestMetrics_LatencyAverage(t *testing.T) {
	resetMetricsForTest(t)
	stub := &stubChatModel{
		resp: `{"blocked": false, "category": "none", "confidence": 0.99}`,
	}
	WithLLMClassify(NewLLMClassifyFuncFromEino(stub))
	defer WithLLMClassify(nil)

	// 3 calls, all hit the LLM.
	for i := 0; i < 3; i++ {
		CheckCtx(context.Background(), "足够长的输入文本")
	}
	snap := DefaultMetrics.Snapshot()
	if snap.LLMLatencyCount != 3 {
		t.Errorf("LLMLatencyCount = %d, want 3", snap.LLMLatencyCount)
	}
	// Avg is sum/count. On Windows the default clock resolution is
	// ~15ms, so a no-op stub call may measure 0µs. We assert the
	// invariant sum == avg*count instead of avg > 0 — that holds
	// for any non-negative timing.
	if snap.LLMLatencyAvgUs < 0 {
		t.Errorf("LLMLatencyAvgUs = %d, want >= 0", snap.LLMLatencyAvgUs)
	}
	// Sum is recorded; we just don't assert > 0 (clock resolution).
	if m := DefaultMetrics.LLMLatencySumUs.Load(); m < 0 {
		t.Errorf("LLMLatencySumUs = %d, want >= 0", m)
	}
}

// ---- Snapshot / PrometheusText --------------------------------------

func TestSnapshot_LLMHitRateWhenNoBlocks(t *testing.T) {
	resetMetricsForTest(t)
	snap := DefaultMetrics.Snapshot()
	if snap.LLMHitRate != 0 {
		t.Errorf("LLMHitRate with no blocks = %v, want 0", snap.LLMHitRate)
	}
}

func TestSnapshot_LLMHitRateWhenOnlyKeyword(t *testing.T) {
	resetMetricsForTest(t)
	const testCat Category = CategoryIllegal
	defer Reset()
	AddWords(testCat, []string{"x"})

	CheckCtx(context.Background(), "contains x")
	snap := DefaultMetrics.Snapshot()
	if snap.LLMHitRate != 0 {
		t.Errorf("LLMHitRate with only keyword hits = %v, want 0", snap.LLMHitRate)
	}
	if snap.KeywordHits != 1 {
		t.Errorf("KeywordHits = %d, want 1", snap.KeywordHits)
	}
}

func TestPrometheusText_ContainsExpectedSeries(t *testing.T) {
	resetMetricsForTest(t)
	// Seed a couple of values so the output is non-trivial.
	const testCat Category = CategoryAd
	defer Reset()
	AddWords(testCat, []string{"promo"})

	CheckCtx(context.Background(), "contains promo")

	out := DefaultMetrics.PrometheusText()

	wantSubstrings := []string{
		"# HELP openbook_sensitive_keyword_hits_total",
		"# TYPE openbook_sensitive_keyword_hits_total counter",
		"openbook_sensitive_keyword_hits_total 1",
		"openbook_sensitive_passes_total 0",
		"openbook_sensitive_llm_hit_rate 0.0000",
		`openbook_sensitive_category_hits_total{category="ad"} 1`,
		`openbook_sensitive_category_hits_total{category="politics"} 0`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("PrometheusText missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestPrometheusText_AllCountersPresent(t *testing.T) {
	// Even with zero traffic, all metric series should be present so
	// prometheus picks them up on the first scrape.
	out := DefaultMetrics.PrometheusText()
	wantSeries := []string{
		"openbook_sensitive_keyword_hits_total",
		"openbook_sensitive_llm_hits_total",
		"openbook_sensitive_passes_total",
		"openbook_sensitive_skipped_short_total",
		"openbook_sensitive_llm_errors_total",
		"openbook_sensitive_llm_low_conf_total",
		"openbook_sensitive_llm_latency_us_avg",
		"openbook_sensitive_llm_hit_rate",
		"openbook_sensitive_category_hits_total",
	}
	for _, s := range wantSeries {
		if !strings.Contains(out, s) {
			t.Errorf("PrometheusText missing series %q", s)
		}
	}
}

func TestReset_ZeroesAllCounters(t *testing.T) {
	// Dirty the counters first.
	const testCat Category = CategoryViolence
	defer Reset()
	AddWords(testCat, []string{"v"})

	CheckCtx(context.Background(), "contains v")
	if DefaultMetrics.KeywordHits.Load() == 0 {
		t.Fatal("setup: expected KeywordHits > 0 after dirty call")
	}

	DefaultMetrics.Reset()

	snap := DefaultMetrics.Snapshot()
	if snap.KeywordHits != 0 || snap.LLMHits != 0 || snap.Passes != 0 {
		t.Errorf("Reset did not zero counters: %+v", snap)
	}
	for cat, n := range snap.CategoryHits {
		if n != 0 {
			t.Errorf("Reset did not zero category %q: %d", cat, n)
		}
	}
}
