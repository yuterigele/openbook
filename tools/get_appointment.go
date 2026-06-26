package tools

// get_appointment.go
//
// v4.13.6：给 Agent 一个"查当前真实预约"的能力。
//
// 业务背景（prod 复现）：
//   - 顾客约了老王 11:00
//   - 老王请假，leave reschedule 把这条预约改派给 Tony（DB 真实 barber_name = Tony）
//   - 顾客又来说"改成下午两点"
//   - Agent 凭印象以为顾客约的是老王，create_appointment(barber_name=老王) 失败
//   - Agent 看到 "老王 师傅在 06-26 10:15 至 06-27 11:15 临时有事" 一脸懵
//
// 修法：让 Agent 在改/取消前先调 get_appointment 拿真实 barber_name，
//   不要再凭 history 里的旧 barber_name 拼工具调用。
//
// 关键设计：
//   - 只支持按 appointment_id 查（精确），不支持按 phone / customer 查（防越权 + 防名字撞）
//   - 返回完整字段（barber_name / date / time / service / status），Agent 一次拿到全部
//   - 隐私：customer 字段保留（Agent 已经知道是谁在对话），phone 字段**不**返回（Agent 不需要）
//   - 跨店：GetAppointment 不带 shop_id 过滤——Agent 上下文里已经隐含了 shop，靠 appointment_id 唯一性保护

import (
	"context"
	"encoding/json"
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
		Desc: "查询一个预约的当前真实状态（理发师 / 日期 / 时间 / 服务 / 状态）。\n" +
			"\n" +
			"【v4.13.6 关键调用时机】\n" +
			"  - **改时间 / 取消前必调**：history 里的 barber_name 可能是旧的（leave 改派后已变），\n" +
			"    必须用本工具拿到当前真实 barber_name，再决定 create_appointment / cancel_appointment 怎么调。\n" +
			"  - 顾客问'我约的什么'时也调这个（少用，主要靠 history）。\n" +
			"\n" +
			"【输出】\n" +
			"  - 完整返回：理发师、日期、时间、服务项目、状态（active / cancelled / completed / noshow）\n" +
			"  - **不**返回 phone 字段（Agent 不需要，避免越权）\n" +
			"  - 已取消的也会返回 status=cancelled，Agent 可据此告诉顾客'上次约的已取消'",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"appointment_id": {
				Type:     "string",
				Desc:     "预约ID（格式如 A1B2C3D），从本会话 history 里取",
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

	appt, err := storage.GetAppointment(params.AppointmentID)
	if err != nil {
		return "", fmt.Errorf("找不到预约 %s，确认下 ID 没复制错？", params.AppointmentID)
	}

	// 故意不返回 phone 字段（Agent 不需要，避免越权）
	// cancel_reason 保留：Agent 可能需要解释为啥被取消（admin 取消 / 顾客取消 / leave 改派失败）
	return fmt.Sprintf("预约 %s 当前状态：\n理发师：%s\n日期：%s\n时间：%s\n服务：%s\n状态：%s%s",
		appt.ID,
		appt.BarberName,
		appt.Date,
		appt.Time,
		appt.Service,
		appt.Status,
		formatCancelReason(appt),
	), nil
}

// formatCancelReason 拼接取消原因（如果有）
func formatCancelReason(appt *storage.Appointment) string {
	if appt.Status != "cancelled" {
		return ""
	}
	if appt.CancelReason == "" {
		return "\n取消原因：（无）"
	}
	return "\n取消原因：" + appt.CancelReason
}
