package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/intent"
	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/sdk/toolkit"
	"github.com/yuterigele/openbook/tools"
)

func TestNewToolCatalogUsesExplicitAllowlist(t *testing.T) {
	catalog, err := newToolCatalog(intent.NewClassifyTool(intent.NewClassifier()))
	if err != nil {
		t.Fatalf("catalog creation failed: %v", err)
	}

	wantNames := []string{
		"barber_leave", "cancel_appointment", "classify_intent", "create_appointment",
		"get_appointment", "handoff_to_human", "list_barbers", "list_services",
		"list_shop_holidays", "query_schedule", "sensitive_check",
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
	if err := catalog.register(descriptor, &fakeBaseTool{name: "other_tool"}); err == nil {
		t.Fatal("runtime metadata name mismatch should be rejected")
	}
	if err := catalog.register(descriptor, &tools.ListServicesTool{}); err != nil {
		t.Fatalf("valid runtime tool rejected: %v", err)
	}
	if err := catalog.register(descriptor, &tools.ListServicesTool{}); !errors.Is(err, toolkit.ErrDuplicateTool) {
		t.Fatalf("duplicate runtime tool should be rejected, got %v", err)
	}
}

type fakeBaseTool struct {
	name string
}

func (t *fakeBaseTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}
