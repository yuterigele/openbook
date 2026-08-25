package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuterigele/openbook/internal/booking/domain"
	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/sdk/toolkit"
	"github.com/yuterigele/openbook/storage"
)

func TestStarterRegistryContainsOneReadAndOneWriteTool(t *testing.T) {
	definition := StarterProfile()
	application, err := NewMemoryApplication(definition)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewStarterRegistry(application, definition)
	if err != nil {
		t.Fatal(err)
	}
	specs := registry.List()
	if len(specs) != 2 || specs[0].Name != "create_booking" || specs[1].Name != "list_services" {
		t.Fatalf("unexpected starter tools: %+v", specs)
	}
	if specs[0].Mode != toolkit.ModeWrite || specs[0].Retry.MaxAttempts != 1 {
		t.Fatalf("write tool must be single-attempt: %+v", specs[0])
	}
	if specs[1].Mode != toolkit.ModeRead || specs[1].Retry.MaxAttempts != 2 {
		t.Fatalf("read tool should use bounded retry: %+v", specs[1])
	}
}

func TestStarterWebChatEndToEndWithMemoryApplication(t *testing.T) {
	definition := StarterProfile()
	application, err := NewMemoryApplication(definition)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewStarterRegistry(application, definition)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewChatHandler(registry, starterTestContext)

	listed := postChat(t, handler, "list_services", map[string]any{}, "read-1")
	if listed.Result.Status != toolkit.StatusOK || listed.Result.Code != "services.listed" {
		t.Fatalf("list services failed: %+v", listed.Result)
	}

	created := postChat(t, handler, "create_booking", map[string]any{
		"service_id": "wash_and_trim",
		"start_at":   "2026-08-29T15:00:00+08:00",
	}, "booking-1")
	if created.Result.Status != toolkit.StatusOK || created.Result.Code != "booking.created" {
		t.Fatalf("create booking failed: %+v", created.Result)
	}
	bookingID, ok := created.Result.Facts["booking_id"].(string)
	if !ok || bookingID == "" {
		t.Fatalf("booking id missing: %+v", created.Result)
	}
	booking, ok := application.LookupBooking("customer-1", "booking-1")
	if !ok || booking.ID != bookingID || booking.CustomerID != "customer-1" {
		t.Fatalf("trusted customer was not used: %+v", booking)
	}

	replayed := postChat(t, handler, "create_booking", map[string]any{
		"service_id": "wash_and_trim",
		"start_at":   "2026-08-29T15:00:00+08:00",
	}, "booking-1")
	if replayed.Result.Status != toolkit.StatusOK || replayed.Result.Facts["booking_id"] != bookingID {
		t.Fatalf("idempotent replay failed: %+v", replayed.Result)
	}
}

func TestStarterRejectsCustomerIdentityInBusinessParameters(t *testing.T) {
	definition := StarterProfile()
	application, err := NewMemoryApplication(definition)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewStarterRegistry(application, definition)
	if err != nil {
		t.Fatal(err)
	}
	result := postChat(t, NewChatHandler(registry, starterTestContext), "create_booking", map[string]any{
		"customer_id": "attacker",
		"service_id":  "wash_and_trim",
		"start_at":    "2026-08-29T15:00:00+08:00",
	}, "booking-attack")
	if result.Result.Status != toolkit.StatusNeedsInput || result.Result.Code != "booking.invalid_request" {
		t.Fatalf("trusted identity field should be rejected: %+v", result.Result)
	}
	if _, ok := application.LookupBooking("customer-1", "booking-attack"); ok {
		t.Fatal("rejected request must not create a booking")
	}
}

func TestStarterWebChatPersistsThroughSQLiteCore(t *testing.T) {
	storage.SetupTestDB(t)
	application := &sqliteStarterApplication{}
	registry, err := NewStarterRegistry(application, StarterProfile())
	if err != nil {
		t.Fatal(err)
	}
	result := postChat(t, NewChatHandler(registry, starterTestContext), "create_booking", map[string]any{
		"service_id": "wash_and_trim",
		"start_at":   "2026-08-29T15:00:00+08:00",
	}, "sqlite-booking-1")
	if result.Result.Status != toolkit.StatusOK {
		t.Fatalf("sqlite booking failed: %+v", result.Result)
	}
	var bookingCount, outboxCount int64
	if err := storage.DB.Model(&storage.BookingRecord{}).Count(&bookingCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := storage.DB.Model(&storage.BookingOutboxRecord{}).Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if bookingCount != 1 || outboxCount != 1 {
		t.Fatalf("sqlite core did not persist booking and outbox: bookings=%d outbox=%d", bookingCount, outboxCount)
	}
}

func starterTestContext(request *http.Request) (v1alpha1.ExecutionContext, error) {
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		idempotencyKey = "test-request"
	}
	return v1alpha1.ExecutionContext{
		MerchantID: "merchant-1", LocationID: "location-1", CustomerID: "customer-1", PrincipalID: "principal-1",
		Permissions: []v1alpha1.Permission{v1alpha1.PermissionBookingRead, v1alpha1.PermissionBookingWrite},
		TraceID:     "trace-1", IdempotencyKey: idempotencyKey,
	}, nil
}

