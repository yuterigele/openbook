package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yuterigele/openbook/internal/booking/legacy"
	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/storage"
	"github.com/yuterigele/openbook/tools"
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
	if executionContext.TraceID != "trace-1" || executionContext.IdempotencyKey != "trace-1" || executionContext.PrincipalID != storage.AuditActorAgent {
		t.Fatalf("trusted execution metadata was not preserved: %+v", executionContext)
	}
	if !executionContext.HasPermission(v1alpha1.PermissionBookingRead) || !executionContext.HasPermission(v1alpha1.PermissionBookingWrite) {
		t.Fatalf("agent permissions are incomplete: %+v", executionContext.Permissions)
	}
}

func TestWithAgentExecutionContextResolvesTrustedCustomer(t *testing.T) {
	storage.SetupTestDB(t)
	if err := storage.DB.Create(&storage.Customer{
		ID: "customer-1", WechatOpenID: "openid-1", ExternalUserID: "external-1", Name: "可信顾客",
	}).Error; err != nil {
		t.Fatal(err)
	}
	ctx := storage.WithTraceID(context.Background(), "trace-1")
	ctx = tools.WithOpenID(ctx, "openid-1")
	ctx = tools.WithExternalUserID(ctx, "external-1")
	ctx = withAgentExecutionContext(ctx, "shop-1")

	executionContext, ok := v1alpha1.ExecutionContextFromContext(ctx)
	if !ok || executionContext.CustomerID != "customer-1" {
		t.Fatalf("trusted customer was not resolved: ok=%v context=%+v", ok, executionContext)
	}
}

func TestTrustedCustomerIdentityReachesLegacyCreateAdapter(t *testing.T) {
	storage.SetupTestDB(t)
	if err := storage.DB.Create(&storage.Customer{
		ID: "customer-1", WechatOpenID: "openid-1", ExternalUserID: "external-1", Name: "真实顾客", Phone: "13800000001",
	}).Error; err != nil {
		t.Fatal(err)
	}

	var captured map[string]string
	application := legacy.NewApplicationForTest(
		func(resolveCtx context.Context, trusted v1alpha1.ExecutionContext) (legacy.CustomerIdentity, error) {
			customer, err := storage.GetCustomerByID(resolveCtx, trusted.CustomerID)
			if err != nil {
				return legacy.CustomerIdentity{}, err
			}
			return legacy.CustomerIdentity{Name: customer.Name, Phone: customer.Phone, OpenID: customer.WechatOpenID, ExternalUserID: customer.ExternalUserID}, nil
		},
		func(context.Context, string) (string, error) { return "", nil },
		func(_ context.Context, arguments string) (string, error) {
			return "预约创建成功", json.Unmarshal([]byte(arguments), &captured)
		},
	)
	ctx := storage.WithTraceID(context.Background(), "trace-1")
	ctx = tools.WithOpenID(ctx, "openid-1")
	ctx = tools.WithExternalUserID(ctx, "external-1")
	ctx = withAgentExecutionContext(ctx, "shop-1")
	trusted, ok := v1alpha1.ExecutionContextFromContext(ctx)
	if !ok || trusted.CustomerID != "customer-1" {
		t.Fatalf("trusted customer context missing: ok=%v context=%+v", ok, trusted)
	}

	response, err := application.Execute(ctx, trusted, v1alpha1.Call{
		Operation:  v1alpha1.OperationCreateBooking,
		Parameters: json.RawMessage(`{"barber_name":"Tony","customer":"模型伪造的人","phone":"13800000002","date":"2026-08-28","time":"14:00","service":"剪发"}`),
	})
	if err != nil {
		t.Fatalf("create through trusted adapter failed: %v", err)
	}
	if captured["customer"] != "真实顾客" || captured["phone"] != "13800000001" {
		t.Fatalf("legacy create did not use trusted customer data: %#v", captured)
	}
	if strings.Contains(string(response.Data), "模型伪造的人") || strings.Contains(string(response.Data), "13800000002") {
		t.Fatalf("model identity leaked into response: %s", response.Data)
	}
}
