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

package helpers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/storage"
)

// NewSafeToolMiddleware converts tool errors into error-message strings so that
// a non-zero exit code or mid-stream failure is returned to the model as a
// readable tool result instead of aborting the agent pipeline.
func NewSafeToolMiddleware[M adk.MessageType]() adk.TypedChatModelAgentMiddleware[M] {
	return NewSafeToolMiddlewareWithRetryPolicy[M](defaultRetryAttempts)
}

// NewSafeToolMiddlewareWithRetryPolicy 使用工具注册表提供的重试策略。
func NewSafeToolMiddlewareWithRetryPolicy[M adk.MessageType](retryAttempts map[string]int) adk.TypedChatModelAgentMiddleware[M] {
	return &safeToolMiddleware[M]{
		TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[M]{},
		retryAttempts:                     cloneRetryAttempts(retryAttempts),
	}
}

type safeToolMiddleware[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	retryAttempts map[string]int
}

func (m *safeToolMiddleware[M]) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		startedAt := time.Now()
		toolName := "unknown"
		if tCtx != nil && tCtx.Name != "" {
			toolName = tCtx.Name
		}
		result, err := endpoint(ctx, args, opts...)
		maxAttempts := m.retryAttempts[toolName]
		for attempt := 1; err != nil && attempt < maxAttempts && isRetryableTool(tCtx, err, m.retryAttempts); attempt++ {
			// 只读工具不会改变预约状态，短暂故障可以按注册策略重试。
			select {
			case <-ctx.Done():
			case <-time.After(100 * time.Millisecond):
			}
			if ctx.Err() != nil {
				break
			}
			result, err = endpoint(ctx, args, opts...)
		}
		if err != nil {
			if _, ok := compose.IsInterruptRerunError(err); ok {
				return "", err
			}
			storage.RecordTraceSpan(ctx, storage.TraceSpan{
				Name: "tool." + toolName, Kind: "internal", Status: "error",
				ErrorCode: "tool_execution_failed", StartedAt: startedAt,
			}, map[string]any{"arguments_bytes": len(args), "retried": isRetryableReadTool(tCtx, err)})
			if action, ok := mutatingToolAuditActions[toolName]; ok {
				_ = storage.WriteAudit(ctx, storage.AuditLog{
					ShopID: storage.AuditShopFromContext(ctx), ActorType: storage.AuditActorAgent,
					Action: action, ResourceType: "request", ResourceID: storage.TraceIDFromContext(ctx),
					Outcome: storage.AuditOutcomeFailure, ErrorCode: "tool_execution_failed",
				}, map[string]any{"tool": toolName, "arguments_bytes": len(args)})
			}
			return fmt.Sprintf("[tool error] %v", err), nil
		}
		storage.RecordTraceSpan(ctx, storage.TraceSpan{
			Name: "tool." + toolName, Kind: "internal", Status: "ok", StartedAt: startedAt,
		}, map[string]any{"arguments_bytes": len(args), "result_bytes": len(result)})
		return result, nil
	}, nil
}

var mutatingToolAuditActions = map[string]string{
	"create_appointment": "appointment.create",
	"cancel_appointment": "appointment.cancel",
	"handoff_to_human":   "handoff.create",
}

func isRetryableReadTool(tCtx *adk.ToolContext, err error) bool {
	return isRetryableTool(tCtx, err, defaultRetryAttempts)
}

func isRetryableTool(tCtx *adk.ToolContext, err error, retryAttempts map[string]int) bool {
	if tCtx == nil || err == nil || retryAttempts[tCtx.Name] <= 1 || nonRetryableToolNames[tCtx.Name] {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection reset", "connection refused", "connection closed", "i/o timeout",
		"deadline exceeded", "database is locked", "temporarily unavailable", "eof",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

var defaultRetryAttempts = map[string]int{
	"sensitive_check":    2,
	"classify_intent":    2,
	"query_schedule":     2,
	"list_barbers":       2,
	"list_services":      2,
	"barber_leave":       2,
	"get_appointment":    2,
	"list_shop_holidays": 2,
}

var nonRetryableToolNames = map[string]bool{
	"create_appointment": true,
	"cancel_appointment": true,
	"handoff_to_human":   true,
}

func cloneRetryAttempts(source map[string]int) map[string]int {
	clone := make(map[string]int, len(source))
	for name, attempts := range source {
		clone[name] = attempts
	}
	return clone
}

func (m *safeToolMiddleware[M]) WrapStreamableToolCall(
	_ context.Context,
	endpoint adk.StreamableToolCallEndpoint,
	_ *adk.ToolContext,
) (adk.StreamableToolCallEndpoint, error) {
	return func(ctx context.Context, args string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		sr, err := endpoint(ctx, args, opts...)
		if err != nil {
			if _, ok := compose.IsInterruptRerunError(err); ok {
				return nil, err
			}
			return SingleChunkReader(fmt.Sprintf("[tool error] %v", err)), nil
		}
		return safeWrapReader(sr), nil
	}, nil
}

// SingleChunkReader returns a StreamReader that emits one string then EOF.
func SingleChunkReader(msg string) *schema.StreamReader[string] {
	r, w := schema.Pipe[string](1)
	_ = w.Send(msg, nil)
	w.Close()
	return r
}

// safeWrapReader proxies chunks from sr; on a stream error it emits the error
// as a final chunk instead of propagating it, so the model sees a complete
// (if error-annotated) tool result rather than a pipeline failure.
func safeWrapReader(sr *schema.StreamReader[string]) *schema.StreamReader[string] {
	r, w := schema.Pipe[string](64)
	go func() {
		defer w.Close()
		for {
			chunk, err := sr.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				_ = w.Send(fmt.Sprintf("\n[tool error] %v", err), nil)
				return
			}
			_ = w.Send(chunk, nil)
		}
	}()
	return r
}
