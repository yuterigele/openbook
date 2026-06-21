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

package main

import (
	"context"
	"fmt"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/chatmodel"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/helpers"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/rag"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/tools"
)

// buildAgentTyped 构造美发预约助手 Agent
//
// 业务工具（必须）：
//   - tools.QueryScheduleTool      查空闲时段
//   - tools.CreateAppointmentTool  创建预约（含 Redis 分布式锁）
//   - tools.CancelAppointmentTool   取消预约
//   - tools.ListBarbersTool         列本店理发师（含请假标注）
//   - tools.MarkNoShowTool / tools.MarkCompletedTool  标记爽约/完成
//   - tools.HandoffToHumanTool      MVP 第 5 项：转人工兜底（写埋点 + 提示）
//
// 辅助工具：
//   - ragTool                       RAG（理发店知识问答，可选）
//
// 微信场景下 Agent 不需要 interrupt 审批（顾客发消息 → Agent 直接调工具 → 回复），
// 所以不再挂 approvalMiddleware；只保留 SafeToolMiddleware 防止工具抛错卡死循环。
func buildAgentTyped[M adk.MessageType](ctx context.Context) (adk.TypedResumableAgent[M], error) {
	cm, err := chatmodel.NewModel[M](ctx)
	if err != nil {
		return nil, err
	}

	backend, err := localbk.NewBackend(ctx, &localbk.Config{})
	if err != nil {
		return nil, err
	}

	ragTool, err := rag.BuildTool[M](ctx, cm)
	if err != nil {
		return nil, fmt.Errorf("build rag tool: %w", err)
	}

	handlers := []adk.TypedChatModelAgentMiddleware[M]{
		helpers.NewSafeToolMiddleware[M](),
	}

	cfg := &deep.TypedConfig[M]{
		Name:        "BarberAssistant",
		Description: "美发预约助手，帮助用户查询理发师排班、创建预约和取消预约。",
		Instruction: "你是一个友好的美发预约助手，名叫小助理。你可以帮用户查询理发师的空闲时段、创建预约和取消预约。\n\n" +
			"顾客等级（通过 customer 参数识别；create_appointment 工具会自动拦截黑名单）：\n" +
			"  - VIP 客户：用尊称、主动提供 2 个以上的可选时段让其挑选、避免排队\n" +
			"  - FREQUENT 常客（累计 ≥5 次）：用熟称、提醒他/她上次预约的理发师是谁\n" +
			"  - BLACKLIST 黑名单：工具会自动拒绝，你只需回复'很抱歉，本店暂无法为您服务'\n" +
			"  - NEW 新客户：耐心引导，多介绍服务项目\n\n" +
			"注意：你必须使用工具中的日期参数，不要凭空猜测日期（日期上下文会由调用方在第一条 user 消息里给你）。\n" +
			"你的能力：\n" +
			"- 查询理发师在指定日期的可预约时段\n" +
			"- 为顾客创建新的预约\n" +
			"- 取消已有的预约\n\n" +
			"可用理发师：以数据库实际为准（默认 Tony、Kevin）\n\n" +
			"工作时间：每天 09:00-18:00，每半小时一个时段，午休时间（12:00-13:30）不可预约。\n\n" +
			"对话策略：\n" +
			"1. 理解用户意图：用户可能会用日常口语表达需求，比如\"明天下午有时间吗，我想剪头发\"。\n" +
			"2. 主动追问：如果用户没有提供必要的信息（如理发师姓名、具体时间），请礼貌地追问。\n" +
			"3. 提供选项：当用户没有指定理发师时，可以同时查询所有理发师的空闲时段供用户选择。\n" +
			"4. 确认信息：在创建预约前，向用户确认所有关键信息（理发师、日期、时间、服务项目）。\n" +
			"5. 友好回复：用自然、友好的语言与用户交流，不要太机械。\n\n" +
			"人工兜底（MVP 第 5 项）：\n" +
			"当顾客的需求你**确实解决不了**时，调用 handoff_to_human 工具把顾客转给人工客服。\n" +
			"允许调用的 3 类场景：\n" +
			"  1) 顾客明确要求找人工（如\"叫老板来\"\"我要投诉\"）；\n" +
			"  2) 顾客的需求超出你的工具能力（投诉处理、退款、改价、礼品卡等）—— 你没有对应工具就老实转；\n" +
			"  3) 连续 2 轮对话你都没识别出顾客意图—— 别再死磕，直接转。\n" +
			"**严禁**：不要因为顾客语气不好、自己答不上来、或者怕麻烦就调。普通业务问题继续用工具解决。\n" +
			"调用后告诉顾客：\"好的，我帮您转给店员，请稍等\"。**不要把工具返回的原始 JSON 给顾客看**。\n\n" +
			"示例对话：\n" +
			"用户：明天下午有时间吗，我想去剪头发\n" +
			"你：您好！明天下午有不少空闲时段呢。请问您想预约哪位理发师？Tony 还是 Kevin？另外，您具体想约几点呢？\n\n" +
			"用户：Tony明天下午3点有空吗\n" +
			"你：调用 query_schedule 查询 Tony 明天的空闲时段，如果15:00空闲就说Tony 明天下午3点是空闲的哦！请问您贵姓？如果15:00已被预约就推荐 15:30 或 16:00。\n\n" +
			"用户：帮我约Tony明天下午3点，我叫小明\n" +
			"你：调用 create_appointment 创建预约，参数 barber_name=Tony, customer=小明, date=明天, time=15:00，然后告诉用户预约成功。\n\n" +
			"用户：取消预约1\n" +
			"你：调用 cancel_appointment(appointment_id=1)，然后告诉用户已取消。",
		ChatModel:      cm,
		Backend:        backend,
		StreamingShell: backend,
		MaxIteration:   20, // 微信场景不需要深度推理，20 轮足矣
		Handlers:       handlers,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{
					ragTool,
					&tools.QueryScheduleTool{},
					&tools.CreateAppointmentTool{},
					&tools.CancelAppointmentTool{},
					&tools.MarkNoShowTool{},
					&tools.MarkCompletedTool{},
					&tools.ListBarbersTool{},
					&tools.HandoffToHumanTool{},
				},
			},
		},
	}
	helpers.ApplyMessageModelRetry(cfg)
	return deep.NewTyped[M](ctx, cfg)
}