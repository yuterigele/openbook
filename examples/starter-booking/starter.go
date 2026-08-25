package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/sdk/profile"
	"github.com/yuterigele/openbook/sdk/toolkit"
)

// StarterProfile 返回一个可复制修改的最小行业 Profile。
func StarterProfile() profile.Definition {
	return profile.Definition{
		ID:          "pet_grooming",
		DisplayName: "宠物护理预约",
		Terms: profile.Terms{
			Staff:    "护理师",
			Service:  "护理项目",
			Resource: "护理台",
		},
		StartInterval: 30 * time.Minute,
		ResourceTypes: []profile.ResourceType{{
			ID: "grooming_table", DisplayName: "护理台", Exclusive: true,
		}},
		Services: []profile.Service{
			{
				ID: "wash_and_trim", DisplayName: "洗护修剪", Duration: 60 * time.Minute,
				BufferBefore: 15 * time.Minute, BufferAfter: 15 * time.Minute,
				RequiredResources: []profile.ResourceRequirement{{ResourceTypeID: "grooming_table", Quantity: 1}},
			},
		},
		RequiredCustomerFields: []string{"name"},
		TemplateVariables:      []string{"service", "staff"},
		ReplyTemplates: map[string]string{
			"booking_created": "已为你预约 {{service}}，护理师是 {{staff}}。",
		},
	}
}

// NewStarterRegistry 将 Starter Application 接入显式工具白名单。
func NewStarterRegistry(application v1alpha1.Application, definition profile.Definition) (*toolkit.Registry, error) {
	if application == nil {
		return nil, errors.New("starter application is required")
	}
	if err := definition.Validate(); err != nil {
		return nil, fmt.Errorf("starter profile: %w", err)
	}
	registry := toolkit.NewRegistry()
	if err := registry.Register(toolkit.ToolSpec{
		Name:        "list_services",
		Description: "列出当前行业可预约的护理项目",
		Operation:   v1alpha1.OperationListServices,
		Mode:        toolkit.ModeRead,
		Permission:  v1alpha1.PermissionBookingRead,
		Retry:       toolkit.ReadRetryPolicy(),
		Handler:     applicationHandler(application, v1alpha1.OperationListServices),
	}); err != nil {
		return nil, err
	}
	if err := registry.Register(toolkit.ToolSpec{
		Name:        "create_booking",
		Description: "为当前已验证顾客创建护理预约",
		Operation:   v1alpha1.OperationCreateBooking,
		Mode:        toolkit.ModeWrite,
		Permission:  v1alpha1.PermissionBookingWrite,
		Retry:       toolkit.WriteRetryPolicy(),
		Handler:     applicationHandler(application, v1alpha1.OperationCreateBooking),
	}); err != nil {
		return nil, err
	}
	return registry, nil
}

func applicationHandler(application v1alpha1.Application, operation v1alpha1.Operation) toolkit.Handler {
	return func(ctx context.Context, trusted v1alpha1.ExecutionContext, parameters json.RawMessage) (toolkit.Result, error) {
		if err := validateStarterParameters(parameters); err != nil {
			return toolkit.FromError(err), err
		}
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{}`)
		}
		response, err := application.Execute(ctx, trusted, v1alpha1.Call{Operation: operation, Parameters: parameters})
		if err != nil {
			return toolkit.FromError(err), err
		}
		if response.Operation != operation {
			err := v1alpha1.NewError(v1alpha1.ErrorCodeUnavailable, "application returned an unexpected operation")
			return toolkit.FromError(err), err
		}
		var result toolkit.Result
		if err := json.Unmarshal(response.Data, &result); err != nil {
			wrapped := v1alpha1.WrapError(v1alpha1.ErrorCodeUnavailable, "application returned an invalid tool result", err)
			return toolkit.FromError(wrapped), wrapped
		}
		if err := result.Validate(toolkit.DefaultBudget); err != nil {
			wrapped := v1alpha1.WrapError(v1alpha1.ErrorCodeUnavailable, "application returned an invalid tool result", err)
			return toolkit.FromError(wrapped), wrapped
		}
		return result, nil
	}
}

func validateStarterParameters(parameters json.RawMessage) error {
	if len(parameters) == 0 || string(parameters) == "null" {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(parameters, &object); err != nil || object == nil {
		return v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "business parameters must be an object")
	}
	for _, forbidden := range []string{"merchant_id", "location_id", "customer_id", "principal_id", "permissions", "trace_id", "idempotency_key"} {
		if _, exists := object[forbidden]; exists {
			return v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, fmt.Sprintf("trusted field %q is not accepted in business parameters", forbidden))
		}
	}
	return nil
}

// ChatRequest 是 Starter Web Chat 的最小请求格式。
// 身份、权限和幂等键由 Handler 的 ContextResolver 注入，不从请求体读取。
type ChatRequest struct {
	Tool       string          `json:"tool"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

// ChatResponse 是 Web Chat 返回的稳定工具结果。
type ChatResponse struct {
	Result toolkit.Result `json:"result"`
}

// ContextResolver 从 Host 的认证结果构造可信执行上下文。
type ContextResolver func(*http.Request) (v1alpha1.ExecutionContext, error)

// NewChatHandler 创建最小 Web Chat HTTP 入口。
func NewChatHandler(registry *toolkit.Registry, resolve ContextResolver) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeChatError(writer, http.StatusMethodNotAllowed, "only POST is supported")
			return
		}
		var input ChatRequest
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Tool) == "" {
			writeChatError(writer, http.StatusBadRequest, "tool is required")
			return
		}
		if resolve == nil {
			writeChatError(writer, http.StatusInternalServerError, "context resolver is not configured")
			return
		}
		trusted, err := resolve(request)
		if err != nil {
			result := toolkit.FromError(err)
			writeChatResult(writer, result)
			return
		}
		result, _ := registry.Invoke(request.Context(), trusted, input.Tool, input.Parameters)
		writeChatResult(writer, result)
	})
}

