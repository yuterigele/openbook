// agent-eval evaluates a versioned routing suite without invoking appointment
// tools or requiring MySQL, Redis, or a paid model.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/evaluation"
	"github.com/yuterigele/openbook/intent"
)

func main() {
	// Match the application entrypoint so a local .env can configure an
	// explicitly requested lightweight-model evaluation. LoadEnv never prints
	// or overrides shell-provided credentials.
	chatmodel.LoadEnv()

	suitePath := flag.String("suite", "docs/evals/intent-v1.json", "path to intent evaluation suite JSON")
	jsonOutput := flag.Bool("json", false, "emit machine-readable JSON")
	strategy := flag.String("strategy", "keyword", "classifier strategy: keyword or small")
	timeout := flag.Duration("timeout", 2*time.Minute, "total evaluation timeout")
	minAccuracy := flag.Float64("min-accuracy", -1, "minimum overall accuracy in [0,1]; negative disables the gate")
	minCriticalAccuracy := flag.Float64("min-critical-accuracy", -1, "minimum critical-case accuracy in [0,1]; negative disables the gate")
	flag.Parse()

	f, err := os.Open(*suitePath)
	if err != nil {
		fatalf("open suite: %v", err)
	}
	defer f.Close()
	suite, err := evaluation.LoadIntentSuite(f)
	if err != nil {
		fatalf("load suite: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	classifier, modelName := buildClassifier(ctx, *strategy)
	score := evaluation.EvaluateIntent(ctx, suite, classifier)
	score.Strategy = strings.ToLower(strings.TrimSpace(*strategy))
	score.Model = modelName
	if *jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(score); err != nil {
			fatalf("encode score: %v", err)
		}
	} else {
		fmt.Printf("suite=%s strategy=%s model=%s total=%d correct=%d accuracy=%.1f%% critical=%.1f%% failures=%d duration=%dms\n",
			score.SuiteVersion, score.Strategy, displayModel(score.Model), score.Total, score.Correct,
			score.Accuracy*100, score.CriticalAccuracy*100, len(score.Failures), score.DurationMillis)
		categories := make([]string, 0, len(score.ByCategory))
		for category := range score.ByCategory {
			categories = append(categories, category)
		}
		sort.Strings(categories)
		for _, category := range categories {
			rate := score.ByCategory[category]
			fmt.Printf("  %s: %d/%d (%.1f%%)\n", category, rate.Correct, rate.Total, rate.Accuracy*100)
		}
	}
	if err := evaluation.CheckIntentThresholds(score, *minAccuracy, *minCriticalAccuracy); err != nil {
		fatalf("quality gate: %v", err)
	}
}

func buildClassifier(ctx context.Context, strategy string) (*intent.Classifier, string) {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "keyword":
		return intent.NewClassifier(), ""
	case "small":
		small, err := chatmodel.NewSmallClassifierModel(ctx)
		if err != nil {
			fatalf("initialize small classifier: %v", err)
		}
		if small == nil {
			fatalf("small strategy requires SMALL_MODEL_ENABLED=1 and SMALL_MODEL_API_KEY")
		}
		return intent.NewClassifier().WithLLMClassify(intent.NewLLMClassifyFuncFromEino(small)), envOr("SMALL_MODEL_NAME", "qwen-flash")
	default:
		fatalf("unsupported strategy %q (want keyword or small)", strategy)
		return nil, "" // unreachable; keeps the compiler aware fatalf exits.
	}
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func displayModel(model string) string {
	if model == "" {
		return "-"
	}
	return model
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "agent-eval: "+format+"\n", args...)
	os.Exit(1)
}
