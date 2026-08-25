// Package channel 定义渠道适配器与 Agent Host 之间的最小消息契约。
//
// 渠道消息只承载已验签消息的传输元数据和正文。商户、门店、顾客、权限
// 等可信身份必须作为独立的 v1alpha1.ExecutionContext 传入 Handler，不能
// 从消息正文或渠道请求体中恢复。
package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
)

const (
	// SchemaVersion 是入站渠道消息的稳定外层版本。
	SchemaVersion = "channel.message.v1"
	// MaxMessageRunes 限制一次入站文本的大小，避免渠道适配器把异常大正文
	// 直接送入 Agent。
	MaxMessageRunes = 8000
	// MaxReplyRunes 限制 Host 返回给渠道适配器的单条文本大小。
	MaxReplyRunes = 6000
)

// Kind 是已支持的渠道类型白名单。
type Kind string

const (
	KindWebChat              Kind = "web_chat"
	KindWeComCustomerService Kind = "wecom_customer_service"
	KindWeComExternalContact Kind = "wecom_external_contact"
)

// IsValid 判断渠道类型是否属于显式支持的白名单。
func (k Kind) IsValid() bool {
	switch k {
	case KindWebChat, KindWeComCustomerService, KindWeComExternalContact:
		return true
	default:
		return false
	}
}

// InboundMessage 是渠道适配器验签、去重并解析后的入站文本。
//
// MessageID 用于渠道重放保护，SessionID 用于会话串联。结构体不包含
// customer_id、merchant_id、location_id 或权限字段，避免把不可信渠道
// 字段误当作身份来源。
type InboundMessage struct {
	SchemaVersion string    `json:"schema_version"`
	MessageID     string    `json:"message_id"`
	SessionID     string    `json:"session_id"`
	Kind          Kind      `json:"kind"`
	Text          string    `json:"text"`
	ReceivedAt    time.Time `json:"received_at"`
}

// NewInbound 创建并校验一条入站消息。
func NewInbound(messageID, sessionID string, kind Kind, text string, receivedAt time.Time) (InboundMessage, error) {
	message := InboundMessage{
		SchemaVersion: SchemaVersion,
		MessageID:     strings.TrimSpace(messageID),
		SessionID:     strings.TrimSpace(sessionID),
		Kind:          kind,
		Text:          text,
		ReceivedAt:    receivedAt,
	}
	if err := message.Validate(); err != nil {
		return InboundMessage{}, err
	}
	return message, nil
}

// Validate 校验消息的稳定外层和大小边界。
func (m InboundMessage) Validate() error {
	switch {
	case m.SchemaVersion != SchemaVersion:
		return fmt.Errorf("schema_version must be %q", SchemaVersion)
	case strings.TrimSpace(m.MessageID) == "":
		return errors.New("message_id is required")
	case utf8.RuneCountInString(m.MessageID) > 256:
		return errors.New("message_id is too long")
	case strings.TrimSpace(m.SessionID) == "":
		return errors.New("session_id is required")
	case utf8.RuneCountInString(m.SessionID) > 256:
		return errors.New("session_id is too long")
	case !m.Kind.IsValid():
		return fmt.Errorf("unsupported channel kind %q", m.Kind)
	case strings.TrimSpace(m.Text) == "":
		return errors.New("text is required")
	case utf8.RuneCountInString(m.Text) > MaxMessageRunes:
		return fmt.Errorf("text exceeds %d characters", MaxMessageRunes)
	case m.ReceivedAt.IsZero():
		return errors.New("received_at is required")
	default:
		return nil
	}
}

// Reply 是 Host 返回给渠道适配器的最小文本回复。
type Reply struct {
	Text string `json:"text"`
}

// NewReply 创建并校验一条渠道回复。
func NewReply(text string) (Reply, error) {
	reply := Reply{Text: text}
	if err := reply.Validate(); err != nil {
		return Reply{}, err
	}
	return reply, nil
}

// Validate 校验回复文本大小。
func (r Reply) Validate() error {
	switch {
	case strings.TrimSpace(r.Text) == "":
		return errors.New("reply text is required")
	case utf8.RuneCountInString(r.Text) > MaxReplyRunes:
		return fmt.Errorf("reply text exceeds %d characters", MaxReplyRunes)
	default:
		return nil
	}
}

// Handler 是渠道适配器调用 Agent Host 的最小接口。
//
// executionContext 必须由 Host 根据已验签、已认证的渠道会话构造；Handler
// 实现不得从 InboundMessage.Text 或渠道元数据中补写可信身份。
type Handler interface {
	Handle(context.Context, InboundMessage, v1alpha1.ExecutionContext) (Reply, error)
}

// HandlerFunc 将函数适配为 Handler。
type HandlerFunc func(context.Context, InboundMessage, v1alpha1.ExecutionContext) (Reply, error)

// Handle 实现 Handler。
func (handler HandlerFunc) Handle(ctx context.Context, message InboundMessage, executionContext v1alpha1.ExecutionContext) (Reply, error) {
	if handler == nil {
		return Reply{}, errors.New("channel handler is nil")
	}
	if err := message.Validate(); err != nil {
		return Reply{}, err
	}
	if err := executionContext.Validate(); err != nil {
		return Reply{}, err
	}
	reply, err := handler(ctx, message, executionContext)
	if err != nil {
		return Reply{}, err
	}
	if err := reply.Validate(); err != nil {
		return Reply{}, err
	}
	return reply, nil
}

var _ Handler = HandlerFunc(nil)
