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
)

// NewSafeToolMiddleware converts tool errors into error-message strings so that
// a non-zero exit code or mid-stream failure is returned to the model as a
// readable tool result instead of aborting the agent pipeline.
func NewSafeToolMiddleware[M adk.MessageType]() adk.TypedChatModelAgentMiddleware[M] {
	return &safeToolMiddleware[M]{
		TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[M]{},
	}
}

type safeToolMiddleware[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
}

func (m *safeToolMiddleware[M]) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		result, err := endpoint(ctx, args, opts...)
		if err != nil && isRetryableReadTool(tCtx, err) {
			// Read-only tools never mutate reservations. One short retry absorbs
			// transient DB/Redis/network blips without risking a duplicate write.
			select {
			case <-ctx.Done():
			case <-time.After(100 * time.Millisecond):
			}
			if ctx.Err() == nil {
				result, err = endpoint(ctx, args, opts...)
			}
		}
		if err != nil {
			if _, ok := compose.IsInterruptRerunError(err); ok {
				return "", err
			}
			return fmt.Sprintf("[tool error] %v", err), nil
		}
		return result, nil
	}, nil
}

func isRetryableReadTool(tCtx *adk.ToolContext, err error) bool {
	if tCtx == nil || err == nil || !readOnlyToolNames[tCtx.Name] {
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

var readOnlyToolNames = map[string]bool{
	"sensitive_check":    true,
	"classify_intent":    true,
	"query_schedule":     true,
	"list_barbers":       true,
	"list_services":      true,
	"barber_leave":       true,
	"get_appointment":    true,
	"list_shop_holidays": true,
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
