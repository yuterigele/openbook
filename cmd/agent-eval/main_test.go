package main

import (
	"context"
	"testing"

	"github.com/yuterigele/openbook/intent"
)

func TestBuildClassifierKeyword(t *testing.T) {
	classifier, model := buildClassifier(context.Background(), "keyword")
	if model != "" {
		t.Fatalf("model = %q, want empty keyword baseline", model)
	}
	if got := classifier.Classify(context.Background(), "我要预约").Intent; got != intent.IntentBook {
		t.Fatalf("intent = %q, want %q", got, intent.IntentBook)
	}
}
