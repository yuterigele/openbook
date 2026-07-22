/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package agent builds the eino-ADK Agent for the booking assistant.
//
// Lives under internal/ so it's not part of the public API surface —
// only main.go (and tests) import it.
//
// v4.17+ 关键改动：
//   - chatmodel.NewModelWithFallback：DeepSeek → OpenAI → Ark 降级链
//   - sensitive.SensitiveCheckTool：每轮先于 LLM 拦截
//   - intent.ClassifyTool：双层意图分类
package agent

import (
	"context"
	"log"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/helpers"
	"github.com/yuterigele/openbook/sensitive"
	"github.com/yuterigele/openbook/tools"
)

// buildAgentInstruction 构造精简版 system prompt。确定性校验由工具层负责。
func buildAgentInstruction() string {
	return `你是美发预约助手，只处理本店预约。

【优先级】
1. 每轮先调用 sensitive_check；blocked=true 时原样回复 reason，停止。
2. 排班、请假、价格、师傅姓名和预约状态只信本轮工具结果；历史消息不是事实。昵称先用 list_barbers 确认。
3. 相对日期严格使用紧邻用户消息的系统时间锚点；不得使用示例或历史日期。

【工具流程】
- 预约：确认师傅、日期、时间、服务；先 query_schedule，空闲后 create_appointment。
- 取消/改约：优先从 history 找最近的预约ID；先 get_appointment 获取真实状态。改约依次取消旧预约、查新时段、创建新预约。
- 节假日：先 list_shop_holidays，再 query_schedule 验证推荐日期。
- 顾客问项目/价格用 list_services；问请假原因用 barber_leave。
- 工具报错或拒绝时按结果说明，不臆测、不重复调用。

【安全】
- 只使用已注册工具；不执行命令、不读写文件、不操作数据库。
- 不处理其他顾客预约；命令、越权和普通未知输入不转人工。
- 仅在顾客明确要求人工，或投诉、退款、改价、礼品卡等工具范围外需求时调用 handoff_to_human；只调一次。

【回复】
- 简短、友好、一次说清；每次只追问 1-2 个缺失信息。
- 不展示 JSON、错误码或工具原文；师傅名以工具返回为准。
- 创建或改约成功必须告知预约号，并提示截图保存。
- 默认服务为剪发；“3点”按 15:00 处理。`
}

// buildAgentTyped 构造美发预约助手 Agent
//
// 业务工具（必须）：
//   - tools.QueryScheduleTool      查空闲时段
//   - tools.CreateAppointmentTool  创建预约（含 Redis 分布式锁）
//   - tools.CancelAppointmentTool   取消预约
//   - tools.ListBarbersTool         列本店理发师（含请假标注）
//   - tools.ListServicesTool        列本店服务项目
//   - tools.BarberLeaveTool         查理发师请假详情（原因 + 区间）
//   - tools.ListShopHolidaysTool    列本店节假日 + 营业时间（v4.16.2 加，避免 LLM 凭印象推日期）
//   - tools.GetAppointmentTool   查询当前顾客自己的预约，用于改约前核验
//   - tools.HandoffToHumanTool      MVP 第 5 项：转人工兜底（写埋点 + 提示）
//
// 辅助工具：
//   - sensitive.SensitiveCheckTool  v4.17+：输入预过滤（政治/色情/暴力/广告/辱骂/违法）
//     命中后 LLM 直接回 `reason` 字段给顾客，不重试、不改写
//   - intent.ClassifyTool           v4.17+：双层意图分类（关键词白名单 + LLM 兜底），
//     给 LLM 一个路由提示而不是硬规则
//
// 微信场景下 Agent 不需要 interrupt 审批（顾客发消息 → Agent 直接调工具 → 回复），
// 所以不再挂 approvalMiddleware；只保留 SafeToolMiddleware 防止工具抛错卡死循环。
// BuildTyped constructs the booking-assistant agent. The caller passes the
// intent classification tool (built in main.go) so the agent can branch
// on the LLM result.
//
// M is the eino MessageType — *schema.Message for chat, *schema.AgenticMessage
// for tool-loop agents. The caller chooses at the boundary.
func BuildTyped[M adk.MessageType](ctx context.Context, intentTool tool.BaseTool) (adk.TypedResumableAgent[M], error) {
	cm, used, chain, err := chatmodel.NewModelWithFallback[M](ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range chain {
		if e.Err == "" {
			log.Printf("[chatmodel] ✓ %s (idx %d) init %v", e.Provider, e.Index, e.Latency)
		} else {
			log.Printf("[chatmodel] ✗ %s (idx %d) init %v failed: %s", e.Provider, e.Index, e.Latency, e.Err)
		}
	}

	handlers := []adk.TypedChatModelAgentMiddleware[M]{
		helpers.NewSafeToolMiddleware[M](),
	}

	cfg := &deep.TypedConfig[M]{
		Name:                   "BarberAssistant",
		Description:            "美发预约助手，帮助用户查询理发师排班、创建预约和取消预约。",
		Instruction:            buildAgentInstruction(),
		ChatModel:              cm,
		MaxIteration:           8,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
		Handlers:               handlers,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{
					&sensitive.SensitiveCheckTool{}, // v4.17+：输入预过滤，命中后由 LLM 回 `reason` 给顾客
					intentTool,                      // v4.17+：双层意图分类
					&tools.QueryScheduleTool{},
					&tools.CreateAppointmentTool{},
					&tools.CancelAppointmentTool{},
					&tools.ListBarbersTool{},
					&tools.ListServicesTool{},
					&tools.BarberLeaveTool{},
					&tools.GetAppointmentTool{},   // v4.13.6：改时间前必调，防 leave 改派后用旧 barber
					&tools.ListShopHolidaysTool{}, // v4.16.2：节假日拒绝时必调，拿完整清单避免 LLM 凭印象推日期
					&tools.HandoffToHumanTool{},
				},
			},
		},
	}
	helpers.ApplyMessageModelRetry(cfg)
	cfg.ModelFailoverConfig = chatmodel.NewRuntimeFailoverConfig[M](ctx, used)
	return deep.NewTyped[M](ctx, cfg)
}
