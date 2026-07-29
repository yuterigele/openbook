package evaluation

import (
	"context"
	"strings"
	"testing"

	"github.com/yuterigele/openbook/intent"
)

func TestLoadIntentSuiteRejectsDuplicateIDs(t *testing.T) {
	_, err := LoadIntentSuite(strings.NewReader(`{"version":"v1","cases":[{"id":"a","input":"预约","expected":"book","category":"booking"},{"id":"a","input":"取消","expected":"cancel","category":"booking"}]}`))
	if err == nil {
		t.Fatal("expected duplicate ID error")
	}
}

func TestEvaluateIntentReportsCategoryAndCriticalScores(t *testing.T) {
	suite := IntentSuite{Version: "test", Cases: []IntentCase{
		{ID: "book", Input: "我要预约", Expected: intent.IntentBook, Category: "booking", Critical: true},
		{ID: "unknown", Input: "量子纠缠", Expected: intent.IntentUnknown, Category: "out_of_scope"},
	}}
	score := EvaluateIntent(context.Background(), suite, intent.NewClassifier())
	if score.Correct != 2 || score.Accuracy != 1 {
		t.Fatalf("score = %+v, want two correct cases", score)
	}
	if score.CriticalAccuracy != 1 || score.ByCategory["booking"].Accuracy != 1 {
		t.Fatalf("unexpected critical/category score: %+v", score)
	}
	if score.BySource["keyword"].Accuracy != 1 {
		t.Fatalf("unexpected source score: %+v", score.BySource)
	}
}
