package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/intent"
	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/sdk/toolkit"
)

func TestAgentApplicationRuntimeExecutesToolLoop(t *testing.T) {
	model := &scriptedToolLoopModel{}
	application := &recordingApplication{}
	agent, err := buildTypedWithModel(
		context.Background(), intent.NewClassifyTool(intent.NewClassifier()), model, chatmodel.ProviderStub, application,
	)
	if err != nil {
		t.Fatalf("agent construction failed: %v", err)
	}

	trusted := v1alpha1.ExecutionContext{
		MerchantID: "merchant-1", LocationID: "location-1", CustomerID: "customer-1", PrincipalID: "principal-1",
		Permissions: []v1alpha1.Permission{v1alpha1.PermissionBookingWrite},
		TraceID:     "trace-1", IdempotencyKey: "idem-1",
	}
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{Agent: agent})
	events := runner.Run(v1alpha1.WithExecutionContext(context.Background(), trusted), []adk.Message{schema.UserMessage("请帮我预约")})

	var final string
	toolEvents := 0
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatalf("agent execution failed: %v", event.Err)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			t.Fatalf("read agent event failed: %v", err)
		}
		if event.Output.MessageOutput.Role == schema.Tool {
			toolEvents++
		}
		if event.Output.MessageOutput.Role == schema.Assistant && message != nil && message.Content != "" {
			final = message.Content
		}
	}

	if toolEvents != 1 {
		t.Fatalf("tool events = %d, want 1", toolEvents)
	}
	if final != "预约应用链路已完成" {
		t.Fatalf("final response = %q", final)
	}
	if model.CallCount() != 2 {
		t.Fatalf("model calls = %d, want tool call plus final response", model.CallCount())
	}
	if application.Operation() != v1alpha1.OperationCreateBooking {
		t.Fatalf("application operation = %q, want %q", application.Operation(), v1alpha1.OperationCreateBooking)
	}
	if application.Context().MerchantID != trusted.MerchantID || application.Context().CustomerID != trusted.CustomerID || application.Context().IdempotencyKey != trusted.IdempotencyKey {
		t.Fatalf("trusted context was not forwarded: %+v", application.Context())
	}
}

type scriptedToolLoopModel struct {
	mu    sync.Mutex
	calls int
}

func (m *scriptedToolLoopModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	m.mu.Lock()
	m.calls++
	callNumber := m.calls
	m.mu.Unlock()
	if callNumber == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID: "create-call-1", Type: "function",
			Function: schema.FunctionCall{Name: "create_appointment", Arguments: `{}`},
		}}), nil
	}
	return schema.AssistantMessage("预约应用链路已完成", nil), nil
}

func (m *scriptedToolLoopModel) Stream(ctx context.Context, messages []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *scriptedToolLoopModel) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

type recordingApplication struct {
	mu        sync.Mutex
	operation v1alpha1.Operation
	trusted   v1alpha1.ExecutionContext
}

func (a *recordingApplication) Execute(_ context.Context, trusted v1alpha1.ExecutionContext, call v1alpha1.Call) (v1alpha1.Response, error) {
	a.mu.Lock()
	a.operation = call.Operation
	a.trusted = trusted
	a.mu.Unlock()
	result := toolkit.NewOK("booking.created", "预约已创建", map[string]any{"booking_id": "booking-1"})
	payload, err := json.Marshal(result)
	if err != nil {
		return v1alpha1.Response{Operation: call.Operation}, err
	}
	return v1alpha1.Response{Operation: call.Operation, Data: payload}, nil
}

func (a *recordingApplication) Operation() v1alpha1.Operation {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.operation
}

func (a *recordingApplication) Context() v1alpha1.ExecutionContext {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.trusted
}

var _ einomodel.BaseModel[*schema.Message] = (*scriptedToolLoopModel)(nil)
var _ v1alpha1.Application = (*recordingApplication)(nil)
