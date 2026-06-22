package tools

// list_services.go
//
// 列出当前店铺所有可用服务（PRD §4.1 + §5 services 表）
//
//   - 输入：无（自动从 ctx 拿 shopID）
//   - 输出：服务名 + 预估时长 + 价格区间，按 sort_order ASC
//   - 场景：顾客问"你们有什么项目""价格多少""剪发要多久"时调用
//
// 错误兜底：
//   - ctx 没 shopID：降级为"取第一家的服务"（与 list_barbers 一致）
//   - DB 失败：返回友好兜底
//   - 没数据：返回空列表 + 友好提示

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// ListServicesTool 列出当前店铺所有可用服务
type ListServicesTool struct{}

// Info 返回工具信息
func (t *ListServicesTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "list_services",
		Desc: "列出本店当前可提供的服务项目（剪发/烫发/染发/护理/造型/洗吹/其他）。" +
			"返回每项服务的名称、预估时长、价格区间。\n" +
			"\n" +
			"【调用时机】\n" +
			"  - 顾客问「你们有什么项目」「有哪些服务」「价格怎么样」时主动调；\n" +
			"  - 顾客没指定服务但要预约时，调一下让他/她挑；\n" +
			"  - 顾客问「剪发要多久」「烫发多少钱」等具体问题前，先调拿到数据再答（不要凭印象答）。\n" +
			"\n" +
			"【回复要求】\n" +
			"  - 不要把列表原文照搬给顾客，用自然语言 + 关键信息（如价格区间、时长）总结；\n" +
			"  - 顾客没明确选项目时，**最多推荐 3 项**最相关的（剪发默认包含）；\n" +
			"  - 如某项没有价格信息，告诉顾客「价格请到店咨询」或「可让师傅根据发型评估」。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

// InvokableRun 执行列出服务
func (t *ListServicesTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if err := EnsureDB("list_services"); err != nil {
		return "", err
	}
	shopID := ShopIDFromCtx(ctx)
	var svcs []storage.Service
	var err error

	if shopID == "" {
		// 兜底：取第一家有服务的店（与 list_barbers 策略一致）
		svcs, err = listServicesFallback(ctx)
		if err != nil {
			return "", FriendlyError(ctx, err, "查询服务列表失败，请稍后再试", "list_services.fallback")
		}
	} else {
		svcs, err = storage.ListServicesByShop(ctx, shopID, false) // 仅 active
		if err != nil {
			return "", FriendlyError(ctx, err, "查询服务列表失败，请稍后再试", "list_services.byShop")
		}
	}

	if len(svcs) == 0 {
		return "本店暂未配置服务项目。请到店咨询店员了解可提供的项目。", nil
	}

	// 渲染为 Agent 易读的格式（含 few-shot）
	var sb strings.Builder
	sb.WriteString("本店可提供的服务项目（按热门顺序）：\n")
	for i, s := range svcs {
		price := strings.TrimSpace(s.PriceRange)
		priceSuffix := ""
		if price == "" || price == "0-0" {
			priceSuffix = "（价格请到店咨询）"
		} else {
			priceSuffix = "（价格约 " + price + " 元）"
		}
		durSuffix := fmt.Sprintf("约 %d 分钟", s.EstimatedMin)
		if s.EstimatedMin <= 0 {
			durSuffix = "时长待定"
		}
		sb.WriteString(fmt.Sprintf("  %d. %s %s，%s\n", i+1, s.Name, priceSuffix, durSuffix))
	}
	sb.WriteString("\n（推荐：剪发是大多数顾客的首选；烫发/染发需提前预留 90 分钟）")
	return sb.String(), nil
}

// listServicesFallback 兜底：取所有店中第一家有服务的店
func listServicesFallback(ctx context.Context) ([]storage.Service, error) {
	if storage.DB == nil {
		return nil, fmt.Errorf("DB 未初始化")
	}
	var shops []storage.Shop
	if err := storage.DB.WithContext(ctx).Find(&shops).Error; err != nil {
		return nil, err
	}
	for _, s := range shops {
		svcs, err := storage.ListServicesByShop(ctx, s.ID, false)
		if err == nil && len(svcs) > 0 {
			return svcs, nil
		}
	}
	return []storage.Service{}, nil
}
