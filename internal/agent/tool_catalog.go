package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/sdk/toolkit"
	"github.com/yuterigele/openbook/sensitive"
	"github.com/yuterigele/openbook/tools"
)

// toolCatalog 将 Eino 工具和公开的工具元数据绑定在同一个显式白名单中。
type toolCatalog struct {
	descriptors *toolkit.DescriptorRegistry
	tools       map[string]tool.BaseTool
}

// catalogTool 在进入旧工具前校验 Host 注入的基础上下文和声明权限。
type catalogTool struct {
	descriptor toolkit.Descriptor
	runtime    tool.InvokableTool
}

// applicationTool 将业务工具调用转发到预约应用端口，不让 Runtime 直接依赖旧工具实现。
type applicationTool struct {
	descriptor  toolkit.Descriptor
	application v1alpha1.Application
	info        *schema.ToolInfo
}

func (t *applicationTool) Info(context.Context) (*schema.ToolInfo, error) {
	if t == nil {
		return nil, fmt.Errorf("application tool is nil")
	}
	if t.info == nil {
		return &schema.ToolInfo{Name: t.descriptor.Name, Desc: t.descriptor.Description}, nil
	}
	info := *t.info
	info.Name = t.descriptor.Name
	if info.Desc == "" {
		info.Desc = t.descriptor.Description
	}
	return &info, nil
}

func (t *applicationTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if t == nil || t.application == nil {
		return "", v1alpha1.NewError(v1alpha1.ErrorCodeUnavailable, "booking application is not configured")
	}
	executionContext, ok := v1alpha1.ExecutionContextFromContext(ctx)
	if !ok {
		return "", v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "trusted execution context is required")
	}
	response, err := t.application.Execute(ctx, executionContext, v1alpha1.Call{
		Operation:  t.descriptor.Operation,
		Parameters: json.RawMessage(argumentsInJSON),
	})
	if response.Operation != t.descriptor.Operation {
		return "", v1alpha1.NewError(v1alpha1.ErrorCodeUnavailable, "booking application returned an unexpected operation")
	}
	if len(response.Data) == 0 {
		if err != nil {
			return "", err
		}
		return "", v1alpha1.NewError(v1alpha1.ErrorCodeUnavailable, "booking application returned empty data")
	}
	var result toolkit.Result
	if decodeErr := json.Unmarshal(response.Data, &result); decodeErr != nil {
		return "", v1alpha1.WrapError(v1alpha1.ErrorCodeUnavailable, "booking application returned invalid tool result", decodeErr)
	}
	if validateErr := result.Validate(t.descriptor.Budget); validateErr != nil {
		return "", v1alpha1.WrapError(v1alpha1.ErrorCodeUnavailable, "booking application returned invalid tool result", validateErr)
	}
	_ = opts
	return string(response.Data), err
}

func (t *catalogTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.runtime.Info(ctx)
}

func (t *catalogTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	executionContext, ok := v1alpha1.ExecutionContextFromContext(ctx)
	if !ok {
		return "", v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "trusted execution context is required")
	}
	if err := executionContext.Validate(); err != nil {
		return "", err
	}
	if !executionContext.HasPermission(t.descriptor.Permission) {
		return "", v1alpha1.NewError(v1alpha1.ErrorCodeForbidden, "tool permission is required")
	}
	return t.runtime.InvokableRun(ctx, argumentsInJSON, opts...)
}

