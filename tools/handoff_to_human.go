package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/storage"
)

// HandoffToHumanTool 把顾客转接给人工客服（MVP 兜底能力）
//
// 设计动机：
//   - PRD §11 / MVP 第 5 项要求"复杂问题转人工"，Agent 不能硬扛所有场景
//   - 当前实现是"伪 handoff"：写一条埋点 + 提示商户联系方式，不实际触发企业微信客服会话
//   - 后续可对接第三方客服系统（微信客服、udesk 等）做真转接，工具签名保持不变
//
// 调用语义：
//   - ref_id = 顾客标识（从 ctx.wechat_open_id 取，或 fallback 到 customer 参数）
//   - meta = { reason, last_user_message, attempted_actions }
//   - 工具返回的是给 Agent 看的"成功摘要"，Agent 拿到后**用自然语言告诉顾客**
//     "已为您转人工，请稍候"，而不是把工具原始输出贴给顾客
//
// 约束：
//   - Agent Instruction 里强调"不要没事就调"，只有 3 类场景才允许：
//     1) 顾客明确说"找人工"/"叫老板来"/"投诉"
//     2) 业务超出 Agent 能力（投诉、退款、改价等 Agent 没有 tool 的事）
//     3) 连续 2 轮对话 Agent 都无法识别意图
//
// v4.13.5 修复：**per-refID 去重**——LLM 偶尔会在 1 次 Run 里多次调 handoff 工具
// （拿到第一次的"已转人工"返回后还会重试），导致 event_logs 表里同一个顾客
// 出现 N 条 handoff 埋点，admin 后台看着像"莫名奇妙重复增加"。
// 修复方案：进程内 sync.Map 按 refID（顾客稳定标识）去重，5 分钟窗口内只写 1 次。
// - 进程重启 dedup 状态会丢——但重启不常有，可接受
// - 不同顾客（不同 refID）互不影响
// - 5 分钟后再触发算新事件（避免"无限延后"）
type HandoffToHumanTool struct{}

// handoffDedup per-refID 去重状态（v4.13.5 加）
//   - key = refID（顾客稳定标识，优先 external_user_id）
//   - value = 上次写入时间
//   - 5 分钟内调 handoff 只写 1 次埋点（但工具仍 return 成功）
var handoffDedup sync.Map

// handoffDedupWindow dedup 窗口（5 分钟）
//
// 调参参考：
//   - 太短（< 1 min）：去重效果差，LLM 多次调仍可能写多条
//   - 太长（> 30 min）：商户在后台看到"老事件"，反应迟钝
//   - 5 min 平衡：商户在后台看到新事件 + 防 LLM 短时间循环调
const handoffDedupWindow = 5 * time.Minute

// handoffDedupReset 测试用：清空 dedup 状态
func handoffDedupReset() {
	handoffDedup.Range(func(key, _ any) bool {
		handoffDedup.Delete(key)
		return true
	})
}

// Info 返回工具信息
func (t *HandoffToHumanTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "handoff_to_human",
		Desc: "把当前顾客转接给人工客服（店主/真人）。" +
			"**只有 3 类场景才调用本工具**：\n" +
			"  1) 顾客明确要求找人工（如\"叫老板来\"\"我要投诉\"）；\n" +
			"  2) 顾客的需求超出 Agent 能力（投诉处理、退款、改价等没有对应工具的事）；\n" +
			"  3) 连续 2 轮 Agent 都没识别出顾客意图。\n" +
			"**不要**因为顾客抱怨排队久、或者自己答不上来就随便调。\n" +
			"调用后商户会在后台看到一条转人工记录，并主动联系顾客。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"customer": {
				Type: "string",
				Desc: "顾客姓名或标识（用于商户在后台识别是谁）。" +
					"如果 Agent 已经从对话上下文里拿到了顾客姓名，填它；否则填 'unknown'。",
				Required: false,
			},
			"reason": {
				Type: "string",
				Desc: "转人工的原因（简短一句话，商户能看懂）。" +
					"例如：'顾客要求找店长'、'投诉技师手法'、'无法识别顾客意图'。",
				Required: true,
			},
			"last_user_message": {
				Type: "string",
				Desc: "顾客触发的最后一条原文（让商户知道上下文）。",
				Required: false,
			},
		}),
	}, nil
}

// InvokableRun 执行转人工
//
// 流程：
//   1) 解析参数（reason 必填，customer / last_user_message 可选）
//   2) 从 ctx 取 shop_id（工具调用必须带 shop 上下文，否则 fallback 为 "default"）
//   3) 写埋点（storage.TrackEvent）—— 商户后台 `/api/admin/events?event_type=handoff_to_human` 可查
//   4) 返回结构化摘要给 Agent，Agent 用自然语言转述给顾客
func (t *HandoffToHumanTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var params struct {
		Customer         string `json:"customer"`
		Reason           string `json:"reason"`
		LastUserMessage  string `json:"last_user_message"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &params); err != nil {
		return "", fmt.Errorf("解析参数失败: %v", err)
	}
	if strings.TrimSpace(params.Reason) == "" {
		return "", fmt.Errorf("reason 不能为空：请告诉商户为什么要转人工")
	}

	// 取 shop_id（无 ctx 时兜底 "default"，避免埋点丢失）
	shopID := ShopIDFromCtx(ctx)
	if shopID == "" {
		shopID = "default"
	}

	// 顾客标识 ref_id（v4.13.5 改：优先用 ctx 的 external_user_id，stable key for dedup）
	//
	// 旧逻辑：customer 为空时用 "unknown-{time.Now().UnixNano()}" → 每次不同，
	// dedup 永远命中不了。改成：
	//   1) ctx.external_user_id（最稳，wechat 用户唯一标识）
	//   2) customer 参数
	//   3) "unknown"（不附加时间戳，让 dedup 至少能 merge 同一会话的 unknown 事件）
	refID := strings.TrimSpace(ExternalUserIDFromCtx(ctx))
	if refID == "" {
		refID = strings.TrimSpace(params.Customer)
	}
	if refID == "" {
		refID = "unknown"
	}

	// v4.13.5 dedup：同 refID 5 分钟内只写 1 次埋点
	if last, ok := handoffDedup.Load(refID); ok {
		if time.Since(last.(time.Time)) < handoffDedupWindow {
			// 5 分钟内已 handoff 过——不写埋点，直接 return 成功提示
			// 保留这个 return 让 LLM 不会因为没看到结果而重试
			return fmt.Sprintf(
				"已为顾客 %q 发起人工转接（原因：%s；本次会话内已记录过，不再重复埋点）。请用自然语言告诉顾客已转人工，请稍候。",
				refID, params.Reason,
			), nil
		}
	}
	// 写入/更新时间戳（dedup 命中后**不**更新，避免"无限延后"）
	handoffDedup.Store(refID, time.Now())

	// 埋点
	storage.TrackEvent(ctx, shopID, storage.EventHandoffToHuman, refID, map[string]any{
		"reason":             params.Reason,
		"customer":           params.Customer,
		"last_user_message":  truncate(params.LastUserMessage, 200),
		"via":                "agent",
	})

	// 返回结构化摘要给 Agent。Agent 自己再润色转述给顾客。
	// 这里给一行"成功"提示，避免 Agent 误以为失败而反复重试。
	return fmt.Sprintf(
		"已为顾客 %q 发起人工转接（原因：%s）。请用自然语言告诉顾客已转人工，请稍候。",
		refID, params.Reason,
	), nil
}

// truncate 把字符串截断到 maxLen（避免 meta 字段过长影响 event_log 存储）
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}