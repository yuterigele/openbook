// Package sensitive provides input content filtering.
package sensitive

import (
	"strings"
	"sync"
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

type Result struct {
	Blocked  bool
	Category Category
	Word     string
	Reason   string
}

var (
	checkerMu sync.RWMutex
	checker   = defaultChecker()
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

// Check tests text against the active word list.
func Check(text string) Result {
	checkerMu.RLock()
	c := checker
	checkerMu.RUnlock()
	return c.check(text)
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