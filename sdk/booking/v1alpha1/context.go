package v1alpha1

import "context"

type executionContextKey struct{}

// WithExecutionContext 将 Host 已验证的执行上下文放入调用链。
func WithExecutionContext(ctx context.Context, executionContext ExecutionContext) context.Context {
	return context.WithValue(ctx, executionContextKey{}, cloneExecutionContext(executionContext))
}

// ExecutionContextFromContext 读取 Host 注入的执行上下文。
func ExecutionContextFromContext(ctx context.Context) (ExecutionContext, bool) {
	if ctx == nil {
		return ExecutionContext{}, false
	}
	executionContext, ok := ctx.Value(executionContextKey{}).(ExecutionContext)
	if !ok {
		return ExecutionContext{}, false
	}
	return cloneExecutionContext(executionContext), true
}

func cloneExecutionContext(source ExecutionContext) ExecutionContext {
	source.Permissions = append([]Permission(nil), source.Permissions...)
	return source
}
