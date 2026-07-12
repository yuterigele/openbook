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

package chatmodel

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/agenticark"
	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino-ext/components/model/ark"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	openairesponses "github.com/openai/openai-go/v3/responses"
	arkModel "github.com/volcengine/volcengine-go-sdk/service/arkruntime/model"
)

// Provider is one of the supported LLM providers in the fallback chain.
type Provider string

const (
	ProviderDeepSeek Provider = "deepseek" // OpenAI-compatible, default
	ProviderOpenAI   Provider = "openai"   // OpenAI direct
	ProviderArk      Provider = "ark"      // Volcengine Ark (doubao)
)

// FallbackEntry records which provider was used to back the model after
// init-time fallback. The agent main flow can log it; tests assert it.
type FallbackEntry struct {
	Provider Provider
	Index    int    // position in the chain (0 = primary)
	Err      string // empty when this provider succeeded
	Latency  time.Duration
}

// DefaultFallbackChain is the order to try providers at init time.
//
// Tunable via env: OPENBOOK_LLM_CHAIN = "deepseek,openai,ark"
// (any subset / reorder).
func DefaultFallbackChain() []Provider {
	if v := os.Getenv("OPENBOOK_LLM_CHAIN"); v != "" {
		return parseProviderList(v)
	}
	return []Provider{ProviderDeepSeek, ProviderOpenAI, ProviderArk}
}

func parseProviderList(s string) []Provider {
	parts := strings.Split(s, ",")
	out := make([]Provider, 0, len(parts))
	for _, p := range parts {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "deepseek":
			out = append(out, ProviderDeepSeek)
		case "openai":
			out = append(out, ProviderOpenAI)
		case "ark":
			out = append(out, ProviderArk)
		}
	}
	if len(out) == 0 {
		return DefaultFallbackChain()
	}
	return out
}

// NewModelWithFallback tries each provider in the chain (in order) and
// returns the first one that initializes successfully.
//
// Init-time fallback rationale:
//   - If the primary (DeepSeek) is dead, the agent should NOT start the
//     server with a broken model. Better to start with OpenAI / Ark and
//     log a warning.
//   - Runtime fallback (model going down mid-request) is handled separately
//     via the eino retry config in helpers/retry.go + per-request try-next
//     wrapping. See `tryNextProvider` (planned, not yet implemented).
//
// `chain` is a recorded slice (one entry per provider attempt) so callers
// can log / assert which provider actually answered.
func NewModelWithFallback[M adk.MessageType](ctx context.Context) (m einomodel.BaseModel[M], used Provider, chain []FallbackEntry, err error) {
	providers := DefaultFallbackChain()
	chain = make([]FallbackEntry, 0, len(providers))

	for i, p := range providers {
		start := time.Now()
		mm, buildErr := buildProvider[M](ctx, p)
		latency := time.Since(start)

		entry := FallbackEntry{
			Provider: p,
			Index:    i,
			Latency:  latency,
		}
		if buildErr != nil {
			entry.Err = buildErr.Error()
			chain = append(chain, entry)
			log.Printf("[chatmodel] provider %s (idx %d) init failed in %v: %v — trying next",
				p, i, latency, buildErr)
			continue
		}
		entry.Err = ""
		chain = append(chain, entry)
		if i > 0 {
			log.Printf("[chatmodel] primary provider unavailable, falling back to %s (idx %d, init %v)",
				p, i, latency)
		} else {
			log.Printf("[chatmodel] using primary provider %s (init %v)", p, latency)
		}
		return mm, p, chain, nil
	}

	return nil, "", chain, fmt.Errorf("all LLM providers failed: %s", formatChain(chain))
}

// buildProvider constructs a single provider's model. M selects whether to
// use the chat (regular) or agentic (tool-loop) variant.
func buildProvider[M adk.MessageType](ctx context.Context, p Provider) (einomodel.BaseModel[M], error) {
	var zero M
	isAgentic := false
	if _, ok := any(zero).(*schema.AgenticMessage); ok {
		isAgentic = true
	}

	switch p {
	case ProviderArk:
		if isAgentic {
			timeout := 3 * time.Minute
			am, err := agenticark.New(ctx, &agenticark.Config{
				APIKey:  os.Getenv("ARK_API_KEY"),
				Model:   firstEnv("ARK_MODEL", "ARK_MODEL_ID"),
				BaseURL: os.Getenv("ARK_BASE_URL"),
				Timeout: &timeout,
			})
			if err != nil {
				return nil, err
			}
			return any(am).(einomodel.BaseModel[M]), nil
		}
		cm, err := ark.NewChatModel(ctx, &ark.ChatModelConfig{
			APIKey:  os.Getenv("ARK_API_KEY"),
			Model:   firstEnv("ARK_MODEL", "ARK_MODEL_ID"),
			BaseURL: os.Getenv("ARK_BASE_URL"),
			Thinking: &arkModel.Thinking{
				Type: arkModel.ThinkingTypeDisabled,
			},
		})
		if err != nil {
			return nil, err
		}
		return any(cm).(einomodel.BaseModel[M]), nil
	case ProviderOpenAI, ProviderDeepSeek:
		// DeepSeek is OpenAI-compatible (api.deepseek.com/v1). We just point
		// the OpenAI client at it via OPENAI_BASE_URL.
		// No separate code path needed; the env decides.
		apiKey := os.Getenv("OPENAI_API_KEY")
		if p == ProviderDeepSeek {
			// Allow OPENAI_* env vars to default to DeepSeek if not set.
			if apiKey == "" {
				apiKey = os.Getenv("DEEPSEEK_API_KEY")
			}
		}
		baseURL := os.Getenv("OPENAI_BASE_URL")
		model := firstEnv("OPENAI_MODEL", "OPENAI_MODEL_ID")
		if p == ProviderDeepSeek {
			if baseURL == "" {
				baseURL = "https://api.deepseek.com/v1"
			}
			if model == "" {
				model = "deepseek-chat"
			}
		}
		if isAgentic {
			timeout := 3 * time.Minute
			am, err := agenticopenai.New(ctx, &agenticopenai.Config{
				APIKey:  apiKey,
				Model:   model,
				BaseURL: baseURL,
				ByAzure: os.Getenv("OPENAI_BY_AZURE") == "true",
				Timeout: &timeout,
				Include: []openairesponses.ResponseIncludable{
					openairesponses.ResponseIncludableReasoningEncryptedContent,
				},
			})
			if err != nil {
				return nil, err
			}
			return any(am).(einomodel.BaseModel[M]), nil
		}
		cm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
			APIKey:  apiKey,
			Model:   model,
			BaseURL: baseURL,
			ByAzure: os.Getenv("OPENAI_BY_AZURE") == "true",
		})
		if err != nil {
			return nil, err
		}
		return any(cm).(einomodel.BaseModel[M]), nil
	}
	return nil, fmt.Errorf("unknown provider %q", p)
}

// formatChain renders the chain as a one-liner for error messages.
func formatChain(chain []FallbackEntry) string {
	parts := make([]string, 0, len(chain))
	for _, e := range chain {
		parts = append(parts, fmt.Sprintf("%s=%s", e.Provider, e.Err))
	}
	return strings.Join(parts, "; ")
}
