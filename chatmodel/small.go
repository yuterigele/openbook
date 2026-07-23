package chatmodel

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"
)

// NewSmallClassifierModel creates the optional low-cost classifier model.
// It deliberately has no tool access: it may classify text only and must not
// make an authorization or business decision. An empty SMALL_MODEL_API_KEY
// disables it so callers can safely retain their deterministic fallback.
//
// Qwen is called through Alibaba Cloud Model Studio's OpenAI-compatible API.
// Providers with a compatible endpoint can be substituted through the
// SMALL_MODEL_* settings.
func NewSmallClassifierModel(ctx context.Context) (einomodel.ToolCallingChatModel, error) {
	if os.Getenv("SMALL_MODEL_ENABLED") != "1" {
		return nil, nil
	}
	apiKey := strings.TrimSpace(os.Getenv("SMALL_MODEL_API_KEY"))
	if apiKey == "" {
		return nil, fmt.Errorf("SMALL_MODEL_ENABLED=1 but SMALL_MODEL_API_KEY is empty")
	}
	baseURL := strings.TrimSpace(os.Getenv("SMALL_MODEL_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	}
	model := strings.TrimSpace(os.Getenv("SMALL_MODEL_NAME"))
	if model == "" {
		model = "qwen-flash"
	}
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey: apiKey, BaseURL: baseURL, Model: model,
	})
}
