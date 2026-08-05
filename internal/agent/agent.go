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

// Package agent 构建预约助手的 eino-ADK Agent。
//
// 本包位于 internal 下，不属于公开 API；仅供 main.go 和测试引用。
//
// 核心能力：模型降级、敏感内容预过滤和意图分类。
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

// buildAgentInstruction 构造精简版系统提示词。确定性校验由工具层负责。
func buildAgentInstruction() string {
	return `你是美发预约助手，只处理本店预约。

【优先级】
1. 每轮先调用 sensitive_check；blocked=true 时原样回复 reason，停止。
2. 排班、请假、价格、师傅姓名和预约状态只信本轮工具结果；历史消息不是事实。昵称先用 list_barbers 确认。
3. 相对日期严格使用紧邻用户消息的系统时间锚点；不得使用示例或历史日期。

【工具流程】
- 预约：确认师傅、日期、时间、服务；先 query_schedule，空闲后 create_appointment。
- 取消/改约：优先从 history 找最近预约；顾客提供“OB-”开头的预约号时可直接传给 get_appointment。先获取真实状态，再依次取消旧预约、查新时段、创建新预约。
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
- 创建或改约成功必须告知预约号，并提示截图保存。顾客可见回复使用纯文本，不要使用 Markdown 标记；预约信息标题写作“📋 预约信息”，不可展示完整 UUID。
- 默认服务为剪发；“3点”按 15:00 处理。`
}

// BuildTyped 构造美发预约助手 Agent，并注册预约业务工具、敏感内容检查和意图分类工具。
// BuildTyped 构造预约助手 Agent。调用方传入 main.go 中创建的意图分类工具，
// 使 Agent 能根据分类结果分流。
//
// M 是 eino 的消息类型：聊天使用 *schema.Message，工具循环使用
// *schema.AgenticMessage；调用方在边界处选择具体类型。
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
					&sensitive.SensitiveCheckTool{}, // 输入预过滤；命中后直接返回原因。
					intentTool,                      // v4.17+：双层意图分类
					&tools.QueryScheduleTool{},
					&tools.CreateAppointmentTool{},
					&tools.CancelAppointmentTool{},
					&tools.ListBarbersTool{},
					&tools.ListServicesTool{},
					&tools.BarberLeaveTool{},
					&tools.GetAppointmentTool{},   // 改时间前核验当前预约。
					&tools.ListShopHolidaysTool{}, // 节假日推荐前获取完整休息日清单。
					&tools.HandoffToHumanTool{},
				},
			},
		},
	}
	helpers.ApplyMessageModelRetry(cfg)
	cfg.ModelFailoverConfig = chatmodel.NewRuntimeFailoverConfig[M](ctx, used)
	return deep.NewTyped[M](ctx, cfg)
}
