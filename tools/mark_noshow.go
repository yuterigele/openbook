package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/storage"
)

// MarkNoShowTool 把预约标记为爽约（no-show）
type MarkNoShowTool struct{}

// Info 返回工具信息
func (t *MarkNoShowTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "mark_no_show",
		Desc: "把一个已过预约时间且顾客未到店的预约标记为爽约。系统也会自动每 5 分钟扫描并标记。" +
			"仅在顾客明确说'我上次没去成'或主动告知时调用。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"appointment_id": {
				Type: "string", Desc: "预约 ID", Required: true,
			},
		}),
	}, nil
}

// InvokableRun 执行标记爽约
func (t *MarkNoShowTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var params struct {
		AppointmentID string `json:"appointment_id"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &params); err != nil {
		return "", FriendlyError(ctx, err, "参数解析失败", "mark_no_show.unmarshal")
	}
	if params.AppointmentID == "" {
		return "", fmt.Errorf("appointment_id 不能为空")
	}

	if err := EnsureDB("mark_no_show"); err != nil {
		return "", err
	}

	appt, err := storage.GetAppointment(params.AppointmentID)
	if err != nil {
		return "", fmt.Errorf("找不到这个预约（ID: %s），确认下没复制错？", params.AppointmentID)
	}
	if appt.Status == "noshow" {
		return "这个预约已经是爽约状态了", nil
	}
	if appt.Status == "cancelled" {
		return "", fmt.Errorf("这个预约已取消，不用再标爽约了")
	}
	if appt.Status == "completed" {
		return "", fmt.Errorf("这个预约已完成，不能标爽约")
	}

	now := time.Now()
	if err := storage.DB.WithContext(ctx).
		Model(&storage.Appointment{}).
		Where("id = ?", params.AppointmentID).
		Updates(map[string]interface{}{
			"status":          "noshow",
			"active_slot_key": nil,
			"updated_at":      now,
		}).Error; err != nil {
		return "", FriendlyError(ctx, err, "标记爽约失败，请稍后再试", "mark_no_show.update")
	}

	// 埋点
	storage.TrackEvent(ctx, appt.ShopID, storage.EventAppointmentNoShow, appt.ID, map[string]any{
		"customer":    appt.Customer,
		"barber_name": appt.BarberName,
		"date":        appt.Date,
		"time":        appt.Time,
		"via":         "agent",
	})

	return fmt.Sprintf("预约 %s 已标记为爽约", params.AppointmentID), nil
}

// MarkCompletedTool 把预约标记为已完成（商户在后台用，Agent 也可调）
type MarkCompletedTool struct{}

// Info 返回工具信息
func (t *MarkCompletedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "mark_completed",
		Desc: "把一个预约标记为已完成（顾客已到店剪完头发）。通常由商户在后台标记，Agent 较少调用。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"appointment_id": {
				Type: "string", Desc: "预约 ID", Required: true,
			},
		}),
	}, nil
}

// InvokableRun 执行标记完成
func (t *MarkCompletedTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var params struct {
		AppointmentID string `json:"appointment_id"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &params); err != nil {
		return "", FriendlyError(ctx, err, "参数解析失败", "mark_completed.unmarshal")
	}
	if params.AppointmentID == "" {
		return "", fmt.Errorf("appointment_id 不能为空")
	}
	if err := EnsureDB("mark_completed"); err != nil {
		return "", err
	}
	if err := storage.MarkAppointmentCompleted(ctx, params.AppointmentID); err != nil {
		return "", FriendlyError(ctx, err, "标记完成失败，请稍后再试", "mark_completed.update")
	}
	return fmt.Sprintf("预约 %s 已标记为完成", params.AppointmentID), nil
}
