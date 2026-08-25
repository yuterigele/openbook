package toolkit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
)

// Descriptor 是不依赖具体 Runtime 的工具元数据。
type Descriptor struct {
	Name        string
	Description string
	Operation   v1alpha1.Operation
	Mode        Mode
	Permission  v1alpha1.Permission
	Retry       RetryPolicy
	Budget      Budget
}

// Validate 校验工具的注册边界，但不要求提供具体执行 Handler。
func (d Descriptor) Validate() error {
	if !validToolName(d.Name) {
		return fmt.Errorf("%w: name must be lower snake case", ErrInvalidToolSpec)
	}
	if strings.TrimSpace(d.Description) == "" {
		return fmt.Errorf("%w: description is required", ErrInvalidToolSpec)
	}
	if !d.Operation.IsValid() {
		return fmt.Errorf("%w: unsupported operation %q", ErrInvalidToolSpec, d.Operation)
	}
	switch d.Mode {
	case ModeRead:
		if d.Permission != v1alpha1.PermissionBookingRead {
			return fmt.Errorf("%w: read tools require %q", ErrInvalidToolSpec, v1alpha1.PermissionBookingRead)
		}
	case ModeWrite:
		if d.Permission != v1alpha1.PermissionBookingWrite {
			return fmt.Errorf("%w: write tools require %q", ErrInvalidToolSpec, v1alpha1.PermissionBookingWrite)
		}
		if d.Retry.MaxAttempts != 1 {
			return fmt.Errorf("%w: write tools must allow exactly one attempt", ErrInvalidToolSpec)
		}
	default:
		return fmt.Errorf("%w: unsupported mode %q", ErrInvalidToolSpec, d.Mode)
	}
	if d.Retry.MaxAttempts < 1 {
		return fmt.Errorf("%w: max attempts must be positive", ErrInvalidToolSpec)
	}
	if d.Mode == ModeRead && d.Retry.MaxAttempts > 3 {
		return fmt.Errorf("%w: read tools may use at most three attempts", ErrInvalidToolSpec)
	}
	return nil
}

// DescriptorFromSpec 提取执行工具的公开元数据。
func DescriptorFromSpec(spec ToolSpec) Descriptor {
	return Descriptor{
		Name:        spec.Name,
		Description: spec.Description,
		Operation:   spec.Operation,
		Mode:        spec.Mode,
		Permission:  spec.Permission,
		Retry:       spec.Retry,
		Budget:      spec.Budget,
	}
}

// DescriptorRegistry 是 Eino 等具体 Runtime 装配工具时使用的显式元数据白名单。
type DescriptorRegistry struct {
	descriptors map[string]Descriptor
}

// NewDescriptorRegistry 创建空的元数据注册表。
func NewDescriptorRegistry() *DescriptorRegistry {
	return &DescriptorRegistry{descriptors: make(map[string]Descriptor)}
}

// Register 校验并注册工具元数据；重复名称会被拒绝。
func (r *DescriptorRegistry) Register(descriptor Descriptor) error {
	if r == nil {
		return fmt.Errorf("%w: registry is nil", ErrInvalidToolSpec)
	}
	if err := descriptor.Validate(); err != nil {
		return err
	}
	if descriptor.Budget.MaxFields == 0 && descriptor.Budget.MaxChars == 0 {
		descriptor.Budget = DefaultBudget
	}
	if _, exists := r.descriptors[descriptor.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, descriptor.Name)
	}
	r.descriptors[descriptor.Name] = descriptor
	return nil
}

// Get 获取元数据副本。
func (r *DescriptorRegistry) Get(name string) (Descriptor, bool) {
	if r == nil {
		return Descriptor{}, false
	}
	descriptor, ok := r.descriptors[name]
	return descriptor, ok
}

// List 返回按名称排序的元数据副本。
func (r *DescriptorRegistry) List() []Descriptor {
	if r == nil {
		return nil
	}
	result := make([]Descriptor, 0, len(r.descriptors))
	for _, descriptor := range r.descriptors {
		result = append(result, descriptor)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}
