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
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// TestNewModelWithFallback_AllProvidersFail_ReturnsStub is the key
// regression test for the v4.18+ degraded-mode behavior: when every
// real provider (DeepSeek / OpenAI / Ark) is unavailable, the
// function must return a stub model (chat-only, no tools) instead
// of an error.
//
// We simulate "all providers unavailable" by pointing the chain at
// an empty list via OPENBOOK_LLM_CHAIN. parseProviderList drops
// unknown names, so the for-loop body never runs and the stub
// branch is reached. (We can't use "no API key" as the trigger
// because eino-ext's NewChatModel doesn't validate APIKey at
// construction time — it just fails at first call.)
func TestNewModelWithFallback_AllProvidersFail_ReturnsStub(t *testing.T) {
	// Empty chain: every name is unknown to parseProviderList, so
	// the for-loop body is skipped and the stub branch runs.
	t.Setenv("OPENBOOK_LLM_CHAIN", "nonexistent_1,nonexistent_2")

	cm, used, chain, err := NewModelWithFallback[*schema.Message](context.Background())
	if err != nil {
		t.Fatalf("expected nil err (stub returned), got: %v", err)
	}
	if used != ProviderStub {
		t.Errorf("used = %q, want %q (chain empty → stub)", used, ProviderStub)
	}
	if cm == nil {
		t.Fatal("cm is nil; expected a stub model")
	}
	if len(chain) != 0 {
		t.Errorf("chain length = %d, want 0 (no real provider attempted)", len(chain))
	}

	// The returned model is the stub — it should produce an assistant
	// message with empty ToolCalls (no DB writes).
	out, gErr := cm.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("明天下午 3 点预约剪发"),
	})
	if gErr != nil {
		t.Fatalf("stub Generate: %v", gErr)
	}
	if out == nil {
		t.Fatal("stub returned nil message")
	}
	if len(out.ToolCalls) != 0 {
		t.Errorf("stub returned %d tool calls; degraded mode must not trigger any", len(out.ToolCalls))
	}
	if out.Role != schema.Assistant {
		t.Errorf("Role = %q, want assistant", out.Role)
	}
}

// TestNewModelWithFallback_AllProvidersFail_ReturnsStub_Agentic
// does the same for the M = *schema.AgenticMessage path. The agentic
// path uses different eino providers (agenticark / agenticopenai),
// so a stub in this path needs to be AgenticMessage-shaped.
func TestNewModelWithFallback_AllProvidersFail_ReturnsStub_Agentic(t *testing.T) {
	t.Setenv("OPENBOOK_LLM_CHAIN", "nonexistent_1,nonexistent_2")

	cm, used, chain, err := NewModelWithFallback[*schema.AgenticMessage](context.Background())
	if err != nil {
		t.Fatalf("expected nil err (stub returned), got: %v", err)
	}
	if used != ProviderStub {
		t.Errorf("used = %q, want %q", used, ProviderStub)
	}
	if cm == nil {
		t.Fatal("cm is nil; expected a stub model")
	}
	if len(chain) != 0 {
		t.Errorf("chain length = %d, want 0 (no real provider attempted)", len(chain))
	}
	// Smoke-test the returned model: it should not panic and should
	// produce a non-nil message.
	if _, gErr := cm.Generate(context.Background(), nil); gErr != nil {
		t.Errorf("stub Generate: %v", gErr)
	}
}

func TestNewRuntimeFailoverConfig_Stub(t *testing.T) {
	t.Setenv("OPENBOOK_LLM_CHAIN", "stub")

	if cfg := NewRuntimeFailoverConfig[*schema.Message](context.Background(), ProviderStub); cfg != nil {
		t.Fatal("stub must not configure provider failover")
	}
}

func TestIsTransientProviderError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.New("HTTP 503 Service Unavailable"), true},
		{errors.New("connection reset by peer"), true},
		{errors.New("invalid API key"), false},
		{nil, false},
	} {
		if got := isTransientProviderError(tc.err); got != tc.want {
			t.Errorf("isTransientProviderError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
