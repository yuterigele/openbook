package helpers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// GenerateSmallWithPolicy keeps optional classification from delaying the
// booking path. A 429 or timeout fails fast; only brief transport/5xx errors
// get one retry, each bounded to one second.
func GenerateSmallWithPolicy(ctx context.Context, cm interface {
	Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)
}, messages []*schema.Message) (*schema.Message, error) {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, time.Second)
		out, err := cm.Generate(callCtx, messages)
		cancel()
		if err == nil {
			return out, nil
		}
		last = err
		if errors.Is(err, context.DeadlineExceeded) || !smallRetryable(err) {
			break
		}
	}
	return nil, last
}

func smallRetryable(err error) bool {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "429") || strings.Contains(msg, "too many") || strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") {
		return false
	}
	for _, marker := range []string{"500", "502", "503", "504", "connection reset", "connection refused", "eof", "tls handshake"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
