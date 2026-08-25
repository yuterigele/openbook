// Package fitness_coach 提供健身私教行业参考 Profile。
package fitness_coach

import (
	"time"

	"github.com/yuterigele/openbook/sdk/profile"
)

// Definition 返回健身私教行业 Profile 定义。
func Definition() profile.Definition {
	return profile.Definition{
		ID:            "fitness_coach",
		DisplayName:   "健身私教",
		Terms:         profile.Terms{Staff: "教练", Service: "训练课程", Resource: "训练场地/器械"},
		StartInterval: 30 * time.Minute,
		ResourceTypes: []profile.ResourceType{
			{ID: "training_area", DisplayName: "训练场地", Exclusive: true},
			{ID: "equipment", DisplayName: "专用器械", Exclusive: true},
		},
		Services: []profile.Service{
			{
				ID: "trial_session", DisplayName: "体验课", Duration: 60 * time.Minute, BufferAfter: 10 * time.Minute,
				RequiredResources: []profile.ResourceRequirement{{ResourceTypeID: "training_area", Quantity: 1}},
			},
			{
				ID: "personal_training", DisplayName: "一对一训练", Duration: 60 * time.Minute, BufferAfter: 10 * time.Minute,
				RequiredResources: []profile.ResourceRequirement{{ResourceTypeID: "training_area", Quantity: 1}},
			},
			{
				ID: "strength_program", DisplayName: "力量训练计划", Duration: 90 * time.Minute, BufferAfter: 15 * time.Minute,
				RequiredResources: []profile.ResourceRequirement{
					{ResourceTypeID: "training_area", Quantity: 1},
					{ResourceTypeID: "equipment", Quantity: 1},
				},
			},
		},
		RequiredCustomerFields: []string{"name", "phone"},
		TemplateVariables:      []string{"customer_name", "service_name", "staff_name", "start_at", "end_at", "booking_id"},
		ReplyTemplates: map[string]string{
			"booking_confirmed": "已为 {{customer_name}} 预约 {{service_name}}，由 {{staff_name}} 教练服务，时间 {{start_at}}-{{end_at}}。预约号：{{booking_id}}",
			"need_phone":        "为了完成训练课程预约，请提供手机号。",
		},
	}
}
