// Package toolkit 定义适合模型消费的工具结果外层契约。
package toolkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
)

const SchemaVersion = "tool.result.v1"

// Status 是模型工具结果的有限状态集合。
type Status string

const (
	StatusOK          Status = "ok"
	StatusNeedsInput  Status = "needs_input"
	StatusConflict    Status = "conflict"
	StatusForbidden   Status = "forbidden"
	StatusUnavailable Status = "unavailable"
	StatusMaintenance Status = "maintenance"
	StatusUnknown     Status = "unknown"
)

// Result 是模型友好的工具结果，不应直接承载领域实体或存储错误。
type Result struct {
	SchemaVersion    string         `json:"schema_version"`
	Status           Status         `json:"status"`
	Code             string         `json:"code"`
	Summary          string         `json:"summary"`
	Facts            map[string]any `json:"facts"`
	SuggestedActions []string       `json:"suggested_actions"`
}

// Budget 限制单次工具结果的字段数和字符数。
type Budget struct {
	MaxFields int
	MaxChars  int
}

var DefaultBudget = Budget{MaxFields: 32, MaxChars: 6000}

// TimeFact 是工具结果中表达时间区间的统一结构。
type TimeFact struct {
	StartAt  string `json:"start_at"`
	EndAt    string `json:"end_at"`
	Timezone string `json:"timezone"`
	Display  string `json:"display"`
}

// NewOK 创建成功结果。
func NewOK(code, summary string, facts map[string]any, actions ...string) Result {
	return newResult(StatusOK, code, summary, facts, actions...)
}

// NewNeedsInput 创建需要顾客补充信息的结果。
func NewNeedsInput(code, summary string, facts map[string]any, actions ...string) Result {
	return newResult(StatusNeedsInput, code, summary, facts, actions...)
}

func newResult(status Status, code, summary string, facts map[string]any, actions ...string) Result {
	if facts == nil {
		facts = map[string]any{}
	}
	if actions == nil {
		actions = []string{}
	}
	return Result{
		SchemaVersion:    SchemaVersion,
		Status:           status,
		Code:             code,
		Summary:          summary,
		Facts:            facts,
		SuggestedActions: actions,
	}
}

// FromError 将应用错误映射为稳定的模型结果，不把原始错误文本返回给模型。
func FromError(err error) Result {
	if err == nil {
		return NewOK("booking.ok", "操作已完成", map[string]any{})
	}
	code := v1alpha1.CodeOf(err)
	switch code {
	case v1alpha1.ErrorCodeInvalidContext, v1alpha1.ErrorCodeInvalidOperation:
		return NewNeedsInput("booking.invalid_request", "还需要补充预约信息，请检查后再试", map[string]any{}, "补充预约信息")
	case v1alpha1.ErrorCodeForbidden:
		return newResult(StatusForbidden, "booking.forbidden", "当前身份无权执行这项预约操作", map[string]any{}, "转人工")
	case v1alpha1.ErrorCodeConflict:
		return newResult(StatusConflict, "booking.conflict", "这个时段刚刚被占用，请选择其他时段", map[string]any{}, "查询其他时段")
	case v1alpha1.ErrorCodeMaintenance:
		return newResult(StatusMaintenance, "booking.maintenance", "预约服务正在升级，暂时无法创建新预约，请稍后再试", map[string]any{}, "稍后查询", "转人工")
	case v1alpha1.ErrorCodeUnavailable:
		return newResult(StatusUnavailable, "booking.unavailable", "预约服务暂时不可用，请稍后再试", map[string]any{}, "稍后再试", "转人工")
	case v1alpha1.ErrorCodeUnknown:
		if errors.Is(err, context.Canceled) {
			return newResult(StatusUnknown, "booking.unknown", "系统正在确认本次操作，请稍后通过我的预约查看", map[string]any{}, "查看我的预约", "转人工")
		}
		return newResult(StatusUnknown, "booking.unknown", "系统正在确认本次操作，请稍后通过我的预约查看", map[string]any{}, "查看我的预约", "转人工")
	default:
		return newResult(StatusUnknown, "booking.unknown", "系统正在确认本次操作，请稍后通过我的预约查看", map[string]any{}, "查看我的预约", "转人工")
	}
}

