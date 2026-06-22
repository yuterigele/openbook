package tools

// util_test.go
//
// v4.5 C1 工具降级 — 单元测试

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yuterigele/openbook/storage"
)

func TestFriendlyError_NilInput(t *testing.T) {
	err := FriendlyError(context.Background(), nil, "fallback", "test")
	if err != nil {
		t.Errorf("nil err 应返回 nil，实际: %v", err)
	}
}

func TestFriendlyError_RawSQLError_BecomesFriendly(t *testing.T) {
	raw := errors.New("SQL logic error: no such table: appointments")
	out := FriendlyError(context.Background(), raw, "查询失败，请稍后重试", "query_foo")
	if out == nil {
		t.Fatal("应返回 error")
	}
	if strings.Contains(out.Error(), "SQL") {
		t.Errorf("友好化后不应含 SQL 字眼: %s", out.Error())
	}
	if !strings.Contains(out.Error(), "查询失败") {
		t.Errorf("应返回 fallback: %s", out.Error())
	}
}

func TestFriendlyError_AlreadyFriendly_Passes(t *testing.T) {
	friendly := errors.New("师傅 Tony 不在店里呢")
	out := FriendlyError(context.Background(), friendly, "fallback", "test")
	if out == nil || out.Error() != "师傅 Tony 不在店里呢" {
		t.Errorf("已友好应原样返回: %v", out)
	}
}

func TestFriendlyError_EmptyFallback(t *testing.T) {
	raw := errors.New("connection refused")
	out := FriendlyError(context.Background(), raw, "", "test")
	if out == nil || !strings.Contains(out.Error(), "系统忙") {
		t.Errorf("空 fallback 应走默认话术: %s", out.Error())
	}
}

func TestFriendlyResult_NilErrReturnsEmptyString(t *testing.T) {
	// nil err 不应走 FriendlyError，但 FriendlyResult 形式是 (_, err)
	// 实际用法：调用方先 if err != nil { return FriendlyResult(...) }
	// 这里测试：当 err = nil 时返回 ("", nil)
	s, err := FriendlyResult(context.Background(), nil, "fallback", "test")
	if s != "" || err != nil {
		t.Errorf("nil 应返回空 string + nil err: %s, %v", s, err)
	}
}

func TestEnsureDB_NoDBReturnsFriendly(t *testing.T) {
	// 默认状态下 storage.DB = nil（测试 setup 还没跑）
	// 但注意：可能其他 test 跑过 setup 后 DB 非 nil —— 用 t.Cleanup 兜底
	// 这里用独立的检测方式：直接调 storage.IsReady
	if storage.IsReady() {
		t.Skip("DB 已就绪（其他 test 副作用），跳过 nil-DB 测试")
	}
	err := EnsureDB("test_op")
	if err == nil {
		t.Fatal("DB 未就绪应返回 error")
	}
	if !strings.Contains(err.Error(), "不可用") {
		t.Errorf("应是友好话术: %s", err.Error())
	}
}

func TestIsAlreadyFriendly(t *testing.T) {
	cases := []struct {
		err   error
		want  bool
	}{
		{errors.New("SQL: syntax error"), false},
		{errors.New("ErrBarberNotFound"), false},
		{errors.New("师傅 Tony 不在"), true},
		{errors.New("你输入的日期有误"), true},
		{errors.New("connection refused"), false}, // 英文技术错误，需替换
		{errors.New("context deadline exceeded"), false}, // 超时也要替换
		{errors.New("i/o timeout"), false},
	}
	for _, c := range cases {
		got := isAlreadyFriendly(c.err)
		if got != c.want {
			t.Errorf("isAlreadyFriendly(%q) = %v, want %v", c.err.Error(), got, c.want)
		}
	}
}

func TestWithTimeout(t *testing.T) {
	ctx, cancel := WithTimeout(context.Background(), 50) // 50ms
	defer cancel()
	type result struct{ value int }
	// 验证 ctx 会过期
	<-ctx.Done()
	if ctx.Err() == nil {
		t.Error("ctx 应当过期")
	}
}
