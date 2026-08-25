package toolkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
)

// Mode 描述工具是否可能改变预约状态。
type Mode string

const (
	ModeRead  Mode = "read"
	ModeWrite Mode = "write"
)

// RetryPolicy 描述工具执行失败后的最多尝试次数。
//
// 次数包含首次执行。写工具必须使用一次，避免框架自动重复提交副作用。
type RetryPolicy struct {
	MaxAttempts int
}

// ReadRetryPolicy 返回默认的只读重试策略。
func ReadRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 2}
}

// WriteRetryPolicy 返回写工具唯一允许的执行策略。
func WriteRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 1}
}

// Handler 是注册工具的确定性执行入口。
//
// ExecutionContext 必须由 Host 注入，parameters 只承载业务参数，不能被
// Handler 用来覆盖商户、门店、顾客或权限身份。
type Handler func(context.Context, v1alpha1.ExecutionContext, json.RawMessage) (Result, error)

// ToolSpec 是工具的权限、操作和执行策略元数据。
type ToolSpec struct {
	Name        string
	Description string
	Operation   v1alpha1.Operation
	Mode        Mode
	Permission  v1alpha1.Permission
	Retry       RetryPolicy
	Budget      Budget
	Handler     Handler
}

var (
	ErrInvalidToolSpec = errors.New("invalid tool spec")
	ErrDuplicateTool   = errors.New("tool is already registered")
	ErrToolNotFound    = errors.New("tool is not registered")
)

// Registry 是显式工具白名单。
type Registry struct {
	tools map[string]ToolSpec
}

// NewRegistry 创建空工具注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]ToolSpec)}
}

// Register 校验并注册工具；重复名称会被拒绝。
func (r *Registry) Register(spec ToolSpec) error {
	if r == nil {
		return fmt.Errorf("%w: registry is nil", ErrInvalidToolSpec)
	}
	if err := validateToolSpec(spec); err != nil {
		return err
	}
	if _, exists := r.tools[spec.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, spec.Name)
	}
	if spec.Budget.MaxFields == 0 && spec.Budget.MaxChars == 0 {
		spec.Budget = DefaultBudget
	}
	r.tools[spec.Name] = spec
	return nil
}

// Get 获取工具元数据的副本。
func (r *Registry) Get(name string) (ToolSpec, bool) {
	if r == nil {
		return ToolSpec{}, false
	}
	spec, ok := r.tools[name]
	return spec, ok
}

// List 返回按名称排序的工具元数据副本。
func (r *Registry) List() []ToolSpec {
	if r == nil {
		return nil
	}
	result := make([]ToolSpec, 0, len(r.tools))
	for _, spec := range r.tools {
		result = append(result, spec)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// Invoke 执行已注册工具，并将失败转换成模型安全的结果 Envelope。
func (r *Registry) Invoke(ctx context.Context, executionContext v1alpha1.ExecutionContext, name string, parameters json.RawMessage) (Result, error) {
	spec, ok := r.Get(name)
	if !ok {
		err := v1alpha1.NewError(v1alpha1.ErrorCodeInvalidOperation, "tool is not registered")
		return FromError(err), fmt.Errorf("%w: %w: %s", ErrToolNotFound, err, name)
	}
	if err := executionContext.ValidateFor(spec.Operation); err != nil {
		return FromError(err), err
	}
	if !executionContext.HasPermission(spec.Permission) {
		err := v1alpha1.NewError(v1alpha1.ErrorCodeForbidden, "tool permission is required")
		return FromError(err), err
	}

	attempts := spec.Retry.MaxAttempts
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			lastErr = v1alpha1.WrapError(v1alpha1.ErrorCodeUnknown, "tool execution was canceled", err)
			break
		}
		result, err := spec.Handler(ctx, executionContext, parameters)
		if err == nil {
			if validationErr := result.Validate(spec.Budget); validationErr != nil {
				lastErr = v1alpha1.WrapError(v1alpha1.ErrorCodeUnknown, "tool returned an invalid result", validationErr)
				break
			}
			return result, nil
		}
		lastErr = normalizeHandlerError(err)
	}
	if lastErr == nil {
		lastErr = v1alpha1.NewError(v1alpha1.ErrorCodeUnknown, "tool execution failed")
	}
	return FromError(lastErr), lastErr
}

func validateToolSpec(spec ToolSpec) error {
	if !validToolName(spec.Name) {
		return fmt.Errorf("%w: name must be lower snake case", ErrInvalidToolSpec)
	}
	if strings.TrimSpace(spec.Description) == "" {
		return fmt.Errorf("%w: description is required", ErrInvalidToolSpec)
	}
	if !spec.Operation.IsValid() {
		return fmt.Errorf("%w: unsupported operation %q", ErrInvalidToolSpec, spec.Operation)
	}
	if spec.Handler == nil {
		return fmt.Errorf("%w: handler is required", ErrInvalidToolSpec)
	}
	switch spec.Mode {
	case ModeRead:
		if spec.Permission != v1alpha1.PermissionBookingRead {
			return fmt.Errorf("%w: read tools require %q", ErrInvalidToolSpec, v1alpha1.PermissionBookingRead)
		}
	case ModeWrite:
		if spec.Permission != v1alpha1.PermissionBookingWrite {
			return fmt.Errorf("%w: write tools require %q", ErrInvalidToolSpec, v1alpha1.PermissionBookingWrite)
		}
		if spec.Retry.MaxAttempts != 1 {
			return fmt.Errorf("%w: write tools must allow exactly one attempt", ErrInvalidToolSpec)
		}
	default:
		return fmt.Errorf("%w: unsupported mode %q", ErrInvalidToolSpec, spec.Mode)
	}
	if spec.Retry.MaxAttempts < 1 {
		return fmt.Errorf("%w: max attempts must be positive", ErrInvalidToolSpec)
	}
	if spec.Mode == ModeRead && spec.Retry.MaxAttempts > 3 {
		return fmt.Errorf("%w: read tools may use at most three attempts", ErrInvalidToolSpec)
	}
	return nil
}

func validToolName(name string) bool {
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, char := range name[1:] {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

func normalizeHandlerError(err error) error {
	if v1alpha1.CodeOf(err) != v1alpha1.ErrorCodeUnknown {
		return err
	}
	return v1alpha1.WrapError(v1alpha1.ErrorCodeUnknown, "tool execution failed", err)
}
