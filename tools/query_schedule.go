package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// QueryScheduleTool 查询排班工具
//
// PRD §11.7.10 v3.6 视觉区分：渲染时把"可约 / 师傅请假 / 已被预约"三类分开
//   - 可约 slot  → 顶部 "09:00, 10:00, 11:00, 17:00" 一行
//   - 请假占用  → "14:00-16:00（体检）" 一行 / 多行（师傅请假占用段）
//   - 已预约    → 末尾 "其余 X 个时段已被预约"，不展开明细（避免长尾刷屏）
//
// 设计理由：
//   - 之前的实现把 leave 占用的 slot 静默掉，Agent 只在末尾看到一句"已有请假"
//   - 顾客实际场景：Agent 看到 "14:00 没了" 不知道是因为有人约了还是师傅请假了
//   - 区分后 Agent 立刻能判断"换时间"还是"换师傅"——是 v3.6 关键体验改进
type QueryScheduleTool struct{}

// Info 返回工具信息
func (t *QueryScheduleTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "query_schedule",
		Desc: "查询某位理发师在指定日期的可预约时段。输入理发师姓名和日期，返回该理发师当天的空闲时段列表。\n" +
			"\n" +
			"【调用时机】\n" +
			"  - 顾客问「Tony 明天几点有空」「周末能约吗」时；\n" +
			"  - 在 create_appointment 前**必须先调**（确认时段真空闲，避免盲下被拒）。\n" +
			"\n" +
			"【业务规则】\n" +
			"  - 输出三段：「可约」「师傅请假占用」「已约满」—— Agent 可据此判断是「换时间」还是「换师傅」；\n" +
			"  - 节假日会直接告知「休息日」；\n" +
			"  - 整天请假会输出「师傅请假占用」段，让 Agent 推荐换时间/换师傅。\n" +
			"\n" +
			"【回复要求】\n" +
			"  - 把「可约」段的关键时段挑 2-3 个推给顾客（不要照搬全部）；\n" +
			"  - 如果同时有「可约」和「师傅请假占用」，主动说「下午 X-X 有师傅请假，上午还有空」；\n" +
			"  - 如果「已约满」段占大头（如 14/15 个时段都满了），提醒顾客这个师傅当天挺忙。",
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

	if err := EnsureDB("query_schedule"); err != nil {
		return "", err
	}

	// 查询理发师 + 所属店铺（用于节假日判断）
	barber, err := storage.GetBarberByName(params.BarberName)
	if err != nil {
		return "", fmt.Errorf("师傅 %s 不在店里呢（本店有 Tony、Kevin 两位），换个试试？", params.BarberName)
	}
	if !barber.Active {
		return fmt.Sprintf("%s 师傅暂时不接单了，试试 Kevin 师傅？", params.BarberName), nil
	}

	// 节假日判断（PRD #6）
	shop, _ := storage.GetShopByID(ctx, barber.ShopID)
	if storage.IsShopHoliday(shop, params.Date) {
		return fmt.Sprintf("%s 是本店休息日（节假日），换个日期试试", params.Date), nil
	}

	// 一次 SQL 拿全 available / leave blocks / booked count（v3.6 新 helper）
	breakdown := storage.QueryScheduleBreakdown(params.BarberName, params.Date)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	dayStart, _ := time.ParseInLocation("2006-01-02", params.Date, loc)
	_ = dayStart // 保留供未来 isFullDayLeave 复用

	// 统一走"可约 / 师傅请假 / 已约满"三段（v3.6 设计），整天请假也走同一路径：
	//   - Available 空 → "当天没有可预约的时段"
	//   - LeaveBlocks 非空 → 单独成段，含原因 + "换时间或换其他理发师"建议
	// 这样视觉一致，Agent 不用为"整天请假"和"部分请假"分别学习两套文案。
	var result string
	if len(breakdown.Available) > 0 {
		result = fmt.Sprintf("理发师 %s 在 %s 的可预约时段：\n  ", params.BarberName, params.Date)
		result += strings.Join(breakdown.Available, ", ")
	} else {
		result = fmt.Sprintf("理发师 %s 在 %s 当天没有可预约的时段。", params.BarberName, params.Date)
	}

	// 请假占用段（v3.6 新增，PRD §11.7.10）
	if len(breakdown.LeaveBlocks) > 0 {
		result += "\n师傅请假占用："
		parts := make([]string, 0, len(breakdown.LeaveBlocks))
		for _, lb := range breakdown.LeaveBlocks {
			if lb.Reason != "" {
				parts = append(parts, fmt.Sprintf("%s-%s（%s）", lb.StartHM, lb.EndHM, lb.Reason))
			} else {
				parts = append(parts, fmt.Sprintf("%s-%s", lb.StartHM, lb.EndHM))
			}
		}
		result += strings.Join(parts, "、")
		result += "\n（这些时段是师傅临时请假，建议换时间或换其他理发师）"
	}

	// 已约满提示（不展开明细，避免长尾刷屏）
	if breakdown.BookedCount > 0 {
		result += fmt.Sprintf("\n其余 %d 个时段均已被预约。", breakdown.BookedCount)
	}
	return result, nil
}

// toBarberLeaves / isFullDayLeave 之前是用于"整天请假走专门路径"的辅助函数
// v3.6 设计统一后，所有请假都走"可约 / 师傅请假 / 已约满"三段，不需要整天请假特殊处理。
// 这两个函数已不再被调用，保留以下两段注释作为设计决策记录：
//
// 取消原因：整天请假也走"师傅请假占用"段（LeaveBlock 里含 00:00-00:00 区间），
// 视觉上跟部分请假一致；Agent 不用学两套文案。"建议换时间或换其他理发师"
// 已经覆盖了整天请假的最优建议场景（当天不可约 + 改天或换人）。

// （函数已删除——若未来需要重新启用整天请假专门路径，可参考以下签名恢复：
//   func toBarberLeaves(blocks []storage.LeaveBlock, dayStart time.Time) []storage.BarberLeave
//   func isFullDayLeave(leaves []storage.BarberLeave, dayStart time.Time) bool
// ）

