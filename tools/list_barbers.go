package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// ListBarbersTool 列出当前店铺所有 active 理发师（PRD §11.3 P2）
//
// Agent 可以在用户没指定理发师时主动查，列出 Tony / Kevin 等姓名让用户挑。
type ListBarbersTool struct{}

// Info 返回工具信息
func (t *ListBarbersTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "list_barbers",
		Desc: "列出当前店铺所有可预约的理发师（姓名 + 技能）。当用户没指定理发师时调用，让用户挑选。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

// InvokableRun 执行
func (t *ListBarbersTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	shopID := ShopIDFromCtx(ctx)
	if shopID == "" {
		// 兜底：取第一家的 active barber
		bs, err := storage.ListActiveBarbers(ctx)
		if err != nil {
			return "", fmt.Errorf("查询理发师失败: %v", err)
		}
		return formatBarbers(bs), nil
	}
	bs, err := storage.ListBarbersByShop(ctx, shopID)
	if err != nil {
		return "", fmt.Errorf("查询理发师失败: %v", err)
	}
	return formatBarbers(bs), nil
}

func formatBarbers(bs []storage.Barber) string {
	if len(bs) == 0 {
		return "本店暂时没有可预约的理发师"
	}
	var sb strings.Builder
	sb.WriteString("本店可预约的理发师：\n")
	for i, b := range bs {
		skills := b.Skills
		if skills == "" {
			skills = "剪发"
		}
		sb.WriteString(fmt.Sprintf("  %d. %s（擅长：%s）\n", i+1, b.Name, skills))
	}
	return sb.String()
}