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

package pool

import (
	"context"
	"sync"

	"github.com/yuterigele/openbook/intent"
	"github.com/yuterigele/openbook/sensitive"
)

// PreCheckResult bundles the outputs of a parallel pre-check pass.
type PreCheckResult struct {
	Sensitive sensitive.Result
	Intent    intent.ClassifyResult
}

// PreCheck runs sensitive.Check + intent.Classify in parallel using the
// pool's workers. Returns when both have completed (or the context is
// canceled).
//
// Rationale: the user-facing pipeline on each chat message is
//
//   1. sensitive filter (always — fail-fast on bad input)
//   2. intent classify (always — routing hint for the LLM)
//   3. history load
//   4. agent run
//
// Steps 1 and 2 are independent and can run concurrently. The total
// latency is max(t_sensitive, t_intent) instead of t_sensitive + t_intent.
// On the cheap keyword path both are < 1ms, so the savings are tiny —
// but on the LLM-fallback path (intent Layer 2) the LLM call dominates
// the total, and parallelising with sensitive saves the sensitive cost.
func (p *Pool) PreCheck(ctx context.Context, userText string, intentClf *intent.Classifier) PreCheckResult {
	var (
		res PreCheckResult
		wg  sync.WaitGroup
	)

	wg.Add(2)
	if err := p.SubmitCtx(ctx, func() {
		defer wg.Done()
		res.Sensitive = sensitive.Check(userText)
	}); err != nil {
		wg.Done()
	}
	if err := p.SubmitCtx(ctx, func() {
		defer wg.Done()
		if intentClf != nil {
			res.Intent = intentClf.Classify(ctx, userText)
		}
	}); err != nil {
		wg.Done()
	}
	wg.Wait()
	return res
}
