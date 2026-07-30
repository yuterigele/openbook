package chatmodel

import (
	"context"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var (
	phonePattern  = regexp.MustCompile(`\b1\d{10}\b`)
	secretPattern = regexp.MustCompile(`(?i)\b(sk-[a-z0-9_-]{12,}|bearer\s+[a-z0-9._-]{12,}|api[_ -]?key\s*[:=]\s*[^\s,;]{8,})`)
)

// NewDebugLogHandler logs model prompts and replies only when LLM_DEBUG_LOG=1.
// It is deliberately opt-in: prompts may contain customer personal data.
func NewDebugLogHandler() callbacks.Handler {
	if os.Getenv("LLM_DEBUG_LOG") != "1" {
		return nil
	}
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if !isModelRun(info) {
				return ctx
			}
			if in := einomodel.ConvCallbackInput(input); in != nil {
				for i, message := range in.Messages {
					if message != nil {
						log.Printf("[llm-debug] request component=%s message=%d role=%s content=%q", info.Name, i, message.Role, safeLogText(message.Content))
					}
				}
			}
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if !isModelRun(info) {
				return ctx
			}
			if out := einomodel.ConvCallbackOutput(output); out != nil && out.Message != nil {
				log.Printf("[llm-debug] response component=%s role=%s content=%q tool_calls=%d", info.Name, out.Message.Role, safeLogText(out.Message.Content), len(out.Message.ToolCalls))
			}
			return ctx
		}).
		Build()
}

// DebugLogPrompt and DebugLogResponse cover agent execution paths that do not
// emit Eino model callbacks (notably the typed deep-agent streaming adapter).
func DebugLogPrompt(source, text string) {
	if os.Getenv("LLM_DEBUG_LOG") == "1" {
		log.Printf("[llm-debug] request source=%s content=%q", source, safeLogText(text))
	}
}

func DebugLogResponse(source, text string) {
	if os.Getenv("LLM_DEBUG_LOG") == "1" {
		log.Printf("[llm-debug] response source=%s content=%q", source, safeLogText(text))
	}
}

func safeLogText(value string) string {
	value = phonePattern.ReplaceAllString(value, "[REDACTED_PHONE]")
	value = secretPattern.ReplaceAllString(value, "[REDACTED_SECRET]")
	limit := 4000
	if raw, err := strconv.Atoi(os.Getenv("LLM_DEBUG_LOG_MAX_CHARS")); err == nil && raw > 0 {
		limit = raw
	}
	if utf8.RuneCountInString(value) > limit {
		value = string([]rune(value)[:limit]) + "…[truncated]"
	}
	return strings.TrimSpace(value)
}

// Ensure schema is retained in this file's public callback signatures across
// Eino minor versions that move the stream reader helpers.
var _ *schema.Message
