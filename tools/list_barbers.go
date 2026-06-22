package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// ListBarbersTool 列出当前店铺所有 active 理发师（PRD §11.3 P2）
//
// Agent 可以在用户没指定理发师时主动查，列出 Tony / Kevin 等姓名让用户挑选。
// 如果某理发师今天有请假，会标注「（今日 HH:MM 起请假 / HH:MM-HH:MM 请假）」
// ——PRD §11.7.9 v3.6 让顾客在选人阶段就知道哪位不能约，减少后续 reject。
type ListBarbersTool struct{}

// Info 返回工具信息
func (t *ListBarbersTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "list_barbers",
		Desc: "列出当前店铺所有可预约的理发师（姓名 + 技能 + 当日请假状态）。\n" +
			"\n" +
			"【调用时机】\n" +
			"  - 顾客没指定理发师但要预约时，先调让顾客挑；\n" +
			"  - 顾客问「你们有谁」「师傅都有谁」时。\n" +
			"\n" +
			"【业务规则】\n" +
			"  - 如果某理发师今日有请假，工具会明确标注「今日 HH:MM 起请假」或「今日 HH:MM-HH:MM 请假」；\n" +
			"  - 已停用（inactive）的师傅不列。\n" +
			"\n" +
			"【回复要求】\n" +
			"  - 不要把列表原文照搬，按理发师人数给推荐：\n" +
			"    * 1-2 位：直接全列；\n" +
			"    * 3 位以上：只推今天全勤的，请假的标到末尾说「XXX 师傅今天 X 点起请假」；\n" +
			"  - 顾客对某师傅有偏好的（VIP/常客/特定剪发习惯），优先推。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

// InvokableRun 执行
func (t *ListBarbersTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if err := EnsureDB("list_barbers"); err != nil {
		return "", err
	}
	shopID := ShopIDFromCtx(ctx)
	if shopID == "" {
		// 兜底：取第一家的 active barber
		bs, err := storage.ListActiveBarbers(ctx)
		if err != nil {
			return "", FriendlyError(ctx, err, "查询师傅名单失败，请稍后再试", "list_barbers.fallback")
		}
		return formatBarbers(ctx, bs), nil
	}
	bs, err := storage.ListBarbersByShop(ctx, shopID)
	if err != nil {
		return "", FriendlyError(ctx, err, "查询师傅名单失败，请稍后再试", "list_barbers.byShop")
	}
	return formatBarbers(ctx, bs), nil
}

func formatBarbers(ctx context.Context, bs []storage.Barber) string {
	if len(bs) == 0 {
		return "本店暂时没有可预约的师傅呢，请联系店员了解"
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	dayEnd := dayStart.Add(24 * time.Hour)

	var sb strings.Builder
	sb.WriteString("本店可预约的理发师：\n")
	for i, b := range bs {
		skills := b.Skills
		if skills == "" {
			skills = "剪发"
		}
		leaveTag := barberLeaveTag(ctx, b.ID, now, dayStart, dayEnd, loc)
		if leaveTag == "" {
			sb.WriteString(fmt.Sprintf("  %d. %s（擅长：%s）\n", i+1, b.Name, skills))
		} else {
			sb.WriteString(fmt.Sprintf("  %d. %s（擅长：%s，%s）\n", i+1, b.Name, skills, leaveTag))
		}
	}
	return sb.String()
}

// barberLeaveTag 返回某理发师在「今天」窗口内的请假提示文案
//
//   - 无请假：返回空字符串
//   - 当前正在请假：返回「今日 HH:MM-HH:MM 请假（原因：xxx）」
//   - 即将请假（start_at 在未来）：返回「今日 HH:MM 起请假（原因：xxx）」
//
// 实现说明：
//   - 用 ListBarberLeavesInRange(barberID, dayStart, dayEnd) 拿到今天相交的 active leave
//   - 把落在「现在」左侧 / 右侧 区分文案（正在 vs 即将），更贴近顾客视角
func barberLeaveTag(ctx context.Context, barberID string, now, dayStart, dayEnd time.Time, loc *time.Location) string {
	leaves, err := storage.ListBarberLeavesInRange(ctx, barberID, dayStart, dayEnd)
	if err != nil || len(leaves) == 0 {
		return ""
	}
	// 取今天最先 / 最相关的一条（ListBarberLeavesInRange 已按 start_at ASC）
	l := leaves[0]
	startHM := l.StartAt.In(loc).Format("15:04")
	endHM := l.EndAt.In(loc).Format("15:04")
	reason := strings.TrimSpace(l.Reason)
	reasonSuffix := ""
	if reason != "" {
		reasonSuffix = "（原因：" + reason + "）"
	}
	// 已开始（含 now）-> 显示完整区间；未开始 -> 显示"HH:MM 起"
	if !now.Before(l.StartAt) {
		return fmt.Sprintf("今日 %s-%s 请假%s", startHM, endHM, reasonSuffix)
	}
	return fmt.Sprintf("今日 %s 起请假%s", startHM, reasonSuffix)
}