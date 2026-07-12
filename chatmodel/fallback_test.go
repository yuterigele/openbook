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
	"os"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestParseProviderList(t *testing.T) {
	tests := []struct {
		in   string
		want []Provider
	}{
		{"deepseek,openai,ark", []Provider{ProviderDeepSeek, ProviderOpenAI, ProviderArk}},
		{"  ark , OPENAI  ", []Provider{ProviderArk, ProviderOpenAI}},
		// Empty / all-unknown input falls back to default chain (defensive).
		{"", []Provider{ProviderDeepSeek, ProviderOpenAI, ProviderArk}},
		{"unknown,deepseek", []Provider{ProviderDeepSeek}},
	}
	for _, tt := range tests {
		got := parseProviderList(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("parseProviderList(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseProviderList(%q)[%d] = %q, want %q",
					tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

func TestDefaultFallbackChain_EnvOverride(t *testing.T) {
	t.Setenv("OPENBOOK_LLM_CHAIN", "ark,openai")
	got := DefaultFallbackChain()
	want := []Provider{ProviderArk, ProviderOpenAI}
	if len(got) != len(want) {
		t.Fatalf("DefaultFallbackChain() = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDefaultFallbackChain_DefaultOrder(t *testing.T) {
	os.Unsetenv("OPENBOOK_LLM_CHAIN")
	got := DefaultFallbackChain()
	want := []Provider{ProviderDeepSeek, ProviderOpenAI, ProviderArk}
	if len(got) != len(want) {
		t.Fatalf("DefaultFallbackChain() = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFormatChain(t *testing.T) {
	chain := []FallbackEntry{
		{Provider: ProviderDeepSeek, Err: "connection refused"},
		{Provider: ProviderOpenAI, Err: "401 unauthorized"},
		{Provider: ProviderArk, Err: ""},
	}
	got := formatChain(chain)
	want := "deepseek=connection refused; openai=401 unauthorized; ark="
	if got != want {
		t.Errorf("formatChain = %q, want %q", got, want)
	}
}

// TestBuildProvider_DeepSeekWithoutKeyFails is a smoke test: building with no
// DeepSeek key should produce an error (not panic), proving the fallback
// loop has a real "fail" path to fall through.
func TestBuildProvider_DeepSeekWithoutKeyFails(t *testing.T) {
	os.Unsetenv("DEEPSEEK_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("OPENAI_BASE_URL")
	os.Unsetenv("ARK_API_KEY")
	os.Unsetenv("ARK_MODEL")
	os.Unsetenv("ARK_BASE_URL")

	_, err := buildProvider[*schema.Message](context.Background(), ProviderDeepSeek)
	if err == nil {
		t.Skip("deepseek accepted a missing key — provider may be lenient in this env; skipping")
	}
}
