package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/intent"
	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/sdk/toolkit"
)

func TestNewToolCatalogUsesExplicitAllowlist(t *testing.T) {
	catalog, err := newToolCatalog(intent.NewClassifyTool(intent.NewClassifier()))
	if err != nil {
		t.Fatalf("catalog creation failed: %v", err)
	}

	wantNames := []string{
		"barber_leave", "cancel_appointment", "classify_intent", "create_appointment",
		"get_appointment", "handoff_to_human", "list_barbers", "list_my_bookings",
		"list_services", "list_shop_holidays", "query_schedule", "sensitive_check",
	}
	got := catalog.Tools()
	if len(got) != len(wantNames) {
		t.Fatalf("catalog tool count = %d, want %d", len(got), len(wantNames))
	}
	for index, runtimeTool := range got {
		info, infoErr := runtimeTool.Info(context.Background())
		if infoErr != nil {
			t.Fatalf("tool %d info failed: %v", index, infoErr)
		}
		if info.Name != wantNames[index] {
			t.Fatalf("tool %d = %q, want %q", index, info.Name, wantNames[index])
		}
	}

	create, ok := catalog.descriptors.Get("create_appointment")
	if !ok || create.Mode != toolkit.ModeWrite || create.Permission != v1alpha1.PermissionBookingWrite || create.Retry.MaxAttempts != 1 {
		t.Fatalf("create metadata is unsafe: %+v found=%v", create, ok)
	}
	query, ok := catalog.descriptors.Get("query_schedule")
	if !ok || query.Mode != toolkit.ModeRead || query.Retry.MaxAttempts != 2 {
		t.Fatalf("query metadata is unexpected: %+v found=%v", query, ok)
	}
}

func TestToolCatalogRejectsRuntimeNameMismatchAndDuplicate(t *testing.T) {
	catalog := &toolCatalog{
		descriptors: toolkit.NewDescriptorRegistry(),
		tools:       make(map[string]tool.BaseTool),
	}
	descriptor := readDescriptor("list_services", "查询服务", v1alpha1.OperationListServices)
	if err := catalog.register(descriptor, &fakeInvokableTool{name: "other_tool"}); err == nil {
		t.Fatal("runtime metadata name mismatch should be rejected")
	}
	if err := catalog.register(descriptor, &fakeInvokableTool{name: "list_services", output: "ok"}); err != nil {
		t.Fatalf("valid runtime tool rejected: %v", err)
	}
	if err := catalog.register(descriptor, &fakeInvokableTool{name: "list_services", output: "ok"}); !errors.Is(err, toolkit.ErrDuplicateTool) {
		t.Fatalf("duplicate runtime tool should be rejected, got %v", err)
	}

	wrapped := catalog.tools["list_services"].(tool.InvokableTool)
	if _, err := wrapped.InvokableRun(context.Background(), `{}`); v1alpha1.CodeOf(err) != v1alpha1.ErrorCodeInvalidContext {
		t.Fatalf("missing trusted context should be rejected, got %v", err)
	}
	trusted := v1alpha1.WithExecutionContext(context.Background(), v1alpha1.ExecutionContext{
		MerchantID:  "merchant-1",
		LocationID:  "location-1",
		TraceID:     "trace-1",
		Permissions: []v1alpha1.Permission{v1alpha1.PermissionBookingRead},
	})
	output, err := wrapped.InvokableRun(trusted, `{}`)
	if err != nil || output != "ok" {
		t.Fatalf("trusted invocation failed: output=%q err=%v", output, err)
	}
}

func TestToolCatalogRoutesBusinessToolThroughApplication(t *testing.T) {
	application := &fakeBookingApplication{}
	catalog, err := newToolCatalog(intent.NewClassifyTool(intent.NewClassifier()), application)
	if err != nil {
		t.Fatalf("catalog creation failed: %v", err)
	}
	create := catalog.tools["create_appointment"].(tool.InvokableTool)
	trusted := v1alpha1.WithExecutionContext(context.Background(), v1alpha1.ExecutionContext{
		MerchantID:     "merchant-1",
		LocationID:     "location-1",
		CustomerID:     "customer-1",
		TraceID:        "trace-1",
		IdempotencyKey: "idem-1",
		Permissions:    []v1alpha1.Permission{v1alpha1.PermissionBookingWrite},
	})
	output, err := create.InvokableRun(trusted, `{"service":"剪发"}`)
	if err != nil {
		t.Fatalf("application-backed tool failed: %v", err)
	}
	if output == "" || application.operation != v1alpha1.OperationCreateBooking || application.parameters != `{"service":"剪发"}` {
		t.Fatalf("application call was not forwarded: operation=%q parameters=%q output=%q", application.operation, application.parameters, output)
	}
	if _, ok := catalog.tools["reschedule_appointment"]; !ok {
		t.Fatal("application catalog should register reschedule_appointment")
	}
}

func TestApplicationToolPreservesBusinessToolSchema(t *testing.T) {
	catalog, err := newToolCatalog(intent.NewClassifyTool(intent.NewClassifier()), &fakeBookingApplication{})
	if err != nil {
		t.Fatalf("catalog creation failed: %v", err)
	}
	info, err := catalog.tools["create_appointment"].Info(context.Background())
	if err != nil {
		t.Fatalf("application tool info failed: %v", err)
	}
	if info.Desc == "" || info.ParamsOneOf == nil {
		t.Fatalf("application tool lost model metadata: %+v", info)
	}
	params, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("application tool schema conversion failed: %v", err)
	}
	if _, ok := params.Properties.Get("barber_name"); !ok {
		t.Fatalf("application tool schema lost barber_name: %+v", params)
	}
}

type fakeInvokableTool struct {
	name   string
	output string
}

type fakeBookingApplication struct {
	operation  v1alpha1.Operation
	parameters string
}

func (a *fakeBookingApplication) Execute(_ context.Context, _ v1alpha1.ExecutionContext, call v1alpha1.Call) (v1alpha1.Response, error) {
	a.operation = call.Operation
	a.parameters = string(call.Parameters)
	result := toolkit.NewOK("booking.created", "预约已创建", map[string]any{"booking_id": "booking-1"})
	payload, _ := json.Marshal(result)
	return v1alpha1.Response{Operation: call.Operation, Data: payload}, nil
}

func (t *fakeInvokableTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (t *fakeInvokableTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return t.output, nil
}
