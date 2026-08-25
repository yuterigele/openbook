// Package hair 提供美发行业参考 Profile。
package hair

import (
	"time"

	"github.com/yuterigele/openbook/sdk/profile"
)

// Definition 返回美发行业 Profile 定义。
func Definition() profile.Definition {
	return profile.Definition{
		ID:            "hair",
		DisplayName:   "美发",
		Terms:         profile.Terms{Staff: "发型师", Service: "服务项目", Resource: "工位"},
		StartInterval: 30 * time.Minute,
		ResourceTypes: []profile.ResourceType{
			{ID: "station", DisplayName: "工位", Exclusive: true},
		},
		Services: []profile.Service{
			{ID: "haircut", DisplayName: "剪发", Duration: 30 * time.Minute, BufferAfter: 10 * time.Minute},
			{
				ID:          "color",
				DisplayName: "染发",
				Duration:    120 * time.Minute,
				BufferAfter: 20 * time.Minute,
				RequiredResources: []profile.ResourceRequirement{
					{ResourceTypeID: "station", Quantity: 1},
				},
			},
			{
				ID:          "perm",
				DisplayName: "烫发",
				Duration:    150 * time.Minute,
				BufferAfter: 20 * time.Minute,
				RequiredResources: []profile.ResourceRequirement{
					{ResourceTypeID: "station", Quantity: 1},
				},
			},
		},
		RequiredCustomerFields: []string{"name", "phone"},
		TemplateVariables:      []string{"customer_name", "service_name", "staff_name", "start_at", "end_at", "booking_id"},
		ReplyTemplates: map[string]string{
			"booking_confirmed": "已为 {{customer_name}} 预约 {{service_name}}，由 {{staff_name}} 服务，时间 {{start_at}}-{{end_at}}。预约号：{{booking_id}}",
			"need_phone":        "为了完成预约，请提供手机号。",
		},
	}
}
