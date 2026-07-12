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

package intent

import "strings"

// keywordMap maps intent → trigger words (lowercase, substring match).
//
// Scoring rule (in keywordMatch):
//   - 1 trigger word hit  → confidence 0.7
//   - 2 trigger word hits → confidence 0.9
//   - 3+ trigger word hits → confidence 0.95
//
// Order matters when multiple intents match: the one with the highest
// score wins; ties broken by intent order in AllIntents.
var keywordMap = map[Intent][]string{
	IntentBook: {
		"预约", "预订", "订", "下单", "想约", "想去", "想剪", "想做",
		"想烫", "想染", "想护", "可以约", "能约", "有空吗", "约一下",
		"book", "appointment", "schedule",
	},
	IntentCancel: {
		"取消", "退订", "退掉", "不要了", "不来了", "销约", "撤销",
		"cancel",
	},
	IntentReschedule: {
		"改时间", "改到", "换时间", "改一下", "改天", "改日", "改期",
		"换到", "改到周", "挪到", "推迟", "提前", "调时间",
		"reschedule", "change time", "move to",
	},
	IntentQueryOpen: {
		"什么时候有空", "什么时候可以", "几号有空", "哪天有空", "几点有空",
		"排班", "档期", "可约", "空档", "有空", "排班表",
		"schedule", "available",
	},
	IntentListBarbers: {
		"有哪些师傅", "有什么师傅", "师傅名单", "理发师", "Tony 师傅", "Kevin 师傅",
		"师傅", "barber", "stylist",
	},
	IntentListService: {
		"价格", "多少钱", "价位", "项目", "服务", "剪发多少钱",
		"烫发多少钱", "染发多少钱", "价目表", "service", "price",
	},
	IntentListHoliday: {
		"营业时间", "几点开门", "几点关门", "周末开吗", "节假日", "休息日",
		"开门", "营业", "几点开", "open hours", "holiday",
	},
	IntentGreeting: {
		"你好", "在吗", "hi", "hello", "嗨", "哈喽", "早上好", "下午好",
		"晚上好",
	},
	IntentComplaint: {
		"投诉", "差评", "退款", "退钱", "不满", "生气", "气死了",
		"烂", "差", "垃圾", "骗人", "骗子", "欺骗", "上当",
		"refund", "complain",
	},
	IntentHandoff: {
		"找人工", "叫老板", "转人工", "要人", "找真人", "找店主",
		"叫店长", "老板呢", "人在吗",
		"handoff", "human", "real person",
	},
	IntentChitchat: {
		"天气", "新闻", "笑话", "讲个", "聊聊", "干嘛呢", "吃饭了吗",
	},
}

// keywordMatch returns the best-matching intent, the trigger word, and
// the confidence (0-1). Returns ("", "", 0) when no match.
//
// Tie-break: position of the *earliest* match in the text wins. This
// matches how humans read — "cancel my appointment" the action word
// comes first, so cancel beats book even though both keywords match.
//
// Example: "取消刚才那个预约" → "取消" at pos 0 wins over "预约" at pos 6.
func keywordMatch(text string) (Intent, string, float64) {
	lower := strings.ToLower(text)
	type match struct {
		intent Intent
		word   string
		pos    int
	}
	var earliest *match
	intentHits := map[Intent]int{}

	for _, intent := range AllIntents {
		words := keywordMap[intent]
		for _, w := range words {
			if w == "" {
				continue
			}
			pos := strings.Index(lower, strings.ToLower(w))
			if pos < 0 {
				continue
			}
			intentHits[intent]++
			if earliest == nil || pos < earliest.pos {
				earliest = &match{intent: intent, word: w, pos: pos}
			}
		}
	}

	if earliest == nil {
		return "", "", 0
	}

	// Confidence curve:
	//   - 1 hit on the winning intent  → 0.7
	//   - 2 hits on the winning intent → 0.9
	//   - 3+ hits                        → 0.95
	//   - other intents also have hits  → +0.05 (ambiguity awareness, but
	//                                      still trust the earliest)
	hits := intentHits[earliest.intent]
	var conf float64
	switch hits {
	case 1:
		conf = 0.7
	case 2:
		conf = 0.9
	default:
		conf = 0.95
	}
	return earliest.intent, earliest.word, conf
}