func postChat(t *testing.T, handler http.Handler, tool string, parameters map[string]any, idempotencyKey string) ChatResponse {
	t.Helper()
	encodedParameters, err := json.Marshal(parameters)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(ChatRequest{Tool: tool, Parameters: encodedParameters})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("chat status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response ChatResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

type sqliteStarterApplication struct{}

func (sqliteStarterApplication) Execute(_ context.Context, trusted v1alpha1.ExecutionContext, call v1alpha1.Call) (v1alpha1.Response, error) {
	response := v1alpha1.Response{Operation: call.Operation}
	if err := trusted.ValidateFor(call.Operation); err != nil {
		return starterResponse(response, toolkit.FromError(err), err)
	}
	if call.Operation != v1alpha1.OperationCreateBooking {
		err := v1alpha1.NewError(v1alpha1.ErrorCodeInvalidOperation, "sqlite starter only handles create_booking")
		return starterResponse(response, toolkit.FromError(err), err)
	}
	var input struct {
		ServiceID string `json:"service_id"`
		StaffID   string `json:"staff_id"`
		StartAt   string `json:"start_at"`
	}
	if err := json.Unmarshal(call.Parameters, &input); err != nil || input.ServiceID == "" || input.StartAt == "" {
		err := v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "service_id and start_at are required")
		return starterResponse(response, toolkit.FromError(err), err)
	}
	startAt, err := time.Parse(time.RFC3339, input.StartAt)
	if err != nil {
		validationErr := v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "start_at must use RFC3339")
		return starterResponse(response, toolkit.FromError(validationErr), validationErr)
	}
	staffID := input.StaffID
	if staffID == "" {
		staffID = "staff-1"
	}
	service := domain.Service{
		ID: input.ServiceID, MerchantID: trusted.MerchantID, LocationID: trusted.LocationID, Name: "洗护修剪",
		Duration: time.Hour, BufferBefore: 15 * time.Minute, BufferAfter: 15 * time.Minute, Active: true,
		ResourceRequirements: []domain.ResourceRequirement{{Kind: "grooming_table", Quantity: 1}},
	}
	staff := domain.Staff{ID: staffID, MerchantID: trusted.MerchantID, LocationID: trusted.LocationID, Name: "Starter 护理师", Active: true}
	booking, err := domain.NewBooking(uuid.NewString(), trusted.MerchantID, trusted.LocationID, trusted.CustomerID, trusted.IdempotencyKey, service, staff, startAt)
	if err != nil {
		return starterResponse(response, toolkit.FromError(v1alpha1.WrapError(v1alpha1.ErrorCodeInvalidContext, "booking parameters are invalid", err)), err)
	}
	allocations := []domain.Allocation{
		{ID: booking.ID + "-staff", BookingID: booking.ID, MerchantID: trusted.MerchantID, LocationID: trusted.LocationID, StaffID: staffID},
		{ID: booking.ID + "-resource", BookingID: booking.ID, MerchantID: trusted.MerchantID, LocationID: trusted.LocationID, ResourceID: "grooming-table-1"},
	}
	record, err := storage.CreateBookingWithAllocations(context.Background(), booking, allocations)
	if err != nil {
		return starterResponse(response, toolkit.FromError(v1alpha1.WrapError(v1alpha1.ErrorCodeUnavailable, "sqlite booking failed", err)), err)
	}
	return starterResponse(response, toolkit.NewOK("booking.created", "预约已创建", map[string]any{
		"booking_id": record.ID, "service_id": record.ServiceID, "staff_id": record.StaffID,
	}), nil)
}

var _ v1alpha1.Application = sqliteStarterApplication{}
