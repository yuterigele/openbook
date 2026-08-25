package toolkit

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
)

func TestRegistryRejectsUnsafeSpecs(t *testing.T) {
	registry := NewRegistry()
	base := ToolSpec{
		Name:        "query_availability",
		Description: "查询可预约时段",
		Operation:   v1alpha1.OperationQueryAvailability,
		Mode:        ModeRead,
		Permission:  v1alpha1.PermissionBookingRead,
		Retry:       ReadRetryPolicy(),
		Handler:     successfulHandler,
	}
	if err := registry.Register(base); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	if err := registry.Register(base); !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("duplicate tool should be rejected, got %v", err)
	}

	unsafeWrite := base
	unsafeWrite.Name = "create_booking"
	unsafeWrite.Operation = v1alpha1.OperationCreateBooking
	unsafeWrite.Mode = ModeWrite
	unsafeWrite.Permission = v1alpha1.PermissionBookingWrite
	unsafeWrite.Retry = RetryPolicy{MaxAttempts: 2}
	if err := registry.Register(unsafeWrite); !errors.Is(err, ErrInvalidToolSpec) {
		t.Fatalf("write retry must be rejected, got %v", err)
	}

	badName := base
	badName.Name = "QueryAvailability"
	if err := registry.Register(badName); !errors.Is(err, ErrInvalidToolSpec) {
		t.Fatalf("non-snake tool name must be rejected, got %v", err)
	}
}

func TestRegistryInvokeChecksRegistrationContextAndPermission(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(ToolSpec{
		Name:        "list_services",
		Description: "查询服务",
		Operation:   v1alpha1.OperationListServices,
		Mode:        ModeRead,
		Permission:  v1alpha1.PermissionBookingRead,
		Retry:       ReadRetryPolicy(),
		Handler:     successfulHandler,
	}); err != nil {
		t.Fatal(err)
	}

	base := validExecutionContext()
	base.Permissions = nil
	result, err := registry.Invoke(context.Background(), base, "list_services", json.RawMessage(`{}`))
	if v1alpha1.CodeOf(err) != v1alpha1.ErrorCodeForbidden || result.Status != StatusForbidden {
		t.Fatalf("missing permission should be forbidden, result=%+v err=%v", result, err)
	}

	result, err = registry.Invoke(context.Background(), validExecutionContext(), "unknown_tool", nil)
	if !errors.Is(err, ErrToolNotFound) || result.Status != StatusNeedsInput {
		t.Fatalf("unregistered tool should be rejected, result=%+v err=%v", result, err)
	}
}

func TestRegistryRetriesReadButNeverRetriesWrite(t *testing.T) {
	registry := NewRegistry()
	var readCalls atomic.Int32
	if err := registry.Register(ToolSpec{
		Name:        "query_availability",
		Description: "查询可预约时段",
		Operation:   v1alpha1.OperationQueryAvailability,
		Mode:        ModeRead,
		Permission:  v1alpha1.PermissionBookingRead,
		Retry:       ReadRetryPolicy(),
		Handler: func(context.Context, v1alpha1.ExecutionContext, json.RawMessage) (Result, error) {
			if readCalls.Add(1) == 1 {
				return Result{}, errors.New("temporary read failure")
			}
			return successfulResult(), nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	readResult, readErr := registry.Invoke(context.Background(), validExecutionContext(), "query_availability", nil)
	if readErr != nil || readResult.Status != StatusOK || readCalls.Load() != 2 {
		t.Fatalf("read should retry once, result=%+v err=%v calls=%d", readResult, readErr, readCalls.Load())
	}

	var writeCalls atomic.Int32
	if err := registry.Register(ToolSpec{
		Name:        "create_booking",
		Description: "创建预约",
		Operation:   v1alpha1.OperationCreateBooking,
		Mode:        ModeWrite,
		Permission:  v1alpha1.PermissionBookingWrite,
		Retry:       WriteRetryPolicy(),
		Handler: func(context.Context, v1alpha1.ExecutionContext, json.RawMessage) (Result, error) {
			writeCalls.Add(1)
			return Result{}, errors.New("write failed")
		},
	}); err != nil {
		t.Fatal(err)
	}

	writeResult, writeErr := registry.Invoke(context.Background(), validExecutionContext(), "create_booking", nil)
	if writeErr == nil || writeResult.Status != StatusUnknown || writeCalls.Load() != 1 {
		t.Fatalf("write must not retry, result=%+v err=%v calls=%d", writeResult, writeErr, writeCalls.Load())
	}
}

func TestRegistryRejectsInvalidHandlerResult(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(ToolSpec{
		Name:        "list_staff",
		Description: "查询员工",
		Operation:   v1alpha1.OperationListStaff,
		Mode:        ModeRead,
		Permission:  v1alpha1.PermissionBookingRead,
		Retry:       ReadRetryPolicy(),
		Handler: func(context.Context, v1alpha1.ExecutionContext, json.RawMessage) (Result, error) {
			return Result{Status: StatusOK}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Invoke(context.Background(), validExecutionContext(), "list_staff", nil)
	if err == nil || result.Status != StatusUnknown {
		t.Fatalf("invalid result should fail safely, result=%+v err=%v", result, err)
	}
}

func successfulHandler(context.Context, v1alpha1.ExecutionContext, json.RawMessage) (Result, error) {
	return successfulResult(), nil
}

func successfulResult() Result {
	return NewOK("booking.ok", "操作已完成", map[string]any{})
}

func validExecutionContext() v1alpha1.ExecutionContext {
	return v1alpha1.ExecutionContext{
		MerchantID:     "merchant-1",
		LocationID:     "location-1",
		CustomerID:     "customer-1",
		TraceID:        "trace-1",
		IdempotencyKey: "idem-1",
		Permissions: []v1alpha1.Permission{
			v1alpha1.PermissionBookingRead,
			v1alpha1.PermissionBookingWrite,
		},
	}
}
