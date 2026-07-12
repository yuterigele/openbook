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

package intent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ClassifyTool exposes the two-layer classifier to the Agent.
//
// The Agent calls this on every user message (after sensitive_check) to
// pick a routing path. The result is a closed-set intent string + a
// confidence number; the Agent should treat confidence < 0.5 as "fall
// through to whatever you think is best" rather than blindly trusting the
// classifier.
type ClassifyTool struct {
	clf *Classifier
}

// NewClassifyTool returns a tool wrapping the given classifier.
func NewClassifyTool(clf *Classifier) *ClassifyTool {
	return &ClassifyTool{clf: clf}
}

// Info describes the tool for the LLM tool-calling layer.
func (t *ClassifyTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "classify_intent",
		Desc: `Classify the user's message into a closed intent set before deciding which tool to call.
Input: {"text": "the user's most recent message"}.
Output: {"intent": "<book|cancel|reschedule|query_open|list_barbers|list_service|list_holiday|greeting|complaint|handoff|chitchat|unknown>", "confidence": 0.0-1.0, "source": "keyword|llm"}.

This is a routing hint, not a hard rule. If confidence < 0.5, the user is probably saying something the classifier doesn't know — pick the tool you think is best based on the message text.
This tool does NOT replace sensitive_check — always call sensitive_check FIRST.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"text": {
				Type:     "string",
				Desc:     "the user's most recent message",
				Required: true,
			},
		}),
	}, nil
}

type classifyInput struct {
	Text string `json:"text"`
}

type classifyOutput struct {
	Intent     Intent  `json:"intent"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
	Trigger    string  `json:"trigger,omitempty"`
	Rationale  string  `json:"rationale,omitempty"`
}

// InvokableRun executes the classifier and returns JSON.
func (t *ClassifyTool) InvokableRun(ctx context.Context, argsIn string, opts ...tool.Option) (string, error) {
	var in classifyInput
	if err := json.Unmarshal([]byte(argsIn), &in); err != nil {
		return "", fmt.Errorf("classify_intent: invalid input: %w", err)
	}
	if in.Text == "" {
		return "", fmt.Errorf("classify_intent: text is empty")
	}
	if t.clf == nil {
		return "", fmt.Errorf("classify_intent: classifier not initialized")
	}
	r := t.clf.Classify(ctx, in.Text)
	out := classifyOutput{
		Intent:     r.Intent,
		Confidence: r.Confidence,
		Source:     r.Source,
		Trigger:    r.TriggerWord,
		Rationale:  r.LMRationale,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("classify_intent: marshal output: %w", err)
	}
	return string(b), nil
}

var _ tool.InvokableTool = (*ClassifyTool)(nil)
