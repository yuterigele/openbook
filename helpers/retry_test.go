package helpers

import (
	"testing"

	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/schema"
)

func TestApplyMessageModelRetry_BoundsRetriesPerProvider(t *testing.T) {
	cfg := &deep.TypedConfig[*schema.Message]{}
	ApplyMessageModelRetry(cfg)
	if cfg.ModelRetryConfig == nil {
		t.Fatal("ModelRetryConfig should be configured")
	}
	if cfg.ModelRetryConfig.MaxRetries != maxModelRetriesPerProvider {
		t.Fatalf("MaxRetries = %d, want %d", cfg.ModelRetryConfig.MaxRetries, maxModelRetriesPerProvider)
	}
}
