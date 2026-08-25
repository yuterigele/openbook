// Package nail 提供美甲行业参考 Profile。
package nail

import (
	"time"

	"github.com/yuterigele/openbook/sdk/profile"
)

// Definition 返回美甲行业 Profile 定义。
func Definition() profile.Definition {
	return profile.Definition{
		ID:            "nail",
		DisplayName:   "美甲",
		Terms:         profile.Terms{Staff: "美甲师", Service: "美甲项目", Resource: "美甲工位"},
		StartInterval: 30 * time.Minute,
		ResourceTypes: []profile.ResourceType{
			{ID: "station", DisplayName: "美甲工位", Exclusive: true},
			{ID: "lamp", DisplayName: "光疗灯", Exclusive: true},
		},
		Services: []profile.Service{
			{
				ID: "basic_manicure", DisplayName: "基础护理", Duration: 45 * time.Minute, BufferAfter: 10 * time.Minute,
				RequiredResources: []profile.ResourceRequirement{{ResourceTypeID: "station", Quantity: 1}},
			},
			{
				ID: "gel_nail", DisplayName: "光疗美甲", Duration: 90 * time.Minute, BufferAfter: 15 * time.Minute,
				RequiredResources: []profile.ResourceRequirement{
					{ResourceTypeID: "station", Quantity: 1},
					{ResourceTypeID: "lamp", Quantity: 1},
				},
			},
			{
				ID: "gel_removal", DisplayName: "卸甲", Duration: 30 * time.Minute, BufferAfter: 10 * time.Minute,
				RequiredResources: []profile.ResourceRequirement{{ResourceTypeID: "station", Quantity: 1}},
			},
		},
		RequiredCustomerFields: []string{"name", "phone"},
		TemplateVariables:      []string{"customer_name", "service_name", "staff_name", "start_at", "end_at", "booking_id"},
		ReplyTemplates: map[string]string{
			"booking_confirmed": "已为 {{customer_name}} 预约 {{service_name}}，由 {{staff_name}} 服务，时间 {{start_at}}-{{end_at}}。预约号：{{booking_id}}",
			"need_phone":        "为了完成美甲预约，请提供手机号。",
		},
	}
}
