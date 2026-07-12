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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
)

// ApplyMessageModelRetry enables model-call retries for transient rate-limit
// and transport errors.
//
// v4.17+ 扩展：除了 429/QPM，还识别常见的瞬时错误（5xx、连接拒绝、超时），
// 让 eino 内置 retry 帮我们做"小抖动"重试。provider 级别的硬故障
// （API key 错、配额耗尽）不会被重试，由 chatmodel.NewModelWithFallback
// 在 init 阶段切 provider 兜底。
func ApplyMessageModelRetry[M adk.MessageType](cfg *deep.TypedConfig[M]) {
	cfg.ModelRetryConfig = &adk.TypedModelRetryConfig[M]{
		MaxRetries: 5,
		IsRetryAble: func(_ context.Context, err error) bool {
			msg := err.Error()
			// 限流类
			if strings.Contains(msg, "429") ||
				strings.Contains(msg, "Too Many Requests") ||
				strings.Contains(msg, "qpm limit") {
				return true
			}
			// 服务端错误类（5xx）
			if strings.Contains(msg, "500") ||
				strings.Contains(msg, "502") ||
				strings.Contains(msg, "503") ||
				strings.Contains(msg, "504") ||
				strings.Contains(msg, "Bad Gateway") ||
				strings.Contains(msg, "Service Unavailable") {
				return true
			}
			// 网络瞬时错误
			if strings.Contains(msg, "connection reset") ||
				strings.Contains(msg, "connection refused") ||
				strings.Contains(msg, "EOF") ||
				strings.Contains(msg, "i/o timeout") ||
				strings.Contains(msg, "TLS handshake timeout") {
				return true
			}
			return false
		},
	}
}

func IsModelRetryInProgress(err error) bool {
	var willRetry *adk.WillRetryError
	return errors.As(err, &willRetry)
}

func LogModelRetry(w io.Writer, err error) bool {
	if !IsModelRetryInProgress(err) {
		return false
	}
	if w != nil {
		fmt.Fprintf(w, "\n[retry] model call failed, retrying: %v\n", err)
	}
	return true
}
