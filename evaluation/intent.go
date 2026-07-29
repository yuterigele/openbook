// Package evaluation provides reproducible, offline evaluation helpers for
// Agent routing behavior. It deliberately does not call a business tool.
package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/yuterigele/openbook/intent"
)

// IntentCase is one versioned intent-routing evaluation example.
type IntentCase struct {
	ID       string        `json:"id"`
	Input    string        `json:"input"`
	Expected intent.Intent `json:"expected"`
	Category string        `json:"category"`
	Critical bool          `json:"critical,omitempty"`
}

// IntentSuite is a named, versioned collection of evaluation examples.
type IntentSuite struct {
	Version string       `json:"version"`
	Cases   []IntentCase `json:"cases"`
}

// LoadIntentSuite decodes a JSON evaluation suite and validates its minimum
// invariants so malformed data cannot silently skew a score.
func LoadIntentSuite(r io.Reader) (IntentSuite, error) {
	var suite IntentSuite
	if err := json.NewDecoder(r).Decode(&suite); err != nil {
		return IntentSuite{}, fmt.Errorf("decode intent suite: %w", err)
	}
	if suite.Version == "" {
		return IntentSuite{}, fmt.Errorf("intent suite version is required")
	}
	if len(suite.Cases) == 0 {
		return IntentSuite{}, fmt.Errorf("intent suite must contain at least one case")
	}
	seen := make(map[string]struct{}, len(suite.Cases))
	for _, c := range suite.Cases {
		if c.ID == "" || c.Input == "" || c.Expected == "" || c.Category == "" {
			return IntentSuite{}, fmt.Errorf("intent suite case requires id, input, expected, and category")
		}
		if _, ok := seen[c.ID]; ok {
			return IntentSuite{}, fmt.Errorf("duplicate intent suite case id %q", c.ID)
		}
		seen[c.ID] = struct{}{}
	}
	return suite, nil
}

// IntentFailure records one incorrect route for regression review.
type IntentFailure struct {
	ID       string        `json:"id"`
	Input    string        `json:"input"`
	Category string        `json:"category"`
	Expected intent.Intent `json:"expected"`
	Actual   intent.Intent `json:"actual"`
	Source   string        `json:"source"`
	Critical bool          `json:"critical"`
}

// IntentScore is a machine-readable routing evaluation result.
type IntentScore struct {
	SuiteVersion     string          `json:"suite_version"`
	Strategy         string          `json:"strategy,omitempty"`
	Model            string          `json:"model,omitempty"`
	Total            int             `json:"total"`
	Correct          int             `json:"correct"`
	Accuracy         float64         `json:"accuracy"`
	CriticalTotal    int             `json:"critical_total"`
	CriticalCorrect  int             `json:"critical_correct"`
	CriticalAccuracy float64         `json:"critical_accuracy"`
	ByCategory       map[string]Rate `json:"by_category"`
	BySource         map[string]Rate `json:"by_source"`
	Failures         []IntentFailure `json:"failures"`
	DurationMillis   int64           `json:"duration_ms"`
}

// Rate is a compact per-slice score.
type Rate struct {
	Total    int     `json:"total"`
	Correct  int     `json:"correct"`
	Accuracy float64 `json:"accuracy"`
}

// EvaluateIntent runs every case through classifier. The caller controls
// whether classifier is keyword-only or has an injected lightweight model.
func EvaluateIntent(ctx context.Context, suite IntentSuite, classifier *intent.Classifier) IntentScore {
	score := IntentScore{
		SuiteVersion: suite.Version,
		Total:        len(suite.Cases),
		ByCategory:   make(map[string]Rate),
		BySource:     make(map[string]Rate),
	}
	started := time.Now()
	for _, c := range suite.Cases {
		result := classifier.Classify(ctx, c.Input)
		actual := result.Intent
		// The keyword-only classifier uses an empty intent to signal “no
		// match”. At the routing boundary that is an unknown intent, so
		// normalize it here instead of penalizing safe abstention.
		if actual == "" {
			actual = intent.IntentUnknown
		}
		correct := actual == c.Expected
		if correct {
			score.Correct++
		}
		if c.Critical {
			score.CriticalTotal++
			if correct {
				score.CriticalCorrect++
			}
		}
		rate := score.ByCategory[c.Category]
		rate.Total++
		if correct {
			rate.Correct++
		}
		score.ByCategory[c.Category] = rate
		sourceRate := score.BySource[result.Source]
		sourceRate.Total++
		if correct {
			sourceRate.Correct++
		}
		score.BySource[result.Source] = sourceRate
		if !correct {
			score.Failures = append(score.Failures, IntentFailure{
				ID: c.ID, Input: c.Input, Category: c.Category,
				Expected: c.Expected, Actual: actual,
				Source: result.Source, Critical: c.Critical,
			})
		}
	}
	score.Accuracy = percentage(score.Correct, score.Total)
	score.CriticalAccuracy = percentage(score.CriticalCorrect, score.CriticalTotal)
	for category, rate := range score.ByCategory {
		rate.Accuracy = percentage(rate.Correct, rate.Total)
		score.ByCategory[category] = rate
	}
	for source, rate := range score.BySource {
		rate.Accuracy = percentage(rate.Correct, rate.Total)
		score.BySource[source] = rate
	}
	score.DurationMillis = time.Since(started).Milliseconds()
	sort.Slice(score.Failures, func(i, j int) bool { return score.Failures[i].ID < score.Failures[j].ID })
	return score
}

func percentage(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total)
}