// Validate 校验结果结构、字段预算、时间字段和不应暴露的敏感字段。
func (r Result) Validate(budget Budget) error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must be %q", SchemaVersion)
	}
	if !validStatus(r.Status) {
		return fmt.Errorf("unsupported status %q", r.Status)
	}
	if strings.TrimSpace(r.Code) == "" || strings.TrimSpace(r.Summary) == "" {
		return errors.New("code and summary are required")
	}
	if r.Facts == nil {
		return errors.New("facts must be an object")
	}
	if err := validateFacts(r.Facts); err != nil {
		return err
	}
	if budget.MaxFields > 0 && countFields(r.Facts) > budget.MaxFields {
		return fmt.Errorf("result exceeds max_fields %d", budget.MaxFields)
	}
	if budget.MaxChars > 0 {
		encoded, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("marshal result: %w", err)
		}
		if utf8.RuneCount(encoded) > budget.MaxChars {
			return fmt.Errorf("result exceeds max_chars %d", budget.MaxChars)
		}
	}
	return nil
}

func validStatus(status Status) bool {
	switch status {
	case StatusOK, StatusNeedsInput, StatusConflict, StatusForbidden, StatusUnavailable, StatusMaintenance, StatusUnknown:
		return true
	default:
		return false
	}
}

func validateFacts(facts map[string]any) error {
	for key, value := range facts {
		if err := validateFactValue(key, value); err != nil {
			return err
		}
	}
	return nil
}

func validateFactValue(path string, value any) error {
	if forbiddenFactKey(path) {
		return fmt.Errorf("sensitive fact field %q is not allowed", path)
	}
	if timeFact, ok := value.(TimeFact); ok {
		if err := timeFact.Validate(); err != nil {
			return fmt.Errorf("fact %q: %w", path, err)
		}
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		if looksLikeTimeFact(typed) {
			parsed := TimeFact{
				StartAt:  stringValue(typed["start_at"]),
				EndAt:    stringValue(typed["end_at"]),
				Timezone: stringValue(typed["timezone"]),
				Display:  stringValue(typed["display"]),
			}
			if err := parsed.Validate(); err != nil {
				return fmt.Errorf("fact %q: %w", path, err)
			}
		}
		for key, nested := range typed {
			if err := validateFactValue(path+"."+key, nested); err != nil {
				return err
			}
		}
	case []any:
		for index, nested := range typed {
			if err := validateFactValue(fmt.Sprintf("%s[%d]", path, index), nested); err != nil {
				return err
			}
		}
	}
	return nil
}

func forbiddenFactKey(key string) bool {
	key = strings.ToLower(key)
	if dot := strings.LastIndexByte(key, '.'); dot >= 0 {
		key = key[dot+1:]
	}
	if bracket := strings.IndexByte(key, '['); bracket >= 0 {
		key = key[:bracket]
	}
	switch key {
	case "phone", "mobile", "password", "secret", "api_key", "token", "wechat_open_id", "external_user_id":
		return true
	default:
		return false
	}
}

func looksLikeTimeFact(value map[string]any) bool {
	_, start := value["start_at"]
	_, end := value["end_at"]
	_, zone := value["timezone"]
	return start || end || zone
}

func stringValue(value any) string {
	valueString, _ := value.(string)
	return valueString
}

func (t TimeFact) Validate() error {
	if t.StartAt == "" || t.EndAt == "" || t.Timezone == "" || t.Display == "" {
		return errors.New("time facts require start_at, end_at, timezone and display")
	}
	start, err := timeParseRFC3339(t.StartAt)
	if err != nil {
		return fmt.Errorf("invalid start_at: %w", err)
	}
	end, err := timeParseRFC3339(t.EndAt)
	if err != nil {
		return fmt.Errorf("invalid end_at: %w", err)
	}
	if !end.After(start) {
		return errors.New("time fact end_at must be after start_at")
	}
	return nil
}

func timeParseRFC3339(value string) (time.Time, error) {
	return time.Parse(time.RFC3339, value)
}

func countFields(value any) int {
	switch typed := value.(type) {
	case map[string]any:
		count := 0
		for key, nested := range typed {
			if key == "" {
				continue
			}
			count++
			count += countFields(nested)
		}
		return count
	case []any:
		count := 0
		for _, nested := range typed {
			count += countFields(nested)
		}
		return count
	default:
		return 0
	}
}
