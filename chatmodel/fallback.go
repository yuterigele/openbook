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

// ProviderStub is the sentinel Provider value returned by
// NewModelWithFallback when every real provider failed and the
// returned model is a stubChatModel (chat-only, no tools). Use it
// to log a one-line "running in degraded mode" warning at startup.
const ProviderStub Provider = "stub"

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
	// v4.18+ bug fix: if every name was unknown, return an empty
	// chain (NOT a recursive fallback to DefaultFallbackChain, which
	// would re-read OPENBOOK_LLM_CHAIN and infinite-loop if the env
	// var still holds the unrecognized value).
	//
	// The empty chain is the trigger for the stub branch in
	// NewModelWithFallback — see "all providers failed" handling.
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
// v4.18+ degradation: when EVERY provider has failed to initialize,
// the function now returns a stubChatModel (chat-only, no tools)
// instead of an error. This lets the process keep running so the
// store-front stays online: the customer gets a canned "AI 助手暂
// 不可用" reply instead of a connection-refused error. The caller
// detects this via `used == ProviderStub`.
//
// `chain` is a recorded slice (one entry per provider attempt) so callers
// can log / assert which provider actually answered (or whether they
// all failed).
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

	// v4.18+ degraded-mode fallback: every provider failed. Return a
	// stub model instead of an error so the process can keep running.
	// The stub is chat-only: no LLM, no tool calls, no DB writes.
	log.Printf("[chatmodel] ⚠️  ALL providers failed — entering degraded mode (chat-only stub)")
	return newStubForType[M](), ProviderStub, chain, nil
}

// NewRuntimeFailoverConfig creates the per-request provider failover used by
// the agent after retry attempts are exhausted. It deliberately excludes the
// provider already selected at startup, then tries the remaining configured
// providers in order. A later request starts from the primary again; a circuit
// breaker can be layered on top when provider health is made shared state.
func NewRuntimeFailoverConfig[M adk.MessageType](buildCtx context.Context, primary Provider) *adk.ModelFailoverConfig[M] {
	providers := DefaultFallbackChain()
	remaining := make([]Provider, 0, len(providers)-1)
	for _, provider := range providers {
		if provider != primary {
			remaining = append(remaining, provider)
		}
	}
	return &adk.ModelFailoverConfig[M]{
		MaxRetries: uint(len(remaining)),
		ShouldFailover: func(ctx context.Context, _ M, err error) bool {
			return ctx.Err() == nil && isTransientProviderError(err)
		},
		GetFailoverModel: func(ctx context.Context, failoverCtx *adk.FailoverContext[M]) (einomodel.BaseModel[M], []M, error) {
			index := int(failoverCtx.FailoverAttempt) - 1
			if index < 0 || index >= len(remaining) {
				return nil, nil, nil
			}
			provider := remaining[index]
			model, err := buildProvider[M](buildCtx, provider)
			if err != nil {
				return nil, nil, fmt.Errorf("build runtime failover provider %s: %w", provider, err)
			}
			log.Printf("[chatmodel] runtime failover attempt=%d provider=%s", failoverCtx.FailoverAttempt, provider)
			return model, nil, nil
		},
	}
}

func isTransientProviderError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{"429", "too many requests", "qpm limit", "500", "502", "503", "504", "bad gateway", "service unavailable", "connection reset", "connection refused", "i/o timeout", "tls handshake timeout", "eof"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// newStubForType returns the stub model matching the M generic. The
// two concrete types are necessary because einomodel.BaseModel[M]
// has a fixed type-set constraint:
//
//	*schema.Message | *schema.AgenticMessage
//
// A single generic stub can't satisfy both at once (the methods
// would need to return either of two incompatible concrete types).
// Instead we keep two stubs and pick at the call site.
func newStubForType[M adk.MessageType]() einomodel.BaseModel[M] {
	var zero M
	switch any(zero).(type) {
	case *schema.AgenticMessage:
		return any(&stubAgenticModel{}).(einomodel.BaseModel[M])
	default:
		return any(&stubMessageModel{}).(einomodel.BaseModel[M])
	}
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
			timeout := defaultAgenticModelTimeout
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
			timeout := defaultAgenticModelTimeout
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
