// Package beauty 提供美容行业参考 Profile。
package beauty

import (
	"time"

	"github.com/yuterigele/openbook/sdk/profile"
)

// Definition 返回美容行业 Profile 定义。
func Definition() profile.Definition {
	return profile.Definition{
		ID:            "beauty",
		DisplayName:   "美容",
		Terms:         profile.Terms{Staff: "美容师", Service: "护理项目", Resource: "房间/床位"},
		StartInterval: 30 * time.Minute,
		ResourceTypes: []profile.ResourceType{
			{ID: "room", DisplayName: "护理房间", Exclusive: true},
			{ID: "bed", DisplayName: "护理床位", Exclusive: true},
			{ID: "device", DisplayName: "护理仪器", Exclusive: true},
		},
		Services: []profile.Service{
			{
				ID:          "hydration",
				DisplayName: "补水护理",
				Duration:    60 * time.Minute,
				BufferAfter: 15 * time.Minute,
				RequiredResources: []profile.ResourceRequirement{
					{ResourceTypeID: "room", Quantity: 1},
					{ResourceTypeID: "bed", Quantity: 1},
				},
			},
			{
				ID:          "deep_cleaning",
				DisplayName: "深层清洁",
				Duration:    75 * time.Minute,
				BufferAfter: 15 * time.Minute,
				RequiredResources: []profile.ResourceRequirement{
					{ResourceTypeID: "room", Quantity: 1},
					{ResourceTypeID: "bed", Quantity: 1},
				},
			},
			{
				ID:          "facial_care",
				DisplayName: "面部护理",
				Duration:    90 * time.Minute,
				BufferAfter: 15 * time.Minute,
				RequiredResources: []profile.ResourceRequirement{
					{ResourceTypeID: "room", Quantity: 1},
					{ResourceTypeID: "bed", Quantity: 1},
					{ResourceTypeID: "device", Quantity: 1},
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
