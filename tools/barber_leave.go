package tools

// barber_leave.go
//
// 查询理发师请假情况（PRD §4.1 + §11.7 P4 顾客侧感知）
//
//   - 顾客场景："Tony 明天有空吗？" / "Kevin 后天请假吗？" / "师傅什么时候回来？"
//   - 与 query_schedule 区别：
//     * query_schedule 查的是某师傅某天的可约时段（已自动扣除请假）
//     * barber_leave 查的是某师傅某段时间的请假详情（原因 + 区间）
//
// 设计：
//   - 输入：barber_name 必填，date 可选（缺省 = 今天）
//   - 输出：请假区间列表（"HH:MM-HH:MM（原因）"），无请假时明确说"无"
//   - 友好兜底：barber 不存在 / 不在 active / 跨日请假的合并提示

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/storage"
)

// BarberLeaveTool 查询理发师请假情况
type BarberLeaveTool struct{}

// Info 返回工具信息
func (t *BarberLeaveTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "barber_leave",
		Desc: "查询某位理发师在指定日期的请假安排（返回请假时段区间）。\n" +
			"\n" +
			"【调用时机】\n" +
			"  - 顾客问「Tony 明天有空吗」「Kevin 后天请假吗」「师傅什么时候回来」等；\n" +
			"  - 在 query_schedule 前调一下，提前知道是否有大段请假（避免推了一堆空档后顾客选不中）；\n" +
			"  - 顾客问「为什么 X 师傅没排班」等异常情况。\n" +
			"\n" +
"【与 query_schedule 的区别】\n" +
		"  - barber_leave 给「请假区间」详情（用于解释）；\n" +
		"  - query_schedule 给「可约时段」（用于下单）。\n" +
		"两者互补，不必都调；问「为什么没空」调 leave，问「什么时候有空」调 schedule。\n" +
		"\n" +
		"【v4.16.3 唯一源约束】\n" +
		"  - **本工具是师傅请假信息的唯一可信来源**。\n" +
		"  - 真实事故（v4.16.3）：Agent 没调本工具，凭印象对顾客说「老王 7-3 10:15-11:15 请假」，\n" +
		"    商户后台根本没有这条请假记录 → LLM 幻觉把假请假信息塞给了顾客。\n" +
		"  - 任何对顾客说「X 师傅 Y 时间在请假」前**必须**调本工具拿真实数据；\n" +
		"    没调过本工具就别说有请假，没看到返回里有的请假就**绝对不能说**有请假。\n" +
			"\n" +
			"【返回格式】（自然语言文本，不是 JSON，**请按字面念给顾客**）\n" +
			"  - 有请假：「师傅 {name} 在 {date} 的请假安排：\\n  1. HH:MM-HH:MM（师傅临时有事）\\n  ...」\n" +
			"  - 无请假：「师傅 {name} 在 {date} 没有请假安排，正常可约。」\n" +
			"  - 师傅不存在：「师傅 {name} 不在店里呢（本店有 Tony、Kevin 两位），换个试试？」\n" +
			"  - 师傅已停用：「师傅 {name} 暂时不接单了」\n" +
			"\n" +
			"【输出软约定 — 隐私硬约束】\n" +
			"  - 返回的「原因」**永远是固定文案「师傅临时有事」**，**不返回任何具体原因**\n" +
			"  - 这是 v4.13.0 隐私保护设计：商户在 admin 填的内部 Reason\n" +
			"    （可能含「痔疮手术」「陪老婆产检」等敏感字眼）**永远不返给 LLM**\n" +
			"  - **禁止**根据请假时长 / 时间点推测未在返回中明示的信息\n" +
			"    （如「他大概是生病了」「估计是感冒」→ 这种推论让顾客尴尬，**禁止**）\n" +
			"  - 返回「正常可约」时 → **继续调 query_schedule 拿可约时段**\n" +
			"  - 返回部分时段请假时 → 调 query_schedule 拿剩余可约时段\n" +
			"\n" +
			"【回复顾客的硬要求】\n" +
			"  - 把「师傅临时有事」转告顾客（**不要**画蛇添足加任何具体描述）；\n" +
			"  - 整天请假（HH:MM 接近 00:00-23:59）：主动建议「换时间 / 换其他师傅」；\n" +
			"  - 部分时段请假：点明「上午还有 / 下午没了 / X 点到 Y 点不在」；\n" +
			"  - 语气自然，**不要**机械复述「HH:MM-HH:MM（师傅临时有事）」格式。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"barber_name": {
				Type: "string",
				Desc: "理发师**精确姓名**（必须匹配 shop_barbers.name，**大小写敏感**）。\n" +
					"  - 示例：「Tony」「Kevin」\n" +
					"  - 顾客用昵称必须先映射：「托尼老师」→「Tony」、「小 Kevin」→「Kevin」\n" +
					"  - 不确定时**先调 list_barbers** 拿到准确名单再调本工具",
				Required: true,
			},
			"date": {
				Type: "string",
				Desc: "查询日期，**严格 YYYY-MM-DD 格式**（如「2026-06-25」）。\n" +
					"  - 不传 → 默认今天（服务器时区 Asia/Shanghai）\n" +
					"  - 「明天」/「后天」/「下周三」必须**先**转换为 YYYY-MM-DD 再传入\n" +
					"  - 过去日期也能查（看历史），但**业务上无价值**，避免主动查过去",
				Required: false,
			},
		}),
	}, nil
}

