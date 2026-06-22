package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/storage"
)

// CancelAppointmentTool 取消预约工具（PRD §11.1 + §11.4 P3 策略联动）
//
// 行为：
//   - 调用 storage.CancelAppointmentWithPolicy(source="agent")
//   - 根据 cancel_type 给 Agent 返回不同的提示语：
//     * early_cancel → "已成功取消"
//     * late_cancel  → "已取消，但本次属于'晚退订'，下次请提前 2h 取消避免影响预约权益"
//     * after_due    → "已过预约时间无法取消；如需标记爽约请用 mark_no_show 工具"
//     * admin/system → 不从 Agent 路径触发，保留兼容
//   - 如果本次触发 BLACKLIST，自动加到 Warning 让 Agent 友好提示顾客
type CancelAppointmentTool struct{}

// Info 返回工具信息
func (t *CancelAppointmentTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "cancel_appointment",
		Desc: "取消一个已存在的预约。需要提供预约ID。\n" +
			"策略：提前 2 小时以上取消 = 免 penalty；不足 2 小时算'晚退订'（影响后续预约权益）；已过预约时间请改用 mark_no_show。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"appointment_id": {
				Type:     "string",
				Desc:     "预约ID",
				Required: true,
			},
			"reason": {
				Type: "string",
				Desc: "取消原因（可选）。如顾客主动告知（'临时有事''孩子生病'），可记录用于后续分析。",
			},
		}),
	}, nil
}

// InvokableRun 执行取消预约
func (t *CancelAppointmentTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	// 解析参数
	var params struct {
		AppointmentID string `json:"appointment_id"`
		Reason        string `json:"reason"`
	}

	if err := json.Unmarshal([]byte(argumentsInJSON), &params); err != nil {
		return "", fmt.Errorf("解析参数失败: %v", err)
	}

	// 验证参数
	if params.AppointmentID == "" {
		return "", fmt.Errorf("appointment_id 参数不能为空")
	}

	// 调用带策略的取消 API
	result, err := storage.CancelAppointmentWithPolicy(ctx, params.AppointmentID, storage.CancelSourceAgent, params.Reason)
	if err != nil {
		// 已过预约时间：特殊错误，引导 Agent 改用 mark_no_show
		if errors.Is(err, storage.ErrAfterDueCancel) {
			return "该预约已过预约时间，无法取消。" +
				"如确认顾客未到店，请改用 mark_no_show 工具标记爽约；" +
				"如顾客想换时间，请用 create_appointment 帮他重新约。", nil
		}
		return "", err
	}

	// 根据 cancel_type 拼装回复
	msg := fmt.Sprintf("预约 %s 已成功取消。", params.AppointmentID)
	switch result.CancelType {
	case storage.CancelTypeLate:
		msg += "\n\n⚠️ 注意：本次取消距离预约时间不足 2 小时，已记录为'晚退订'。" +
			"请向顾客温和提醒：'为了不影响您以后的预约权益，下次请尽量提前 2 小时取消哦~'"
		if result.Warning != "" {
			msg += "\n（策略提示：" + result.Warning + "）"
		}
	case storage.CancelTypeEarly:
		// 默认成功语即可
	case storage.CancelTypeAdmin:
		msg += "（商户后台取消）"
	case storage.CancelTypeSystem:
		msg += "（系统取消）"
	}

	// 黑名单副作用：告知 Agent
	if result.Blacklisted {
		msg += "\n\n🚫 该顾客累计晚退订/爽约达到阈值，已自动加入黑名单。" +
			"后续该顾客的所有 create_appointment 请求都会被工具层拒绝，无需 Agent 处理。"
	}

	return msg, nil
}