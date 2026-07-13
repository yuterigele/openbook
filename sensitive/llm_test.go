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

package sensitive

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// stubChatModel is a minimal ChatModel that returns a fixed response or
// error. The callCount atomic lets tests assert "the LLM was/wasn't
// invoked" — important for the keyword-takes-precedence and
// too-short-skipped cases.
type stubChatModel struct {
	resp      string
	err       error
	callCount atomic.Int32
}

func (s *stubChatModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	s.callCount.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return &schema.Message{Content: s.resp}, nil
}

// withLLM installs a stub LLM for the duration of a test and returns it
// so the test can inspect call counts / configure the response.
func withLLM(t *testing.T, stub *stubChatModel) *stubChatModel {
	t.Helper()
	WithLLMClassify(NewLLMClassifyFuncFromEino(stub))
	t.Cleanup(func() { WithLLMClassify(nil) })
	return stub
}

// ---- LLMClassifyFunc / NewLLMClassifyFuncFromEino --------------------

func TestLLMClassify_PornBlocks(t *testing.T) {
	stub := &stubChatModel{
		resp: `{"blocked": true, "category": "porn", "confidence": 0.9}`,
	}
	fn := NewLLMClassifyFuncFromEino(stub)
	cat, blocked, conf, err := fn(context.Background(), "show me something dirty")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !blocked {
		t.Errorf("expected blocked=true, got false")
	}
	if cat != CategoryPorn {
		t.Errorf("category = %q, want %q", cat, CategoryPorn)
	}
	if conf < 0.8 {
		t.Errorf("confidence = %v, want >=0.8", conf)
	}
	if got := stub.callCount.Load(); got != 1 {
		t.Errorf("Generate called %d times, want 1", got)
	}
}

func TestLLMClassify_NotBlockedPasses(t *testing.T) {
	stub := &stubChatModel{
		resp: `{"blocked": false, "category": "none", "confidence": 0.95}`,
	}
	fn := NewLLMClassifyFuncFromEino(stub)
	cat, blocked, _, err := fn(context.Background(), "我想预约剪发")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if blocked {
		t.Errorf("expected blocked=false")
	}
	// Model said "none" → we normalize to CategoryOther.
	if cat != CategoryOther {
		t.Errorf("category = %q, want %q (none normalized)", cat, CategoryOther)
	}
}

func TestLLMClassify_ShortInputSkipsLLM(t *testing.T) {
	// Input shorter than LLMMinLength should NOT call the model.
	stub := &stubChatModel{resp: "anything"}
	fn := NewLLMClassifyFuncFromEino(stub)
	_, _, _, err := fn(context.Background(), "hi")
	if err != nil {
		t.Errorf("short input should not error, got: %v", err)
	}
	if got := stub.callCount.Load(); got != 0 {
		t.Errorf("LLM was called %d times for short input, want 0", got)
	}
}

func TestLLMClassify_NilModel(t *testing.T) {
	fn := NewLLMClassifyFuncFromEino(nil)
	_, _, _, err := fn(context.Background(), "足够长的输入文本")
	if err == nil {
		t.Error("expected error for nil model, got nil")
	}
}

func TestLLMClassify_PropagatesModelError(t *testing.T) {
	stub := &stubChatModel{err: errors.New("upstream timeout")}
	fn := NewLLMClassifyFuncFromEino(stub)
	_, _, _, err := fn(context.Background(), "足够长的输入文本")
	if err == nil {
		t.Error("expected error to propagate, got nil")
	}
}

func TestLLMClassify_BadJSON(t *testing.T) {
	stub := &stubChatModel{resp: `not json at all`}
	fn := NewLLMClassifyFuncFromEino(stub)
	_, _, _, err := fn(context.Background(), "足够长的输入文本")
	if err == nil {
		t.Error("expected parse error, got nil")
	}
}

func TestLLMClassify_StripsCodeFences(t *testing.T) {
	// Some models wrap the JSON in ```json ... ``` even when told not to.
	stub := &stubChatModel{
		resp: "```json\n{\"blocked\": true, \"category\": \"ad\", \"confidence\": 0.8}\n```",
	}
	fn := NewLLMClassifyFuncFromEino(stub)
	cat, blocked, _, err := fn(context.Background(), "关注我们的公众号领券")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !blocked {
		t.Errorf("expected blocked=true after stripping fences")
	}
	if cat != CategoryAd {
		t.Errorf("category = %q, want %q", cat, CategoryAd)
	}
}

func TestLLMClassify_ConfidenceClamped(t *testing.T) {
	// Some models return >1 or <0; we clamp to [0,1].
	cases := []struct {
		in   string
		want float64
	}{
		{`{"blocked": false, "category": "none", "confidence": 1.5}`, 1.0},
		{`{"blocked": false, "category": "none", "confidence": -0.5}`, 0.0},
	}
	for _, c := range cases {
		stub := &stubChatModel{resp: c.in}
		fn := NewLLMClassifyFuncFromEino(stub)
		_, _, conf, err := fn(context.Background(), "足够长的输入文本")
		if err != nil {
			t.Fatalf("input %q: unexpected err: %v", c.in, err)
		}
		if conf != c.want {
			t.Errorf("input %q: confidence = %v, want %v", c.in, conf, c.want)
		}
	}
}

