// Package sensitive provides input content filtering.
//
// Two-layer design (matches the resume's "关键词 + LLM 兜底双保险"):
//
//	Layer 1 — keyword match: substring against per-category word lists.
//	          Cheap, deterministic, <1ms, catches the obvious cases
//	          (51,345 production words across 6 categories).
//	          ALWAYS runs.
//
//	Layer 2 — LLM fallback: when Layer 1 has no hit AND a chat model
//	          has been wired in via WithLLMClassify, ask a small LLM
//	          call to judge the text semantically. This catches
//	          paraphrased / slang / role-play attacks the keyword
//	          list misses. Optional (off by default; costs ~150+30
//	          tokens per fallback call).
//
// The keyword layer is the safety floor; the LLM layer only ADDS
// detection, never relaxes it. If the LLM errors out, CheckCtx fails
// open (text passes) so a degraded LLM does not block legitimate
// users.
package sensitive

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

type Category string

const (
	CategoryPolitics Category = "politics"
	CategoryPorn     Category = "porn"
	CategoryViolence Category = "violence"
	CategoryAd       Category = "ad"
	CategoryAbuse    Category = "abuse"
	CategoryIllegal  Category = "illegal"
	CategoryOther    Category = "other"
)

// Source tags which layer produced a Blocked=true result. Empty when
// the text was not blocked at all.
const (
	SourceKeyword = "keyword"
	SourceLLM     = "llm"
)

// LLM layer tunables. LLMMinLength skips empty / "hi" / "ok" so we
// don't pay the LLM cost on every short acknowledgement. LLMConfidence
// is the minimum model-reported confidence to escalate to Blocked.
const (
	LLMMinLength          = 4
	LLMConfidenceThreshold = 0.6
)

type Result struct {
	Blocked  bool
	Category Category
	Word     string
	Reason   string
	Source   string // "keyword" / "llm" / ""
}

var (
	checkerMu   sync.RWMutex
	checker     = defaultChecker()
	llmFallback LLMClassifyFunc
)

// defaultWords uses placeholders. Real word lists are added via
// RegisterWords at startup. See sensitive_words_zh.go for the
// production lists.
var defaultWords = map[Category][]string{
	CategoryPolitics: {},
	CategoryPorn:     {},
	CategoryViolence: {},
	CategoryAd:       {},
	CategoryAbuse:    {},
	CategoryIllegal:  {},
}

type Checker struct {
	mu    sync.RWMutex
	words map[Category][]string
}

type TakeFirst bool

func defaultChecker() *Checker {
	return &Checker{words: cloneWordMap(defaultWords)}
}

func cloneWordMap(src map[Category][]string) map[Category][]string {
	dst := make(map[Category][]string, len(src))
	for k, v := range src {
		dst[k] = append([]string(nil), v...)
	}
	return dst
}

// Check tests text against the active word list. It is equivalent to
// CheckCtx with a background context — it is kept for backward compat
// and for the keyword-only call sites that don't need to plumb a
// context (e.g. tests, the cmd/sensitive-gen generator).
func Check(text string) Result {
	return CheckCtx(context.Background(), text)
}

// CheckCtx runs Layer 1 (keyword) first; on miss it runs Layer 2
// (LLM fallback) if a fallback has been wired in via WithLLMClassify.
// The LLM call is short-circuited on short inputs and on LLM errors
// (fail-open).
//
// Observability: every branch updates DefaultMetrics (counters +
// per-category) and emits a structured log line so an operator can
// trace any single block back to its source without scraping.
func CheckCtx(ctx context.Context, text string) Result {
	if text == "" {
		return Result{Blocked: false}
	}
	checkerMu.RLock()
	c := checker
	llm := llmFallback
	checkerMu.RUnlock()

	// Layer 1: keyword fast path. Always runs.
	if r := c.check(text); r.Blocked {
		r.Source = SourceKeyword
		DefaultMetrics.observe(r)
		// Structured log: keeps an audit trail of what got blocked and
		// which word triggered. The text itself is NOT logged (PII
		// concerns: customer messages may contain names, phones, etc).
		log.Printf("[sensitive] block source=keyword category=%q word=%q len=%d",
			r.Category, r.Word, len([]rune(text)))
		return r
	}

	// No LLM fallback wired in → done.
	if llm == nil {
		DefaultMetrics.Passes.Add(1)
		return Result{Blocked: false}
	}
	// Short inputs ("hi" / "ok" / "1") don't pay the LLM cost.
	if len([]rune(text)) < LLMMinLength {
		DefaultMetrics.SkippedShort.Add(1)
		return Result{Blocked: false}
	}

	// Layer 2: LLM semantic fallback. Time the call so we can spot a
	// degraded provider (latency creep is often the first sign).
	start := time.Now()
	cat, blocked, conf, err := llm(ctx, text)
	latencyUs := time.Since(start).Microseconds()
	DefaultMetrics.observeLLMLatency(latencyUs)

	if err != nil {
		// Fail-open: a degraded LLM must not block legitimate users.
		// The keyword layer already covered the high-confidence hits.
		DefaultMetrics.LLMErrored.Add(1)
		log.Printf("[sensitive] LLM 兜底 fail-open: %v (latency=%dus)", err, latencyUs)
		return Result{Blocked: false}
	}
	if !blocked {
		DefaultMetrics.Passes.Add(1)
		return Result{Blocked: false}
	}
	if conf < LLMConfidenceThreshold {
		// Model is unsure; default to letting it through.
		DefaultMetrics.LLMLowConf.Add(1)
		log.Printf("[sensitive] LLM 低置信放行 cat=%q conf=%.2f (latency=%dus)",
			cat, conf, latencyUs)
		return Result{Blocked: false}
	}
	r := Result{
		Blocked:  true,
		Category: cat,
		Reason:   reasonFor(cat) + " (LLM)",
		Source:   SourceLLM,
	}
	DefaultMetrics.observe(r)
	log.Printf("[sensitive] block source=llm category=%q conf=%.2f (latency=%dus)",
		cat, conf, latencyUs)
	return r
}

