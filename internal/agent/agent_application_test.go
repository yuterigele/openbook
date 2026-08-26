package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/intent"
	legacybooking "github.com/yuterigele/openbook/internal/booking/legacy"
	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/sdk/toolkit"
	"github.com/yuterigele/openbook/storage"
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

func TestAgentApplicationRuntimeExecutesLegacyCreate(t *testing.T) {
	storage.SetupTestDB(t)
	shop := storage.MakeShop(t, "agent-runtime-shop", "")
	storage.MakeBarber(t, "agent-runtime-barber", shop.ID, "Tony")
	customer := storage.MakeCustomer(t, "可信顾客", 0, 0)
	if err := storage.DB.Model(&storage.Customer{}).Where("id = ?", customer.ID).Updates(map[string]any{
		"phone":            "13800000001",
		"external_user_id": "external-agent-runtime",
	}).Error; err != nil {
		t.Fatalf("update customer identity: %v", err)
	}

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*60*60)
	}
	date := time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	application := legacybooking.NewApplication(func(ctx context.Context, trusted v1alpha1.ExecutionContext) (legacybooking.CustomerIdentity, error) {
		resolved, err := storage.GetCustomerByID(ctx, trusted.CustomerID)
		if err != nil {
			return legacybooking.CustomerIdentity{}, err
		}
		return legacybooking.CustomerIdentity{
			Name: resolved.Name, Phone: resolved.Phone,
			OpenID: resolved.WechatOpenID, ExternalUserID: resolved.ExternalUserID,
		}, nil
	})
	model := &scriptedLegacyCreateModel{date: date}
	agent, err := buildTypedWithModel(
		context.Background(), intent.NewClassifyTool(intent.NewClassifier()), model, chatmodel.ProviderStub, application,
	)
	if err != nil {
		t.Fatalf("agent construction failed: %v", err)
	}

	trusted := v1alpha1.ExecutionContext{
		MerchantID: shop.ID, LocationID: shop.ID, CustomerID: customer.ID, PrincipalID: "agent",
		Permissions: []v1alpha1.Permission{v1alpha1.PermissionBookingWrite},
		TraceID:     "agent-runtime-trace", IdempotencyKey: "agent-runtime-idempotency",
	}
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{Agent: agent})
	events := runner.Run(v1alpha1.WithExecutionContext(context.Background(), trusted), []adk.Message{schema.UserMessage("请帮我预约明天 14 点")})

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
	if final != "真实预约链路已完成" {
		t.Fatalf("final response = %q", final)
	}

	var appointment storage.Appointment
	if err := storage.DB.Where("shop_id = ? AND date = ? AND time = ?", shop.ID, date, "14:00").First(&appointment).Error; err != nil {
		t.Fatalf("load created appointment: %v", err)
	}
	if appointment.CustomerID != customer.ID || appointment.Customer != customer.Name {
		t.Fatalf("appointment identity = customer_id:%q customer:%q, want trusted customer %q/%q", appointment.CustomerID, appointment.Customer, customer.ID, customer.Name)
	}
}

type scriptedToolLoopModel struct {
	mu    sync.Mutex
	calls int
}

type scriptedLegacyCreateModel struct {
	mu         sync.Mutex
	calls      int
	date       string
	barberName string
}

func (m *scriptedLegacyCreateModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	m.mu.Lock()
	m.calls++
	callNumber := m.calls
	m.mu.Unlock()
	if callNumber == 1 {
		barberName := m.barberName
		if barberName == "" {
			barberName = "Tony"
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID: "legacy-create-call-1", Type: "function",
			Function: schema.FunctionCall{
				Name:      "create_appointment",
				Arguments: `{"barber_name":"` + barberName + `","customer":"模型伪造顾客","phone":"13800000002","date":"` + m.date + `","time":"14:00","service":"剪发"}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("真实预约链路已完成", nil), nil
}

func (m *scriptedLegacyCreateModel) Stream(ctx context.Context, messages []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
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
