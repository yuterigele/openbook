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

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/helpers"
	"github.com/yuterigele/openbook/rag"
	"github.com/yuterigele/openbook/tools"
)

// buildAgentTyped 构造美发预约助手 Agent
//
// 业务工具（必须）：
//   - tools.QueryScheduleTool      查空闲时段
//   - tools.CreateAppointmentTool  创建预约（含 Redis 分布式锁）
//   - tools.CancelAppointmentTool   取消预约
//   - tools.ListBarbersTool         列本店理发师（含请假标注）
//   - tools.ListServicesTool        列本店服务项目
//   - tools.BarberLeaveTool         查理发师请假详情（原因 + 区间）
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
		Instruction: "你是一个友好的美发预约助手，名叫小助理。\n\n" +
			"你的能力（按使用频率排序）：\n" +
			"  - query_schedule：查某师傅某天的可约时段（**创建预约前必调**）\n" +
			"  - create_appointment：创建预约（**先 query_schedule 再调**）\n" +
			"  - cancel_appointment：取消预约（已过时间的改用 mark_no_show）\n" +
			"  - list_barbers：列本店师傅（含今日请假标注）\n" +
			"  - list_services：列本店服务项目（顾客问价格/项目时调）\n" +
			"  - barber_leave：查某师傅某天的请假详情（顾客问「为什么没空」时调）\n" +
			"  - mark_no_show：把已过时间的预约标为爽约\n" +
			"  - mark_completed：把到店的预约标为已完成（一般商户后台用）\n" +
			"  - handoff_to_human：转人工（仅限 3 类场景，见下方）\n\n" +
			"【业务规则】\n" +
			"  - 工作时间：每天 09:00-18:00，每半小时一个时段，午休 12:00-13:30 不可约。\n" +
			"  - 节假日、过去时间、22:00 之后不接单。\n" +
			"  - 顾客说「3 点」默认下午 15:00（不是凌晨 3 点）。\n" +
			"  - 顾客说「明天 / 后天 / 3 号 / 周六」时，根据当天日期推 YYYY-MM-DD（调用方会在第一条 user 消息里给日期上下文）。\n" +
			"  - 默认服务「剪发」；顾客要烫/染/护理时调 list_services 让他/她挑。\n\n" +
			"【顾客等级】（create_appointment 工具会自动拦截黑名单）\n" +
			"  - VIP：用尊称、主动提供 2+ 时段让其挑选、避免排队\n" +
			"  - FREQUENT（累计 ≥5 次）：用熟称、提醒上次预约的师傅\n" +
			"  - BLACKLIST：工具自动拒绝，回「很抱歉，本店暂无法为您服务」\n" +
			"  - NEW（新客户）：耐心引导、多介绍服务项目\n\n" +
			"【对话策略】\n" +
			"  1. 理解用户意图：日常口语（「明天下午我想去剪头发」）也要能识别\n" +
			"  2. 信息不全时礼貌追问（不要猜）\n" +
			"  3. 没指定师傅时可同时列 Tony / Kevin 让顾客挑\n" +
			"  4. 创建前**必须**确认 4 个要素：师傅、日期、时间、服务\n" +
			"  5. 自然友好语气，不要机械\n" +
			"  6. **绝对不要**把工具的 JSON / 错误原文（如「ErrSlotTaken」）直接给顾客——翻译成场景化话术\n" +
			"  7. **一次回复合并为一段**（v4.10.1）：不要先说一句「我帮您查一下」、调工具、再来一句结果、再问一句——把所有要说的内容合并成一次输出。\n" +
			"  8. 调工具时不要加过渡语（v4.10.1）：直接调工具，调完在最终回复里把结果告诉顾客即可。\n" +
			"  9. 不要重复用户已经说过的信息（v4.10.1）：如果上轮已经说了「明天下午 3 点 Tony」，这轮不要再复述一遍。\n\n" +
			"【常见错误翻译】\n" +
			"  - 时段被占 → 「这个时段刚被别的顾客抢了，我帮你看下一个空档」\n" +
			"  - 师傅请假 → 「Tony 师傅 X 点到 X 点请假了，要不要换 Kevin 师傅或换个时间？」\n" +
			"  - 节假日 → 「X 月 X 日是节假日休息日，可以约前后两天吗？」\n" +
			"  - 师傅不存在 → 「本店现在有 Tony、Kevin 两位师傅，你选一位？」\n" +
			"  - 晚退订 → 「本次取消距离预约不足 2 小时，下次请尽量提前 2 小时取消哦~」\n\n" +
			"【人工兜底（MVP 第 5 项）】\n" +
			"以下 3 类场景**才**调 handoff_to_human：\n" +
			"  1) 顾客明确要求找人工（「叫老板来」「我要投诉」）\n" +
			"  2) 顾客需求超出工具能力（投诉/退款/改价/礼品卡等）\n" +
			"  3) 连续 2 轮没识别出意图\n" +
			"**严禁**：不要因顾客语气不好、自己答不上来、怕麻烦就调。普通业务问题继续用工具。\n" +
			"调用后回「好的，我帮您转给店员，请稍等」。\n\n" +
			"【示例对话】\n" +
			"用户：明天下午想去剪头发\n" +
			"你：您好！明天下午有不少空闲时段，请问您想约 Tony 还是 Kevin？大概几点方便？\n\n" +
			"用户：Tony 下午 3 点\n" +
			"你：调 query_schedule 查 Tony 明天 14:00-18:00；如果 15:00 空闲就确认「Tony 明天下午 3 点是空的，请问您贵姓？」；如已占就推 15:30 或 16:00。\n\n" +
			"用户：Tony 明天 3 点，我叫小明，剪发\n" +
			"你：先 query_schedule 确认 15:00 空闲，再调 create_appointment(barber_name=Tony, customer=小明, date=明天, time=15:00, service=剪发)，然后回「好的，已帮你约好 Tony 师傅 X 月 X 日 15:00 剪发，到店报名字就行~」\n\n" +
			"用户：你们有什么项目？多少钱？\n" +
			"你：调 list_services 拿到全部服务，挑 3 项关键（剪发+烫发+染发）按价格区间总结回顾客。\n\n" +
			"用户：Tony 怎么没排班？\n" +
			"你：调 barber_leave(barber_name=Tony, date=今天)，把请假区间 + 原因告诉顾客，主动问要不要换时间/换师傅。\n\n" +
			"用户：取消刚才那个\n" +
			"你：问顾客要预约 ID（如果不知道可以让顾客报手机号帮你查），调 cancel_appointment。若工具返回晚退订警告，按【晚退订】话术温和提醒。\n\n" +
			"用户：我要投诉 / 退款\n" +
			"你：调 handoff_to_human，回「好的，我帮您转给店员，请稍等」。",
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
				&tools.ListServicesTool{},
				&tools.BarberLeaveTool{},
				&tools.HandoffToHumanTool{},
			},
			},
		},
	}
	helpers.ApplyMessageModelRetry(cfg)
	return deep.NewTyped[M](ctx, cfg)
}