func (c *Checker) check(text string) Result {
	if text == "" {
		return Result{Blocked: false}
	}
	lower := strings.ToLower(text)
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Iterate over the priority order first (high-risk categories checked
	// before lower-risk ones), then any custom categories added at runtime
	// (e.g., tests or business-specific lists).
	ordered := []Category{
		CategoryViolence, CategoryIllegal, CategoryAbuse,
		CategoryPorn, CategoryAd, CategoryPolitics,
	}
	seen := map[Category]bool{}
	for _, cat := range ordered {
		seen[cat] = true
		for _, w := range c.words[cat] {
			if w == "" {
				continue
			}
			if strings.Contains(lower, strings.ToLower(w)) {
				return Result{Blocked: true, Category: cat, Word: w, Reason: reasonFor(cat)}
			}
		}
	}
	// Walk any extra categories registered at runtime.
	for cat, words := range c.words {
		if seen[cat] {
			continue
		}
		for _, w := range words {
			if w == "" {
				continue
			}
			if strings.Contains(lower, strings.ToLower(w)) {
				return Result{Blocked: true, Category: cat, Word: w, Reason: reasonFor(cat)}
			}
		}
	}
	return Result{Blocked: false}
}

func reasonFor(cat Category) string {
	switch cat {
	case CategoryPolitics:
		return "Sorry, the message touches a sensitive topic I cannot help with."
	case CategoryPorn:
		return "Sorry, the message contains content I cannot respond to."
	case CategoryViolence:
		return "Sorry, the message involves dangerous content I cannot help with."
	case CategoryAd:
		return "Sorry, I only handle hair-salon appointments."
	case CategoryAbuse:
		return "Please keep the conversation civil, I will do my best to help."
	case CategoryIllegal:
		return "Sorry, the message involves content I cannot assist with."
	default:
		return "Sorry, the message is not something I can respond to."
	}
}

// RegisterWords replaces the word list for a category.
func RegisterWords(cat Category, words []string) {
	checkerMu.Lock()
	defer checkerMu.Unlock()
	checker.words[cat] = append([]string(nil), words...)
}

// AddWords appends words to an existing category.
func AddWords(cat Category, words []string) {
	checkerMu.Lock()
	defer checkerMu.Unlock()
	checker.words[cat] = append(checker.words[cat], words...)
}

// Reset reloads the default word list (for tests).
func Reset() {
	checkerMu.Lock()
	defer checkerMu.Unlock()
	checker = defaultChecker()
}

// Categories lists currently registered categories.
func Categories() []Category {
	checkerMu.RLock()
	defer checkerMu.RUnlock()
	out := make([]Category, 0, len(checker.words))
	for c := range checker.words {
		out = append(out, c)
	}
	return out
}

// WithLLMClassify wires in the LLM fallback for Layer 2 of CheckCtx.
// Pass nil to disable the LLM layer. Thread-safe.
//
// Production wiring is done in main.go under the SENSITIVE_LLM_FALLBACK
// env var; tests call this directly to inject stubs.
func WithLLMClassify(fn LLMClassifyFunc) {
	checkerMu.Lock()
	defer checkerMu.Unlock()
	llmFallback = fn
}