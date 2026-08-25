package server

import (
	"context"
	"testing"

	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/storage"
)

func TestWithAgentExecutionContextMapsLegacyIdentity(t *testing.T) {
	ctx := storage.WithTraceID(context.Background(), "trace-1")
	ctx = withAgentExecutionContext(ctx, "shop-1")

	executionContext, ok := v1alpha1.ExecutionContextFromContext(ctx)
	if !ok {
		t.Fatal("agent execution context should be present")
	}
	if executionContext.MerchantID != "shop-1" || executionContext.LocationID != "shop-1" {
		t.Fatalf("legacy shop identity was not mapped: %+v", executionContext)
	}
	if executionContext.TraceID != "trace-1" || executionContext.PrincipalID != storage.AuditActorAgent {
		t.Fatalf("trusted execution metadata was not preserved: %+v", executionContext)
	}
	if !executionContext.HasPermission(v1alpha1.PermissionBookingRead) || !executionContext.HasPermission(v1alpha1.PermissionBookingWrite) {
		t.Fatalf("agent permissions are incomplete: %+v", executionContext.Permissions)
	}
}
