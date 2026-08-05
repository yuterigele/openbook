package tools

// get_appointment.go 为 Agent 提供查询当前真实预约的能力。
//
// 业务背景（生产环境复现）：
//   - 顾客约了老王 11:00
//   - 老王请假后，改约流程把该预约改派给 Tony。
//   - 顾客随后要求改到下午两点，Agent 若使用历史中的老王创建预约会失败。
//
// 改时间或取消前调用 get_appointment 获取当前真实理发师，
// 不使用历史消息中的旧理发师信息构造工具调用。
//
// 关键设计：
//   - 仅支持按预约 ID 精确查询，并绑定服务端验证过的顾客身份与门店。
//   - 返回理发师、日期、时间、服务和状态；不返回手机号。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/storage"
)

// GetAppointmentTool 查询预约当前真实状态
type GetAppointmentTool struct{}

// Info 返回工具信息
func (t *GetAppointmentTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "get_appointment",
		Desc: "查询当前预约的理发师、日期、时间、服务和状态。改时间或取消前必须调用，以工具结果为准；顾客询问已有预约时也可调用。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"appointment_id": {
				Type:     "string",
				Desc:     "预约号（格式如 OB-A1B2C3D4）或本会话中的预约ID",
				Required: true,
			},
		}),
	}, nil
}

// InvokableRun 执行查询
func (t *GetAppointmentTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var params struct {
		AppointmentID string `json:"appointment_id"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &params); err != nil {
		return "", fmt.Errorf("解析参数失败: %v", err)
	}
	if params.AppointmentID == "" {
		return "", fmt.Errorf("appointment_id 参数不能为空")
	}

	if err := EnsureDB("get_appointment"); err != nil {
		return "", err
	}

	customer, err := currentCustomer(ctx)
	if err != nil {
		return "", err
	}
	appt, err := resolveCustomerAppointment(ctx, params.AppointmentID, ShopIDFromCtx(ctx), customer.ID)
	if err != nil {
		return "", fmt.Errorf("找不到属于您的预约，确认下预约号是否正确？")
	}

	// 不返回手机号，避免泄露不需要的个人信息。
	// 保留取消原因，便于向顾客解释预约为何取消。
	return fmt.Sprintf("预约号：%s\n当前状态：\n理发师：%s\n日期：%s\n时间：%s\n服务：%s\n状态：%s%s",
		appointmentDisplayNumber(appt.ID),
		appt.BarberName,
		appt.Date,
		appt.Time,
		appt.Service,
		appt.Status,
		formatCancelReason(appt),
	), nil
}

func currentCustomer(ctx context.Context) (*storage.Customer, error) {
	customer, err := storage.GetCustomerByMessagingIdentity(ctx, OpenIDFromCtx(ctx), ExternalUserIDFromCtx(ctx))
	if err == nil {
		return customer, nil
	}
	if errors.Is(err, storage.ErrCustomerIdentityRequired) {
		return nil, fmt.Errorf("当前会话未完成身份验证，无法处理预约查询或取消")
	}
	return nil, fmt.Errorf("找不到当前顾客身份，无法处理该预约")
}

// formatCancelReason 在有取消原因时拼接展示文本。
func formatCancelReason(appt *storage.Appointment) string {
	if appt.Status != "cancelled" {
		return ""
	}
	if appt.CancelReason == "" {
		return "\n取消原因：（无）"
	}
	return "\n取消原因：" + appt.CancelReason
}
