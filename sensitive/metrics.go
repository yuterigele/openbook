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
	"fmt"
	"strings"
	"sync/atomic"
)

// Metrics is the in-process observability counter bundle for the
// sensitive-word filter. All fields are atomic — safe for concurrent
// reads/writes without locks.
//
// This is intentionally a hand-rolled atomic-counter bundle rather
// than a prometheus/client_golang registry. Reasons:
//   - The package only needs 10-ish counters (no histograms, no
//     summaries, no complex label dimensions).
//   - Avoiding the prometheus registry keeps the sensitive package
//     dependency-free (no transitive pull-in of protobuf, expfmt, etc.).
//   - Snapshot() and PrometheusText() are ~30 lines each; we trade a
//     little code for a much smaller dependency surface.
//   - The /metrics endpoint renders the text format inline.
//
// Trade-off: the text format we emit is hand-rolled and is not 100%
// spec-compliant (e.g. no help escaping for special characters). For
// internal-only scraping that's fine; if we ever need to expose this
// to a public prometheus instance, swap to client_golang.
type Metrics struct {
	// Layer-1 keyword hits.
	KeywordHits atomic.Int64
	// Layer-2 LLM-fallback hits (LLM said blocked=true with conf≥threshold).
	LLMHits atomic.Int64
	// Text passed both layers.
	Passes atomic.Int64
	// Input shorter than LLMMinLength — LLM was deliberately skipped.
	SkippedShort atomic.Int64
	// LLM call returned an error → fail-open.
	LLMErrored atomic.Int64
	// LLM returned blocked=true but conf < LLMConfidenceThreshold → pass.
	LLMLowConf atomic.Int64

	// LLM call latency (microseconds). Use sum/count for the avg.
	LLMLatencySumUs atomic.Int64
	LLMLatencyCount atomic.Int64

	// Per-category hit counts (keyword + LLM combined). Pre-seeded
	// for the 7 standard categories so the prometheus output is stable
	// (zeros are visible from the first scrape).
	CategoryHits map[Category]*atomic.Int64
}

// DefaultMetrics is the package-level counter singleton. It is
// exposed at /metrics and reset by tests via Reset().
var DefaultMetrics = newMetrics()

func newMetrics() *Metrics {
	return &Metrics{
		CategoryHits: map[Category]*atomic.Int64{
			CategoryPolitics: {},
			CategoryPorn:     {},
			CategoryViolence: {},
			CategoryAd:       {},
			CategoryAbuse:    {},
			CategoryIllegal:  {},
			CategoryOther:    {},
		},
	}
}

// Reset zeros all counters. Tests use this to isolate; production
// should not call it (counters are cumulative over process lifetime).
func (m *Metrics) Reset() {
	m.KeywordHits.Store(0)
	m.LLMHits.Store(0)
	m.Passes.Store(0)
	m.SkippedShort.Store(0)
	m.LLMErrored.Store(0)
	m.LLMLowConf.Store(0)
	m.LLMLatencySumUs.Store(0)
	m.LLMLatencyCount.Store(0)
	for _, c := range m.CategoryHits {
		c.Store(0)
	}
}

// observe records a CheckCtx outcome. The single switch keeps the
// call site in CheckCtx readable.
func (m *Metrics) observe(r Result) {
	if !r.Blocked {
		if r.Source == "" {
			m.Passes.Add(1)
		}
		return
	}
	switch r.Source {
	case SourceKeyword:
		m.KeywordHits.Add(1)
	case SourceLLM:
		m.LLMHits.Add(1)
	}
	if ctr, ok := m.CategoryHits[r.Category]; ok && ctr != nil {
		ctr.Add(1)
	}
}

// observeLLMLatency records a single LLM call's wall-clock latency.
func (m *Metrics) observeLLMLatency(us int64) {
	m.LLMLatencySumUs.Add(us)
	m.LLMLatencyCount.Add(1)
}

