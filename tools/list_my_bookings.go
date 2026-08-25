package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/storage"
)

// ListMyBookingsTool 查询当前顾客在本店未来的 active 预约。
type ListMyBookingsTool struct{}

// Info 返回工具信息。
func (t *ListMyBookingsTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        "list_my_bookings",
		Desc:        "查询当前顾客在本店未来的有效预约，不返回手机号或内部完整 ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

// InvokableRun 执行顾客预约查询。
func (t *ListMyBookingsTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if err := EnsureDB("list_my_bookings"); err != nil {
		return "", err
	}
	shopID := ShopIDFromCtx(ctx)
	if shopID == "" {
		return "", fmt.Errorf("当前会话缺少可信门店身份，请联系门店处理")
	}
	customer, err := currentCustomer(ctx)
	if err != nil {
		return "", err
	}
	appointments, err := storage.ListActiveAppointmentsForCustomer(ctx, shopID, customer.ID)
	if err != nil {
		return "", fmt.Errorf("查询预约失败，请稍后再试")
	}
	if len(appointments) == 0 {
		return "您目前没有未来的有效预约。", nil
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "您目前有 %d 个未来预约：\n", len(appointments))
	for _, appointment := range appointments {
		fmt.Fprintf(&builder, "- 预约号：%s；师傅：%s；日期：%s；时间：%s；服务：%s\n",
			appointmentDisplayNumber(appointment.ID), appointment.BarberName,
			appointment.Date, appointment.Time, appointment.Service)
	}
	return strings.TrimSpace(builder.String()), nil
}

var _ tool.InvokableTool = (*ListMyBookingsTool)(nil)