// InvokableRun 执行
func (t *BarberLeaveTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var params struct {
		BarberName string `json:"barber_name"`
		Date       string `json:"date"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &params); err != nil {
		return "", fmt.Errorf("解析参数失败: %v", err)
	}
	if params.BarberName == "" {
		return "", fmt.Errorf("barber_name 不能为空")
	}

	// 缺省日期 = 今天（Asia/Shanghai）
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	if params.Date == "" {
		params.Date = now.Format("2006-01-02")
	}

	// 查 barber（兼容多店）
	barber, err := storage.GetBarberByName(params.BarberName)
	if err != nil {
		return "", fmt.Errorf("师傅 %s 不在店里呢（本店有 Tony、Kevin 两位），换个试试？", params.BarberName)
	}
	if !barber.Active {
		return fmt.Sprintf("师傅 %s 暂时不接单了", params.BarberName), nil
	}

	// 计算当天 [00:00, 24:00) 区间
	dayStart, derr := time.ParseInLocation("2006-01-02", params.Date, loc)
	if derr != nil {
		return "", fmt.Errorf("date 格式错误，需 YYYY-MM-DD")
	}
	dayEnd := dayStart.Add(24 * time.Hour)

	leaves, err := storage.ListBarberLeavesInRange(ctx, barber.ID, dayStart, dayEnd)
	if err != nil {
		return "", fmt.Errorf("查询请假记录失败，请稍后重试")
	}

	// 过滤：只保留 active 状态
	activeLeaves := make([]storage.BarberLeave, 0, len(leaves))
	for _, l := range leaves {
		if l.Status == storage.LeaveStatusActive {
			activeLeaves = append(activeLeaves, l)
		}
	}

	if len(activeLeaves) == 0 {
		return fmt.Sprintf("师傅 %s 在 %s 没有请假安排，正常可约。", params.BarberName, params.Date), nil
	}

	// 渲染请假区间
	// v4.13.0 隐私保护：内部 Reason 永远不返给 LLM
	//   - Reason 是商户填的内部原因（"痔疮手术""陪老婆产检"等敏感信息）
	//   - LLM 拿到任何具体原因都可能在回复时复述给顾客 → 硬编码"师傅临时有事"
	//   - 真实原因商户在 admin 后台自己看，顾客端只看到一致的"临时有事"
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("师傅 %s 在 %s 的请假安排：\n", params.BarberName, params.Date))
	for i, l := range activeLeaves {
		startHM := l.StartAt.In(loc).Format("15:04")
		endHM := l.EndAt.In(loc).Format("15:04")
		// 不暴露 l.Reason / l.CustomerFacingReason（v4.13.0 隐私保护）
		sb.WriteString(fmt.Sprintf("  %d. %s-%s（师傅临时有事）\n", i+1, startHM, endHM))
	}
	// 主动建议
	sb.WriteString("\n建议：换时间（如上午/下午另一段）、换其他师傅，或联系店员确认。")
	return sb.String(), nil
}