// newToolCatalog 创建当前 Agent 允许使用的工具集合。
func newToolCatalog(intentTool tool.BaseTool, applications ...v1alpha1.Application) (*toolCatalog, error) {
	if intentTool == nil {
		return nil, fmt.Errorf("classify_intent tool is required")
	}
	catalog := &toolCatalog{
		descriptors: toolkit.NewDescriptorRegistry(),
		tools:       make(map[string]tool.BaseTool),
	}
	registrations := []struct {
		descriptor toolkit.Descriptor
		runtime    tool.BaseTool
	}{
		{readDescriptor("sensitive_check", "检查消息是否包含需要拦截的敏感内容", v1alpha1.OperationSensitiveCheck), &sensitive.SensitiveCheckTool{}},
		{readDescriptor("classify_intent", "将顾客消息分类为有限的预约意图", v1alpha1.OperationClassifyIntent), intentTool},
		{readDescriptor("query_schedule", "查询理发师指定日期的可预约时段", v1alpha1.OperationQueryAvailability), &tools.QueryScheduleTool{}},
		{writeDescriptor("create_appointment", "创建一条预约", v1alpha1.OperationCreateBooking), &tools.CreateAppointmentTool{}},
		{writeDescriptor("cancel_appointment", "取消顾客自己的预约", v1alpha1.OperationCancelBooking), &tools.CancelAppointmentTool{}},
		{readDescriptor("list_barbers", "查询本店可用理发师", v1alpha1.OperationListStaff), &tools.ListBarbersTool{}},
		{readDescriptor("list_services", "查询本店服务项目和价格", v1alpha1.OperationListServices), &tools.ListServicesTool{}},
		{readDescriptor("barber_leave", "查询理发师请假信息", v1alpha1.OperationQueryStaffLeave), &tools.BarberLeaveTool{}},
		{readDescriptor("get_appointment", "查询顾客自己的预约", v1alpha1.OperationListMyBookings), &tools.GetAppointmentTool{}},
		{readDescriptor("list_my_bookings", "查询顾客未来的有效预约", v1alpha1.OperationListMyBookings), &tools.ListMyBookingsTool{}},
		{readDescriptor("list_shop_holidays", "查询本店休息日", v1alpha1.OperationListShopHolidays), &tools.ListShopHolidaysTool{}},
		{writeDescriptor("handoff_to_human", "将顾客请求转交人工客服", v1alpha1.OperationHandoffToHuman), &tools.HandoffToHumanTool{}},
	}
	for _, registration := range registrations {
		if len(applications) > 0 && applications[0] != nil && applicationToolName(registration.descriptor.Name) {
			info, infoErr := registration.runtime.Info(context.Background())
			if infoErr != nil {
				return nil, fmt.Errorf("application tool %q metadata failed: %w", registration.descriptor.Name, infoErr)
			}
			registration.runtime = &applicationTool{
				descriptor:  registration.descriptor,
				application: applications[0],
				info:        info,
			}
		}
		if err := catalog.register(registration.descriptor, registration.runtime); err != nil {
			return nil, err
		}
	}
	if len(applications) > 0 && applications[0] != nil {
		descriptor := writeDescriptor("reschedule_appointment", "改约顾客自己的预约", v1alpha1.OperationRescheduleBooking)
		if err := catalog.register(descriptor, &applicationTool{descriptor: descriptor, application: applications[0]}); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

func applicationToolName(name string) bool {
	switch name {
	case "list_services", "list_barbers", "query_schedule", "create_appointment", "cancel_appointment", "list_my_bookings", "reschedule_appointment":
		return true
	default:
		return false
	}
}

func (c *toolCatalog) register(descriptor toolkit.Descriptor, runtimeTool tool.BaseTool) error {
	if runtimeTool == nil {
		return fmt.Errorf("tool %q has no runtime implementation", descriptor.Name)
	}
	info, err := runtimeTool.Info(context.Background())
	if err != nil {
		return fmt.Errorf("tool %q metadata failed: %w", descriptor.Name, err)
	}
	if info == nil || info.Name != descriptor.Name {
		return fmt.Errorf("tool %q metadata name mismatch", descriptor.Name)
	}
	runtime, ok := runtimeTool.(tool.InvokableTool)
	if !ok {
		return fmt.Errorf("tool %q is not invokable", descriptor.Name)
	}
	if err := c.descriptors.Register(descriptor); err != nil {
		return err
	}
	c.tools[descriptor.Name] = &catalogTool{descriptor: descriptor, runtime: runtime}
	return nil
}

// Tools 返回按注册名排序的 Eino 工具，供 ToolsNode 使用。
func (c *toolCatalog) Tools() []tool.BaseTool {
	if c == nil {
		return nil
	}
	descriptors := c.descriptors.List()
	result := make([]tool.BaseTool, 0, len(descriptors))
	for _, descriptor := range descriptors {
		result = append(result, c.tools[descriptor.Name])
	}
	return result
}

// RetryAttempts 返回与工具白名单一致的重试次数。
func (c *toolCatalog) RetryAttempts() map[string]int {
	result := make(map[string]int)
	if c == nil {
		return result
	}
	for _, descriptor := range c.descriptors.List() {
		result[descriptor.Name] = descriptor.Retry.MaxAttempts
	}
	return result
}

func readDescriptor(name, description string, operation v1alpha1.Operation) toolkit.Descriptor {
	return toolkit.Descriptor{
		Name: name, Description: description, Operation: operation,
		Mode: toolkit.ModeRead, Permission: v1alpha1.PermissionBookingRead,
		Retry: toolkit.ReadRetryPolicy(), Budget: toolkit.DefaultBudget,
	}
}

func writeDescriptor(name, description string, operation v1alpha1.Operation) toolkit.Descriptor {
	return toolkit.Descriptor{
		Name: name, Description: description, Operation: operation,
		Mode: toolkit.ModeWrite, Permission: v1alpha1.PermissionBookingWrite,
		Retry: toolkit.WriteRetryPolicy(), Budget: toolkit.DefaultBudget,
	}
}
