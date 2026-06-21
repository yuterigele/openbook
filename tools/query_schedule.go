package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// QueryScheduleTool 查询排班工具
type QueryScheduleTool struct{}

// Info 返回工具信息
func (t *QueryScheduleTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "query_schedule",
		Desc: "查询某位理发师在指定日期的可预约时段。输入理发师姓名和日期，返回该理发师当天的空闲时段列表。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"barber_name": {
				Type:     "string",
				Desc:     "理发师姓名，例如：Tony、Kevin",
				Required: true,
			},
			"date": {
				Type:     "string",
				Desc:     "查询日期，格式：YYYY-MM-DD，例如：2026-06-20",
				Required: true,
			},
		}),
	}, nil
}

// InvokableRun 执行查询排班
func (t *QueryScheduleTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	// 解析参数
	var params struct {
		BarberName string `json:"barber_name"`
		Date       string `json:"date"`
	}

	if err := json.Unmarshal([]byte(argumentsInJSON), &params); err != nil {
		return "", fmt.Errorf("解析参数失败: %v", err)
	}

	// 验证参数
	if params.BarberName == "" {
		return "", fmt.Errorf("barber_name 参数不能为空")
	}
	if params.Date == "" {
		return "", fmt.Errorf("date 参数不能为空")
	}

	// 查询理发师 + 所属店铺（用于节假日判断）
	barber, err := storage.GetBarberByName(params.BarberName)
	if err != nil {
		return "", fmt.Errorf("理发师 %s 不存在", params.BarberName)
	}
	if !barber.Active {
		return "", fmt.Errorf("理发师 %s 暂时不接单", params.BarberName)
	}

	// 节假日判断（PRD #6）
	shop, _ := storage.GetShopByID(ctx, barber.ShopID)
	if storage.IsShopHoliday(shop, params.Date) {
		return fmt.Sprintf("%s 是店铺休息日（节假日），不提供预约。", params.Date), nil
	}

	// 查询可预约时段
	availableSlots := storage.QueryAvailableSlots(params.BarberName, params.Date)

	if len(availableSlots) == 0 {
		return fmt.Sprintf("理发师 %s 在 %s 没有可预约的时段（可能已约满）", params.BarberName, params.Date), nil
	}

	// 格式化返回结果
	result := fmt.Sprintf("理发师 %s 在 %s 的可预约时段：\n", params.BarberName, params.Date)
	for i, slot := range availableSlots {
		if i > 0 {
			result += ", "
		}
		result += slot
	}

	return result, nil
}