package server

import (
	"context"
	"strings"
	"time"
	"unicode"
)

// userInputTrustThreshold is deliberately conservative: normal greetings and
// short, incomplete booking requests pass; only clearly unrelated, spammy, or
// instruction-like messages are stopped before they reach the Agent.
const defaultUserInputTrustThreshold = -2

const untrustedInputReply = "我只能协助本店的预约、查询、改约和取消。如需预约，请告诉我日期、时间或想找的师傅。"

type userInputTrustDecision struct {
	Allowed bool
	Score   int
	Reason  string
}

// assessUserInputTrust applies a deterministic relevance/risk threshold before
// an Agent run. It is not an identity or permission decision; those remain in
// the business tools. Its purpose is to keep obvious spam and prompt-injection
// traffic away from the model and every appointment tool.
func assessUserInputTrust(input string) userInputTrustDecision {
	text := strings.ToLower(strings.TrimSpace(input))
	if text == "" {
		return userInputTrustDecision{Allowed: false, Score: -3, Reason: "empty"}
	}
	// Cheap deterministic checks stay first so obvious attacks never spend a
	// model call. The optional small model only handles ambiguous text.
	if (containsAny(text, riskyInputTerms) || looksLikeGarbage(text)) &&
		!containsAny(text, bookingTerms) && !containsAny(text, greetingTerms) {
		return userInputTrustDecision{Allowed: false, Score: -3, Reason: "deterministic_risk"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if decision, ok := classifyInputTrustWithLLM(ctx, input); ok {
		return decision
	}

	score := 0
	if containsAny(text, bookingTerms) {
		score += 2
	}
	if containsAny(text, greetingTerms) {
		score++
	}
	if strings.ContainsAny(text, "？?") {
		score++
	}
	// Long messages without a booking signal are unlikely to be useful in a
	// narrowly scoped appointment channel. Short messages stay allowed so the
	// Agent can naturally handle “你好” and ask a follow-up question.
	if len([]rune(text)) >= 12 && !containsAny(text, bookingTerms) && !containsAny(text, greetingTerms) {
		score -= 2
	}

	threshold := getEnvInt("USER_INPUT_TRUST_THRESHOLD", defaultUserInputTrustThreshold)
	decision := userInputTrustDecision{Allowed: score > threshold, Score: score}
	if !decision.Allowed {
		decision.Reason = "low_relevance_or_risk"
	}
	return decision
}

var bookingTerms = []string{
	"预约", "约个", "约一", "剪发", "烫发", "染发", "洗头", "护理", "理发",
	"师傅", "发型师", "档期", "空位", "时间", "今天", "明天", "后天",
	"改约", "改时间", "取消", "预约号", "价格", "收费", "多少钱",
	"营业", "几点", "地址", "门店", "店长", "投诉", "退款", "人工",
}

var greetingTerms = []string{
	"你好", "您好", "在吗", "嗨", "hello", "hi", "hey", "谢谢", "好的", "ok",
}

var riskyInputTerms = []string{
	"ignore previous", "ignore all", "system prompt", "developer message",
	"忽略之前", "忽略上面", "忽略指令", "系统提示词", "提示词", "越狱",
	"rm -rf", "powershell", "cmd.exe", "curl ", "wget ", "select *", "drop table",
	"http://", "https://", "www.", "加微信", "扫码加", "代理赚钱", "博彩",
}

func containsAny(text string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func looksLikeGarbage(text string) bool {
	runes := []rune(text)
	if len(runes) < 8 {
		return false
	}
	counts := make(map[rune]int)
	meaningful := 0
	for _, r := range runes {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			meaningful++
		}
		counts[r]++
	}
	maxCount := 0
	for _, n := range counts {
		if n > maxCount {
			maxCount = n
		}
	}
	return meaningful == 0 || maxCount*100 >= len(runes)*70
}