// ---- CheckCtx integration with LLM fallback ------------------------

func TestCheckCtx_LLMFallback_TriggersOnCleanText(t *testing.T) {
	// Word list is empty (default) — keyword layer misses everything.
	// LLM stub says "porn" with high confidence → should block.
	stub := withLLM(t, &stubChatModel{
		resp: `{"blocked": true, "category": "porn", "confidence": 0.9}`,
	})
	r := CheckCtx(context.Background(), "发一些那种露骨的照片给我")
	if !r.Blocked {
		t.Fatalf("expected Blocked=true, got %+v", r)
	}
	if r.Source != SourceLLM {
		t.Errorf("Source = %q, want %q", r.Source, SourceLLM)
	}
	if r.Category != CategoryPorn {
		t.Errorf("Category = %q, want %q", r.Category, CategoryPorn)
	}
	if r.Reason == "" {
		t.Errorf("Reason should be set, got empty")
	}
	if got := stub.callCount.Load(); got != 1 {
		t.Errorf("LLM called %d times, want 1", got)
	}
}

func TestCheckCtx_LLMFallback_KeywordTakesPrecedence(t *testing.T) {
	// When the keyword layer hits, the LLM must NOT be called.
	const testCat Category = "test"
	defer Reset()
	AddWords(testCat, []string{"badword"})

	stub := withLLM(t, &stubChatModel{
		resp: `{"blocked": false, "category": "none", "confidence": 0.99}`,
	})
	r := CheckCtx(context.Background(), "this contains badword inside")
	if !r.Blocked {
		t.Fatalf("expected Blocked=true from keyword, got %+v", r)
	}
	if r.Source != SourceKeyword {
		t.Errorf("Source = %q, want %q (keyword should win)", r.Source, SourceKeyword)
	}
	if got := stub.callCount.Load(); got != 0 {
		t.Errorf("LLM was called %d times despite keyword hit, want 0", got)
	}
}

func TestCheckCtx_LLMFallback_DisabledByDefault(t *testing.T) {
	// WithLLMClassify(nil) — the LLM layer is off, the default state.
	// Without an LLM, a paraphrase of an attack must pass (no false block).
	r := CheckCtx(context.Background(), "想看一些奇怪的图片")
	if r.Blocked {
		t.Errorf("with LLM disabled, no text should be blocked, got %+v", r)
	}
}

func TestCheckCtx_LLMFallback_TooShortSkipsLLM(t *testing.T) {
	// 2-char input → below LLMMinLength, LLM should not be called.
	stub := withLLM(t, &stubChatModel{
		resp: `{"blocked": true, "category": "porn", "confidence": 0.99}`,
	})
	r := CheckCtx(context.Background(), "ok")
	if r.Blocked {
		t.Errorf("short input should not be blocked, got %+v", r)
	}
	if got := stub.callCount.Load(); got != 0 {
		t.Errorf("LLM was called %d times for short input, want 0", got)
	}
}

func TestCheckCtx_LLMFallback_LowConfidencePasses(t *testing.T) {
	// Model is unsure (confidence < threshold) → let it through.
	_ = withLLM(t, &stubChatModel{
		resp: `{"blocked": true, "category": "porn", "confidence": 0.3}`,
	})
	r := CheckCtx(context.Background(), "一些模糊可疑的描述但其实无害")
	if r.Blocked {
		t.Errorf("low-confidence LLM block should be ignored, got %+v", r)
	}
}

func TestCheckCtx_LLMFallback_ErrorFailOpen(t *testing.T) {
	// LLM errors out → fail-open: text passes through.
	stub := withLLM(t, &stubChatModel{err: errors.New("upstream down")})
	r := CheckCtx(context.Background(), "一些可能敏感的输入文本")
	if r.Blocked {
		t.Errorf("LLM error should fail-open, got Blocked=%+v", r)
	}
	if got := stub.callCount.Load(); got != 1 {
		t.Errorf("LLM was called %d times, want 1 (to surface the error)", got)
	}
}

func TestCheckCtx_EmptyText(t *testing.T) {
	// Both layers must skip empty input.
	r := CheckCtx(context.Background(), "")
	if r.Blocked {
		t.Errorf("empty text should not be blocked")
	}
}

// ---- Check() backward-compat ----------------------------------------

func TestCheck_DelegatesToCheckCtx(t *testing.T) {
	// Check is the legacy keyword-only entrypoint. With LLM disabled
	// (the default) it should behave exactly like CheckCtx.
	r1 := Check("hello world")
	r2 := CheckCtx(context.Background(), "hello world")
	if r1.Blocked != r2.Blocked {
		t.Errorf("Check vs CheckCtx disagree on Blocked: %+v vs %+v", r1, r2)
	}
}