func writeChatError(writer http.ResponseWriter, status int, message string) {
	http.Error(writer, message, status)
}

func writeChatResult(writer http.ResponseWriter, result toolkit.Result) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(ChatResponse{Result: result})
}

// MemoryApplication 是 Starter 可直接运行的内存 Application。
type MemoryApplication struct {
	mu        sync.Mutex
	profile   profile.Definition
	bookings  map[string]MemoryBooking
	byRequest map[string]string
}

// MemoryBooking 是内存示例保存的最小预约记录。
type MemoryBooking struct {
	ID         string
	CustomerID string
	ServiceID  string
	StaffID    string
	StartAt    time.Time
	EndAt      time.Time
}

// NewMemoryApplication 创建内存 Application，适合 Demo 和单元测试。
func NewMemoryApplication(definition profile.Definition) (*MemoryApplication, error) {
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	return &MemoryApplication{
		profile:   definition,
		bookings:  make(map[string]MemoryBooking),
		byRequest: make(map[string]string),
	}, nil
}

// LookupBooking 按可信顾客和幂等键读取内存预约。
func (a *MemoryApplication) LookupBooking(customerID, idempotencyKey string) (MemoryBooking, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id, ok := a.byRequest[customerID+"\x00"+idempotencyKey]
	if !ok {
		return MemoryBooking{}, false
	}
	booking, ok := a.bookings[id]
	return booking, ok
}

// Execute 实现 v1alpha1.Application，仅用于 Starter 的可运行示例。
func (a *MemoryApplication) Execute(_ context.Context, trusted v1alpha1.ExecutionContext, call v1alpha1.Call) (v1alpha1.Response, error) {
	response := v1alpha1.Response{Operation: call.Operation}
	if err := trusted.ValidateFor(call.Operation); err != nil {
		return starterResponse(response, toolkit.FromError(err), err)
	}
	switch call.Operation {
	case v1alpha1.OperationListServices:
		services := make([]map[string]any, 0, len(a.profile.Services))
		for _, service := range a.profile.Services {
			services = append(services, map[string]any{
				"id": service.ID, "name": service.DisplayName, "duration_minutes": int(service.Duration / time.Minute),
			})
		}
		return starterResponse(response, toolkit.NewOK("services.listed", "可预约的护理项目", map[string]any{
			"profile": a.profile.ID, "services": services,
		}), nil)
	case v1alpha1.OperationCreateBooking:
		return a.createBooking(trusted, call, response)
	default:
		err := v1alpha1.NewError(v1alpha1.ErrorCodeInvalidOperation, "starter application does not support this operation")
		return starterResponse(response, toolkit.FromError(err), err)
	}
}

func (a *MemoryApplication) createBooking(trusted v1alpha1.ExecutionContext, call v1alpha1.Call, response v1alpha1.Response) (v1alpha1.Response, error) {
	var input struct {
		ServiceID string `json:"service_id"`
		StaffID   string `json:"staff_id"`
		StartAt   string `json:"start_at"`
	}
	if err := json.Unmarshal(call.Parameters, &input); err != nil || input.ServiceID == "" || input.StartAt == "" {
		err := v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "service_id and start_at are required")
		return starterResponse(response, toolkit.FromError(err), err)
	}
	var selected profile.Service
	for _, service := range a.profile.Services {
		if service.ID == input.ServiceID {
			selected = service
			break
		}
	}
	if selected.ID == "" {
		err := v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "service is not available")
		return starterResponse(response, toolkit.FromError(err), err)
	}
	startAt, err := time.Parse(time.RFC3339, input.StartAt)
	if err != nil {
		validationErr := v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "start_at must use RFC3339")
		return starterResponse(response, toolkit.FromError(validationErr), validationErr)
	}
	staffID := input.StaffID
	if staffID == "" {
		staffID = "staff-demo"
	}
	requestKey := trusted.CustomerID + "\x00" + trusted.IdempotencyKey
	a.mu.Lock()
	defer a.mu.Unlock()
	if existingID, ok := a.byRequest[requestKey]; ok {
		booking := a.bookings[existingID]
		return starterResponse(response, bookingResult(booking), nil)
	}
	booking := MemoryBooking{
		ID: uuid.NewString(), CustomerID: trusted.CustomerID, ServiceID: selected.ID, StaffID: staffID,
		StartAt: startAt, EndAt: startAt.Add(selected.Duration),
	}
	a.bookings[booking.ID] = booking
	a.byRequest[requestKey] = booking.ID
	return starterResponse(response, bookingResult(booking), nil)
}

func bookingResult(booking MemoryBooking) toolkit.Result {
	return toolkit.NewOK("booking.created", "预约已创建", map[string]any{
		"booking_id": booking.ID,
		"service_id": booking.ServiceID,
		"staff_id":   booking.StaffID,
		"slot": toolkit.TimeFact{
			StartAt: booking.StartAt.Format(time.RFC3339), EndAt: booking.EndAt.Format(time.RFC3339),
			Timezone: "Asia/Shanghai", Display: booking.StartAt.Format("2006-01-02 15:04"),
		},
	})
}

func starterResponse(response v1alpha1.Response, result toolkit.Result, executionErr error) (v1alpha1.Response, error) {
	payload, err := json.Marshal(result)
	if err != nil {
		return response, err
	}
	response.Data = payload
	return response, executionErr
}

var _ v1alpha1.Application = (*MemoryApplication)(nil)
