package main

// agent_prompt_test.go
//
// 覆盖 v4.13.4 Agent prompt 关键约束——防有人误删/改坏关键对话规则。
//
// 关键约束（缺一不可）：
//  1. 创建预约成功后必须告诉顾客预约号
//  2. 改/取消时优先从 history 找 ID（不要无脑问顾客）
//  3. 回复 ≤ 80 字（v4.13.3）
//  4. 不暴露工具错误原文（如 ErrSlotTaken）
//  5. 涉及金额主动告知
//  6. 改时间的正确流程：取消旧的 + 创建新的
//
// 这些都是 LLM 行为约束——测试只能验证 prompt 字符串包含关键规则，
// 实际 LLM 行为得用集成测试（e2e 跑 chatmodel）才能验证。
//
// Run:
//   go test . -v -run "TestBuildAgentInstruction"

import (
	"strings"
	"testing"
)

func TestBuildAgentInstruction_KeyConstraints(t *testing.T) {
	prompt := buildAgentInstruction()

	// 关键约束列表
	checks := []struct {
		desc     string
		mustHave []string // prompt 必须包含的子串
	}{
		{
			desc: "【v4.13.4 核心】创建预约成功必须告诉预约号",
			mustHave: []string{
				"创建预约成功后，必须在回复里把'预约号'告诉顾客",
				"A1B2C3D",        // 示例 ID
				"建议截图保存",   // 提示顾客保存
			},
		},
		{
			desc: "【v4.13.4 核心】改/取消时优先从 history 找 ID",
			mustHave: []string{
				"优先从本会话 history 里找最近一次 create_appointment 的返回值",
				"绝对不要",                       // 强约束语气
				"请提供预约号",                    // 错的反例
				"这是死循环，顾客根本不知道",         // 解释为什么不能这样
			},
		},
		{
			desc: "【v4.13.3】回复 ≤ 80 字",
			mustHave: []string{
				"回复必须 ≤ 80 字",
			},
		},
		{
			desc: "【隐私】不暴露工具错误原文",
			mustHave: []string{
				"ErrSlotTaken",
				"翻译成场景化话术",
			},
		},
		{
			desc: "【v4.13.4 新增】涉及金额主动告知",
			mustHave: []string{
				"涉及金额时**主动告知**",
			},
		},
		{
			desc: "【改时间】正确流程：取消旧的 + 创建新的",
			mustHave: []string{
				"我想改到 4 点",     // 示例触发场景
				"cancel_appointment 取消", // 取消旧的
				"调 create_appointment 约 16:00", // 创建新的
				"新预约号", // 告诉新 ID
			},
		},
		{
			desc: "【核心对话】问手机号（不是 ID）作为顾客身份识别",
			mustHave: []string{
				"13812345678",         // 示例手机号
				"方便留个手机号吗",      // 创建前要手机号
			},
		},
	}

	for _, check := range checks {
		t.Run(check.desc, func(t *testing.T) {
			for _, sub := range check.mustHave {
				if !strings.Contains(prompt, sub) {
					t.Errorf("prompt 缺关键约束：%q\n\n完整 prompt：\n%s", sub, prompt)
				}
			}
		})
	}
}

func TestBuildAgentInstruction_NoBannedPhrases(t *testing.T) {
	// 反向断言：prompt 不应包含错误/过时的措辞
	prompt := buildAgentInstruction()

	bannedPhrases := []struct {
		phrase     string
		why        string
	}{
		{
			phrase: "到店报名字就行",
			why:    "v4.13.4 改：必须告诉预约号，不能只说报名字",
		},
		{
			phrase: "问顾客要预约 ID",
			why:    "v4.13.4 改：优先从 history 找 ID，不要无脑问",
		},
	}

	for _, bp := range bannedPhrases {
		t.Run(bp.why, func(t *testing.T) {
			if strings.Contains(prompt, bp.phrase) {
				t.Errorf("prompt 包含已废弃措辞：%q（%s）\n\n请检查并删除", bp.phrase, bp.why)
			}
		})
	}
}
