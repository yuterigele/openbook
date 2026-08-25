package profile

import (
	"errors"
	"testing"
	"time"
)

func validDefinition() Definition {
	return Definition{
		ID:            "sample",
		DisplayName:   "示例行业",
		Terms:         Terms{Staff: "技师", Service: "项目", Resource: "工位"},
		StartInterval: 30 * time.Minute,
		ResourceTypes: []ResourceType{{ID: "station", DisplayName: "工位", Exclusive: true}},
		Services: []Service{{
			ID:          "basic_service",
			DisplayName: "基础服务",
			Duration:    30 * time.Minute,
			RequiredResources: []ResourceRequirement{{
				ResourceTypeID: "station",
				Quantity:       1,
			}},
		}},
		RequiredCustomerFields: []string{"name", "phone"},
		TemplateVariables:      []string{"customer_name", "service_name"},
		ReplyTemplates: map[string]string{
			"confirmed": "已为 {{customer_name}} 安排 {{service_name}}",
		},
	}
}

func TestDefinitionValidateAndRegistry(t *testing.T) {
	definition := validDefinition()
	if err := definition.Validate(); err != nil {
		t.Fatalf("valid definition rejected: %v", err)
	}
	registry := NewRegistry()
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(definition); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate id error = %v", err)
	}
	got, err := registry.Get("sample")
	if err != nil {
		t.Fatal(err)
	}
	got.Services[0].DisplayName = "被调用方修改"
	copy, err := registry.Get("sample")
	if err != nil {
		t.Fatal(err)
	}
	if copy.Services[0].DisplayName != "基础服务" {
		t.Fatal("registry returned internal mutable state")
	}
	if gotList := registry.List(); len(gotList) != 1 || gotList[0].ID != "sample" {
		t.Fatalf("unexpected registry list: %+v", gotList)
	}
}

func TestDefinitionRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Definition)
	}{
		{name: "invalid id", change: func(d *Definition) { d.ID = "Sample" }},
		{name: "unknown resource", change: func(d *Definition) {
			d.Services[0].RequiredResources[0].ResourceTypeID = "room"
		}},
		{name: "non exclusive resource", change: func(d *Definition) { d.ResourceTypes[0].Exclusive = false }},
		{name: "undeclared template variable", change: func(d *Definition) {
			d.ReplyTemplates["confirmed"] = "{{unknown}}"
		}},
		{name: "malformed template variable", change: func(d *Definition) {
			d.ReplyTemplates["confirmed"] = "{{customer-name}}"
		}},
		{name: "real phone in template", change: func(d *Definition) {
			d.ReplyTemplates["confirmed"] = "请联系 13812345678"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			definition := validDefinition()
			tc.change(&definition)
			if err := definition.Validate(); err == nil {
				t.Fatal("unsafe or incomplete definition should be rejected")
			}
		})
	}
}