// Snapshot is a per-field-consistent view of the counters. Not
// transactional across fields (counters are independent atomics) but
// every individual field is read atomically, so partial torn values
// are impossible.
type Snapshot struct {
	KeywordHits     int64
	LLMHits         int64
	Passes          int64
	SkippedShort    int64
	LLMErrored      int64
	LLMLowConf      int64
	LLMLatencyAvgUs int64
	LLMLatencyCount int64
	LLMHitRate      float64
	CategoryHits    map[Category]int64
}

func (m *Metrics) Snapshot() Snapshot {
	s := Snapshot{
		KeywordHits:     m.KeywordHits.Load(),
		LLMHits:         m.LLMHits.Load(),
		Passes:          m.Passes.Load(),
		SkippedShort:    m.SkippedShort.Load(),
		LLMErrored:      m.LLMErrored.Load(),
		LLMLowConf:      m.LLMLowConf.Load(),
		LLMLatencyCount: m.LLMLatencyCount.Load(),
		CategoryHits:    make(map[Category]int64, len(m.CategoryHits)),
	}
	cnt := s.LLMLatencyCount
	if cnt > 0 {
		s.LLMLatencyAvgUs = m.LLMLatencySumUs.Load() / cnt
	}
	for cat, c := range m.CategoryHits {
		s.CategoryHits[cat] = c.Load()
	}
	total := s.KeywordHits + s.LLMHits
	if total > 0 {
		s.LLMHitRate = float64(s.LLMHits) / float64(total)
	}
	return s
}

// PrometheusText renders the metrics in Prometheus text exposition
// format (version 0.0.4). Output is suitable for scraping with the
// prometheus server's text parser; it intentionally omits HELP escaping
// for special characters (the category labels are fixed enums).
func (m *Metrics) PrometheusText() string {
	s := m.Snapshot()
	var b strings.Builder

	w := func(name, help string, kind string) {
		fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, kind)
	}

	w("openbook_sensitive_keyword_hits_total",
		"Total sensitive-word blocks produced by the keyword layer (Layer 1).",
		"counter")
	fmt.Fprintf(&b, "openbook_sensitive_keyword_hits_total %d\n", s.KeywordHits)

	w("openbook_sensitive_llm_hits_total",
		"Total sensitive-word blocks produced by the LLM fallback (Layer 2).",
		"counter")
	fmt.Fprintf(&b, "openbook_sensitive_llm_hits_total %d\n", s.LLMHits)

	w("openbook_sensitive_passes_total",
		"Total messages that passed both layers.",
		"counter")
	fmt.Fprintf(&b, "openbook_sensitive_passes_total %d\n", s.Passes)

	w("openbook_sensitive_skipped_short_total",
		"Total messages shorter than LLMMinLength (LLM was deliberately not called).",
		"counter")
	fmt.Fprintf(&b, "openbook_sensitive_skipped_short_total %d\n", s.SkippedShort)

	w("openbook_sensitive_llm_errors_total",
		"Total LLM calls that errored — fail-open, text passed through.",
		"counter")
	fmt.Fprintf(&b, "openbook_sensitive_llm_errors_total %d\n", s.LLMErrored)

	w("openbook_sensitive_llm_low_conf_total",
		"Total LLM blocks rejected because confidence < 0.6.",
		"counter")
	fmt.Fprintf(&b, "openbook_sensitive_llm_low_conf_total %d\n", s.LLMLowConf)

	w("openbook_sensitive_llm_latency_us_avg",
		"Average LLM call latency in microseconds (sum/count).",
		"gauge")
	fmt.Fprintf(&b, "openbook_sensitive_llm_latency_us_avg %d\n", s.LLMLatencyAvgUs)

	w("openbook_sensitive_llm_hit_rate",
		"Share of blocks produced by the LLM layer (LLM hits / total hits). 0 if no blocks yet.",
		"gauge")
	fmt.Fprintf(&b, "openbook_sensitive_llm_hit_rate %.4f\n", s.LLMHitRate)

	w("openbook_sensitive_category_hits_total",
		"Block counts by category (keyword + LLM combined).",
		"counter")
	for cat, n := range s.CategoryHits {
		fmt.Fprintf(&b, "openbook_sensitive_category_hits_total{category=%q} %d\n", string(cat), n)
	}
	return b.String()
}
