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

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// BarberLeaveTool 查询理发师请假情况
type BarberLeaveTool struct{}

// Info 返回工具信息
func (t *BarberLeaveTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "barber_leave",
		Desc: "查询某位理发师在指定日期的请假安排。\n" +
			"\n" +
			"【调用时机】\n" +
			"  - 顾客问「Tony 明天有空吗」「Kevin 后天请假吗」「师傅什么时候回来」等；\n" +
			"  - 在 query_schedule 前调一下，提前知道是否有大段请假（避免推了一堆空档后顾客选不中）；\n" +
			"  - 顾客问「为什么 X 师傅没排班」等异常情况。\n" +
			"\n" +
			"【与 query_schedule 的区别】\n" +
			"  - barber_leave 给「原因 + 区间」详情（用于解释）；\n" +
			"  - query_schedule 给「可约时段」（用于下单）。\n" +
			"两者互补，不必都调；问「为什么没空」调 leave，问「什么时候有空」调 schedule。\n" +
			"\n" +
			"【回复要求】\n" +
			"  - 把请假原因也告诉顾客（顾客最关心）；\n" +
			"  - 整天请假时主动建议换时间或换其他师傅；\n" +
			"  - 半天请假时主动说「上午还有空 / 下午空了」。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"barber_name": {
				Type:     "string",
				Desc:     "理发师姓名，例如：Tony、Kevin",
				Required: true,
			},
			"date": {
				Type:     "string",
				Desc:     "查询日期，格式：YYYY-MM-DD，例如：2026-06-20；不传默认今天",
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
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("师傅 %s 在 %s 的请假安排：\n", params.BarberName, params.Date))
	for i, l := range activeLeaves {
		startHM := l.StartAt.In(loc).Format("15:04")
		endHM := l.EndAt.In(loc).Format("15:04")
		reason := strings.TrimSpace(l.Reason)
		if reason == "" {
			reason = "（未注明原因）"
		}
		sb.WriteString(fmt.Sprintf("  %d. %s-%s（%s）\n", i+1, startHM, endHM, reason))
	}
	// 主动建议
	sb.WriteString("\n建议：换时间（如上午/下午另一段）、换其他师傅，或联系店员确认。")
	return sb.String(), nil
}
