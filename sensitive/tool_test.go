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
	"encoding/json"
	"strings"
	"testing"
)

func TestSensitiveCheckTool_Info(t *testing.T) {
	tl := &SensitiveCheckTool{}
	info, err := tl.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "sensitive_check" {
		t.Errorf("Name = %q, want %q", info.Name, "sensitive_check")
	}
	if info.Desc == "" {
		t.Error("Desc should not be empty")
	}
	if info.ParamsOneOf == nil {
		t.Error("ParamsOneOf should not be nil")
	}
}

func TestSensitiveCheckTool_Run_Clean(t *testing.T) {
	tl := &SensitiveCheckTool{}
	in, _ := json.Marshal(sensitiveCheckInput{Text: "明天下午 3 点剪发"})
	out, err := tl.InvokableRun(context.Background(), string(in))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got sensitiveCheckOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if got.Blocked {
		t.Errorf("expected clean input to pass, got %+v", got)
	}
}

func TestSensitiveCheckTool_Run_Hit(t *testing.T) {
	const testCat Category = "test"
	defer Reset()
	AddWords(testCat, []string{"badtool"})

	tl := &SensitiveCheckTool{}
	in, _ := json.Marshal(sensitiveCheckInput{Text: "this contains badtool inside"})
	out, err := tl.InvokableRun(context.Background(), string(in))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got sensitiveCheckOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if !got.Blocked {
		t.Errorf("expected hit, got %+v", got)
	}
	if got.Word != "badtool" {
		t.Errorf("Word = %q, want badtool", got.Word)
	}
	if got.Reason == "" {
		t.Error("Reason should not be empty on hit")
	}
}

func TestSensitiveCheckTool_Run_InvalidInput(t *testing.T) {
	tl := &SensitiveCheckTool{}
	tests := []struct {
		name string
		args string
	}{
		{"empty args", ""},
		{"not json", "not json at all"},
		{"empty text field", `{"text": ""}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tl.InvokableRun(context.Background(), tt.args)
			if err == nil {
				t.Errorf("expected error for %s, got nil", tt.name)
			}
			if !strings.Contains(err.Error(), "sensitive_check") {
				t.Errorf("error should mention tool name, got: %v", err)
			}
		})
	}
}
