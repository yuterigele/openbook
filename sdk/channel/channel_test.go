package channel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
)

func TestNewInboundValidatesStableEnvelope(t *testing.T) {
	receivedAt := time.Date(2026, 8, 26, 15, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	message, err := NewInbound("msg-1", "session-1", KindWeComCustomerService, "我想预约剪发", receivedAt)
	if err != nil {
		t.Fatalf("valid inbound message rejected: %v", err)
	}
	if message.SchemaVersion != SchemaVersion || message.MessageID != "msg-1" || message.Kind != KindWeComCustomerService {
		t.Fatalf("unexpected inbound message: %+v", message)
	}
}

func TestInboundRejectsUntrustedIdentityFieldsByShape(t *testing.T) {
	message, err := NewInbound("msg-1", "session-1", KindWebChat, "hello", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	// InboundMessage 没有身份字段；身份只能从独立的执行上下文取得。
	for _, forbidden := range []string{"merchant_id", "location_id", "customer_id", "principal_id", "permissions", "trace_id", "idempotency_key"} {
		if _, exists := fields[forbidden]; exists {
			t.Fatalf("inbound message must not carry trusted field %q: %s", forbidden, payload)
		}
	}
}

func TestInboundValidationRejectsUnsupportedOrOversizedMessages(t *testing.T) {
	valid := InboundMessage{
		SchemaVersion: SchemaVersion,
		MessageID:     "msg-1",
		SessionID:     "session-1",
		Kind:          KindWebChat,
		Text:          "hello",
		ReceivedAt:    time.Now(),
	}
	tests := []struct {
		name   string
		mutate func(*InboundMessage)
	}{
		{name: "schema", mutate: func(message *InboundMessage) { message.SchemaVersion = "channel.message.v0" }},
		{name: "kind", mutate: func(message *InboundMessage) { message.Kind = Kind("shell") }},
		{name: "message id", mutate: func(message *InboundMessage) { message.MessageID = " " }},
		{name: "text", mutate: func(message *InboundMessage) { message.Text = strings.Repeat("x", MaxMessageRunes+1) }},
		{name: "received at", mutate: func(message *InboundMessage) { message.ReceivedAt = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := valid
			test.mutate(&message)
			if err := message.Validate(); err == nil {
				t.Fatal("invalid message should be rejected")
			}
		})
	}
}

func TestHandlerFuncSeparatesTrustedContextAndValidatesReply(t *testing.T) {
	message, err := NewInbound("msg-1", "session-1", KindWebChat, "创建预约", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	trusted := v1alpha1.ExecutionContext{
		MerchantID: "merchant-1", LocationID: "location-1", TraceID: "trace-1",
		CustomerID: "customer-1",
	}
	var gotMessage InboundMessage
	var gotContext v1alpha1.ExecutionContext
	handler := HandlerFunc(func(_ context.Context, got InboundMessage, executionContext v1alpha1.ExecutionContext) (Reply, error) {
		gotMessage = got
		gotContext = executionContext
		// 通过显式构造器保持回复边界与 Handler 的校验一致。
		return NewReply("已收到")
	})
	reply, err := handler.Handle(context.Background(), message, trusted)
	if err != nil {
		t.Fatalf("handler rejected valid request: %v", err)
	}
	if reply.Text != "已收到" || gotMessage.MessageID != message.MessageID || gotContext.CustomerID != "customer-1" {
		t.Fatalf("trusted context or message was not forwarded: reply=%+v message=%+v context=%+v", reply, gotMessage, gotContext)
	}
}

func TestHandlerFuncRejectsMissingTrustedContextAndInvalidReply(t *testing.T) {
	message, err := NewInbound("msg-1", "session-1", KindWebChat, "hello", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	missingContext := HandlerFunc(func(context.Context, InboundMessage, v1alpha1.ExecutionContext) (Reply, error) {
		return Reply{Text: "should not run"}, nil
	})
	if _, err := missingContext.Handle(context.Background(), message, v1alpha1.ExecutionContext{}); err == nil {
		t.Fatal("missing trusted context should be rejected")
	}
	invalidReply := HandlerFunc(func(context.Context, InboundMessage, v1alpha1.ExecutionContext) (Reply, error) {
		return Reply{}, nil
	})
	trusted := v1alpha1.ExecutionContext{MerchantID: "merchant-1", LocationID: "location-1", TraceID: "trace-1"}
	if _, err := invalidReply.Handle(context.Background(), message, trusted); err == nil {
		t.Fatal("empty reply should be rejected")
	}
}

func TestReplyRejectsOversizedText(t *testing.T) {
	if _, err := NewReply(strings.Repeat("x", MaxReplyRunes+1)); err == nil {
		t.Fatal("oversized reply should be rejected")
	}
}
