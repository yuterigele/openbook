package tools

// util.go
//
// v4.5 C1 工具降级 — 统一错误处理 + 友好兜底
//
// 设计目标：
//   - DB 挂了 / Redis 不可用 / 任何 panic-level 错误都返回"系统忙，请稍后再试"风格话术
//   - 不向 LLM 暴露 SQL 错误 / 栈追踪 / 内部错误码
//   - 失败 silent log（运维有线索，顾客不恐慌）
//   - storage.DB == nil 时优雅降级（dev 模式常遇到）
//
// 工具实现规范：
//   - 所有 InvokableRun 内部捕获错误后调 FriendlyError() 转译
//   - 底层用 log.Printf（避免循环依赖；不动 storage 层）
//   - 不抛 panic；Agent 拿到 string 错误就当工具失败处理

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// FriendlyError 把内部错误转成"给顾客看的友好话术"
//
//   - 优先保留 error 类型（让 Agent 看到 fail）
//   - 不暴露 SQL / 栈追踪 / "ErrXxx" 等技术字眼
//   - 调用方传 fallback 作为兜底文案
//   - silent log 内部错误（运维可查）
//
// 使用方式：
//
//	out, err := storage.GetFoo(...)
//	if err != nil {
//	    return FriendlyError(ctx, err, "查询失败，请稍后重试", "create_appointment.GetFoo")
//	}
func FriendlyError(ctx context.Context, err error, fallback, op string) error {
	if err == nil {
		return nil
	}
	// silent log（带 op 标签方便 grep）
	log.Printf("[tools] %s failed: %v (ctx_err=%v)", op, err, ctx.Err())
	// 已经友好的（不包含技术字眼）直接返回
	if isAlreadyFriendly(err) {
		return err
	}
	// 兜底话术
	if fallback == "" {
		fallback = "系统忙不过来了，请稍后再试"
	}
	return errors.New(fallback)
}

// FriendlyResult 是 FriendlyError 的"返回 string + error"便捷封装
//
// 多数工具返回 (string, error)，其中 string 已经是给 Agent 的提示。
// 工具失败时 err != nil，string 返回空让 Agent 看 error 即可。
func FriendlyResult(ctx context.Context, err error, fallback, op string) (string, error) {
	return "", FriendlyError(ctx, err, fallback, op)
}

// isAlreadyFriendly 判断错误是否已经是"用户友好"格式
//
// 启发式：必须含中文字符才视为"已友好"；纯英文/技术 token 全部视为"需转译"
//
//   - 含 SQL / Err* / 网络错误 / panic 等技术字眼 → false（要替换）
//   - 纯中文 / 顾客能看懂的 → true（保留）
//   - 纯英文短串（如 "EOF" / "not found"）→ false（要替换成中文）
func isAlreadyFriendly(err error) bool {
	if err == nil {
		return true
	}
	msg := err.Error()
	// 技术 / 网络错误 token：出现任一就视为"非友好"
	techTokens := []string{
		"SQL", "sql:", "SELECT", "INSERT", "UPDATE", "DELETE",
		"ErrRecordNotFound", "ErrBarber", "ErrSlot", "ErrAppointment",
		"gorm:", "panicked", "nil pointer",
		"connection", "refused", "timeout", "i/o", "EOF", "no such", "dial",
		"panic", "runtime error", "index out of",
	}
	for _, t := range techTokens {
		if strings.Contains(msg, t) {
			return false
		}
	}
	// 没有任何技术 token：必须有中文才算"友好"
	return strings.ContainsAny(msg, "顾客用户店时周日在是不了请吧啊你")
}

// EnsureDB 检查 storage.DB 是否初始化（dev / 测试场景）
//
//   - 返回 nil 表示可用
//   - 返回 friendly error 表示 DB 不可用（tools 应立即返回，不再继续）
func EnsureDB(op string) error {
	// 反射检查 storage.DB == nil；通过工具函数避免直接依赖 storage 字段
	if !isStorageDBReady() {
		log.Printf("[tools] %s: storage.DB not initialized", op)
		return errors.New("服务暂时不可用，请稍后再试")
	}
	return nil
}

// isStorageDBReady 通过 storage.IsReady() 判断 DB 是否就绪
//
// 避免在 tools 包直接 import storage.DB 全局变量（保持解耦）
func isStorageDBReady() bool {
	return storage.IsReady()
}

// WithTimeout 给 ctx 加一个超时（防止工具卡死）
//
//   - 适用于 query/list 类的读操作
//   - create_appointment 自己已经有 5s 锁超时，不需要再包
//   - 默认 3s；写操作建议用 5s
func WithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}

// SilentLog 静默记一笔（用于"不影响业务但需要记下来"的场景）
//
// 例如：create_appointment 检测到 IsBarberOnLeaveAt 失败，
// 不阻塞下单但记一笔 log 便于排查。
func SilentLog(op, msg string, args ...any) {
	if len(args) > 0 {
		log.Printf("[tools] %s: "+msg, append([]any{op}, args...)...)
	} else {
		log.Printf("[tools] %s: %s", op, msg)
	}
}
