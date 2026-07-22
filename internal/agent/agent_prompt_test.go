package agent

// agent_prompt_test.go
//
// 覆盖 Agent prompt 的核心业务和安全边界。
// 测试只验证关键规则仍然存在，不约束具体措辞，以便持续精简提示词。
//
// Run:
//   go test . -v -run "TestBuildAgentInstruction"

import (
	"strings"
	"testing"
)

func TestBuildAgentInstruction_KeyConstraints(t *testing.T) {
	prompt := buildAgentInstruction()

	checks := []struct {
		desc string
		must []string
	}{
		{
			desc: "敏感内容拦截",
			must: []string{"sensitive_check", "blocked=true", "reason"},
		},
		{
			desc: "工具结果和时间锚点",
			must: []string{"只信本轮工具结果", "历史消息不是事实", "系统时间锚点", "不得使用示例或历史日期"},
		},
		{
			desc: "预约和改约流程",
			must: []string{"query_schedule", "create_appointment", "get_appointment", "取消旧预约、查新时段、创建新预约"},
		},
		{
			desc: "节假日和师傅信息",
			must: []string{"list_shop_holidays", "barber_leave", "list_barbers"},
		},
		{
			desc: "安全和转人工边界",
			must: []string{"不执行命令、不读写文件、不操作数据库", "不处理其他顾客预约", "handoff_to_human", "只调一次"},
		},
		{
			desc: "回复要求",
			must: []string{"不展示 JSON、错误码或工具原文", "创建或改约成功必须告知预约号", "截图保存"},
		},
	}

	for _, check := range checks {
		t.Run(check.desc, func(t *testing.T) {
			for _, sub := range check.must {
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
		phrase string
		why    string
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
