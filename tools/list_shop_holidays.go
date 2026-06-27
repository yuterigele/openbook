package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/storage"
)

// ListShopHolidaysTool 列出本店所有节假日 + 营业时间 + 午休
//
// 业务背景：
//
//	Agent 在顾客改日期 / 拒绝某天时，需要"本店有哪些休息日"的事实依据。
//	否则 Agent 会凭 prompt 里的"前后两天"硬编码推荐日期，
//	可能把顾客推到另一个节假日（v4.16.2 真实事故：
//	  店只设 7-1、7-2 休息，Agent 推 7-1，顾客同意后又说 7-1 也放假）。
//
// v4.16.2 引入此工具，让 Agent 一次拿到完整节假日清单 + 营业时间，
//	推荐日期时排除所有休息日，避免再次踩坑。
//
// 返回字段：
//   - 节假日清单（YYYY-MM-DD，已按日期升序）
//   - 营业时间（HH:MM-HH:MM）
//   - 午休（HH:MM-HH:MM）
//   - 距今最近 3 个即将到来的节假日（让 Agent 在拒绝当天时知道"下一个假期是啥"）
type ListShopHolidaysTool struct{}

// Info 返回工具信息
func (t *ListShopHolidaysTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "list_shop_holidays",
		Desc: "列出本店所有节假日 + 营业时间 + 午休时间。\n" +
			"\n" +
			"【调用时机】\n" +
			"  - 顾客选的日期是休息日，**必须**先调本工具看清本店完整节假日清单，再推荐可约日期；\n" +
			"  - 顾客问「你们什么时候休息」「最近有假期吗」时；\n" +
			"  - 拒绝某天时，**必须**用本工具的清单排除所有节假日再推日期，**不能**凭印象推「前后两天」。\n" +
			"\n" +
			"【业务规则】\n" +
			"  - 返回节假日清单（YYYY-MM-DD）+ 营业时间 + 午休区间；\n" +
			"  - 同时返最近 3 个尚未到期的节假日（按日期升序），便于 Agent 主动告知；\n" +
			"  - **不传参数**：本工具自动用 ctx 里的 shop_id 找本店。\n" +
			"\n" +
			"【回复要求】\n" +
			"  - 节假日拒绝时，**只推本工具清单里不存在的日期**，不要 LLM 推理；\n" +
			"  - 推荐前先调 query_schedule 验证推荐日期的师傅时段真空闲，再回顾客。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

// InvokableRun 执行
func (t *ListShopHolidaysTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if err := EnsureDB("list_shop_holidays"); err != nil {
		return "", err
	}

	shopID := ShopIDFromCtx(ctx)
	var shop *storage.Shop
	if shopID != "" {
		s, err := storage.GetShopByID(ctx, shopID)
		if err == nil && s != nil {
			shop = s
		}
	}
	if shop == nil {
		// 兜底：取第一店
		shops := storage.ListAllShops(ctx)
		if len(shops) == 0 {
			return "本店信息缺失，请联系店员", nil
		}
		shop = &shops[0]
	}

	// 节假日清单（排序后）
	holidays := storage.AllShopHolidays(shop)
	var sortedHolidays []string
	for d := range holidays {
		sortedHolidays = append(sortedHolidays, d)
	}
	sort.Strings(sortedHolidays)

	// 营业时间
	openHM := fmt.Sprintf("%02d:00", shop.OpenHour)
	closeHM := fmt.Sprintf("%02d:00", shop.CloseHour)
	lunchStartHM := fmt.Sprintf("%02d:%02d", shop.LunchStart, 0)
	lunchEndHM := fmt.Sprintf("%02d:%02d", shop.LunchEnd, shop.LunchEndMin)

	// 即将到来的 3 个节假日（>= 明天）
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	var upcoming []string
	for _, d := range sortedHolidays {
		if d >= tomorrow {
			upcoming = append(upcoming, d)
			if len(upcoming) >= 3 {
				break
			}
		}
	}

	// 拼输出
	var sb strings.Builder
	sb.WriteString("本店营业信息：\n")
	sb.WriteString(fmt.Sprintf("  营业时间：%s-%s\n", openHM, closeHM))
	sb.WriteString(fmt.Sprintf("  午休：%s-%s\n", lunchStartHM, lunchEndHM))

	if len(sortedHolidays) == 0 {
		sb.WriteString("  节假日：无\n")
	} else {
		sb.WriteString(fmt.Sprintf("  节假日（共 %d 天）：%s\n", len(sortedHolidays), strings.Join(sortedHolidays, "、")))
	}

	if len(upcoming) > 0 {
		sb.WriteString(fmt.Sprintf("  即将放假：%s\n", strings.Join(upcoming, "、")))
	} else {
		sb.WriteString("  即将放假：暂无\n")
	}

	return sb.String(), nil
}