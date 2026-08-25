package v1alpha1

import (
	"context"
	"testing"
)

func TestExecutionContextCarrierIsDefensive(t *testing.T) {
	permissions := []Permission{PermissionBookingRead, PermissionBookingWrite}
	executionContext := ExecutionContext{
		MerchantID:  "merchant-1",
		LocationID:  "location-1",
		TraceID:     "trace-1",
		Permissions: permissions,
	}
	ctx := WithExecutionContext(context.Background(), executionContext)
	permissions[0] = Permission("forged")

	got, ok := ExecutionContextFromContext(ctx)
	if !ok {
		t.Fatal("execution context should be present")
	}
	if got.Permissions[0] != PermissionBookingRead {
		t.Fatalf("stored permissions were mutated through input slice: %+v", got.Permissions)
	}
	got.Permissions[0] = Permission("forged-again")
	again, ok := ExecutionContextFromContext(ctx)
	if !ok || again.Permissions[0] != PermissionBookingRead {
		t.Fatalf("returned permissions must be defensive copies: %+v", again.Permissions)
	}
}

func TestExecutionContextCarrierMissing(t *testing.T) {
	if _, ok := ExecutionContextFromContext(context.Background()); ok {
		t.Fatal("missing execution context should not be reported as present")
	}
	if _, ok := ExecutionContextFromContext(nil); ok {
		t.Fatal("nil context should not be reported as present")
	}
}
