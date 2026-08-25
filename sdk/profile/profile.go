// Package profile 定义可显式注册的行业 Profile。
//
// Profile 只提供术语、服务、资源模板和回复模板，不拥有身份校验、事务、
// 冲突判断或数据库访问能力。
package profile

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	// ErrInvalidDefinition 表示 Profile 定义不符合 v1 规则。
	ErrInvalidDefinition = errors.New("invalid profile definition")
	// ErrDuplicateID 表示注册了重复的 Profile ID。
	ErrDuplicateID = errors.New("duplicate profile id")
	// ErrProfileNotFound 表示请求的 Profile 不存在。
	ErrProfileNotFound = errors.New("profile not found")
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var templateVariablePattern = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)
var phonePattern = regexp.MustCompile(`(^|[^0-9])1[0-9]{10}([^0-9]|$)`)

// Definition 是一个行业 Profile 的完整定义。
type Definition struct {
	ID                     string            `json:"id"`
	DisplayName            string            `json:"display_name"`
	Terms                  Terms             `json:"terms"`
	StartInterval          time.Duration     `json:"start_interval"`
	ResourceTypes          []ResourceType    `json:"resource_types"`
	Services               []Service         `json:"services"`
	RequiredCustomerFields []string          `json:"required_customer_fields"`
	TemplateVariables      []string          `json:"template_variables"`
	ReplyTemplates         map[string]string `json:"reply_templates"`
}

// Terms 是顾客界面使用的行业术语。
type Terms struct {
	Staff    string `json:"staff"`
	Service  string `json:"service"`
	Resource string `json:"resource"`
}

// ResourceType 是 Profile 可使用的独占资源类型。
type ResourceType struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Exclusive   bool   `json:"exclusive"`
}

// Service 是一个可预约服务模板。
type Service struct {
	ID                string                `json:"id"`
	DisplayName       string                `json:"display_name"`
	Duration          time.Duration         `json:"duration"`
	BufferBefore      time.Duration         `json:"buffer_before"`
	BufferAfter       time.Duration         `json:"buffer_after"`
	RequiredResources []ResourceRequirement `json:"required_resources"`
}

// ResourceRequirement 表示服务需要占用一种资源及其数量。
type ResourceRequirement struct {
	ResourceTypeID string `json:"resource_type_id"`
	Quantity       int    `json:"quantity"`
}

// Validate 校验 Profile 定义，不访问数据库，也不加载任意插件。
func (d Definition) Validate() error {
	if !identifierPattern.MatchString(d.ID) {
		return fmt.Errorf("%w: invalid profile id %q", ErrInvalidDefinition, d.ID)
	}
	if strings.TrimSpace(d.DisplayName) == "" {
		return fmt.Errorf("%w: display name is required", ErrInvalidDefinition)
	}
	if strings.TrimSpace(d.Terms.Staff) == "" || strings.TrimSpace(d.Terms.Service) == "" || strings.TrimSpace(d.Terms.Resource) == "" {
		return fmt.Errorf("%w: staff, service and resource terms are required", ErrInvalidDefinition)
	}
	if d.StartInterval <= 0 {
		return fmt.Errorf("%w: start interval must be positive", ErrInvalidDefinition)
	}
	if len(d.Services) == 0 {
		return fmt.Errorf("%w: at least one service is required", ErrInvalidDefinition)
	}

	resourceTypes := make(map[string]ResourceType, len(d.ResourceTypes))
	for _, resource := range d.ResourceTypes {
		if !identifierPattern.MatchString(resource.ID) || strings.TrimSpace(resource.DisplayName) == "" {
			return fmt.Errorf("%w: invalid resource type %q", ErrInvalidDefinition, resource.ID)
		}
		if !resource.Exclusive {
			return fmt.Errorf("%w: resource type %q must be exclusive in v1", ErrInvalidDefinition, resource.ID)
		}
		if _, exists := resourceTypes[resource.ID]; exists {
			return fmt.Errorf("%w: duplicate resource type %q", ErrInvalidDefinition, resource.ID)
		}
		resourceTypes[resource.ID] = resource
	}

	serviceIDs := make(map[string]struct{}, len(d.Services))
	for _, service := range d.Services {
		if !identifierPattern.MatchString(service.ID) || strings.TrimSpace(service.DisplayName) == "" {
			return fmt.Errorf("%w: invalid service %q", ErrInvalidDefinition, service.ID)
		}
		if _, exists := serviceIDs[service.ID]; exists {
			return fmt.Errorf("%w: duplicate service %q", ErrInvalidDefinition, service.ID)
		}
		serviceIDs[service.ID] = struct{}{}
		if service.Duration <= 0 || service.Duration > 24*time.Hour {
			return fmt.Errorf("%w: service %q duration is invalid", ErrInvalidDefinition, service.ID)
		}
		if service.BufferBefore < 0 || service.BufferAfter < 0 {
			return fmt.Errorf("%w: service %q buffer cannot be negative", ErrInvalidDefinition, service.ID)
		}
		seenResources := make(map[string]struct{}, len(service.RequiredResources))
		for _, requirement := range service.RequiredResources {
			resource, exists := resourceTypes[requirement.ResourceTypeID]
			if !exists {
				return fmt.Errorf("%w: service %q references unknown resource %q", ErrInvalidDefinition, service.ID, requirement.ResourceTypeID)
			}
			if !resource.Exclusive {
				return fmt.Errorf("%w: service %q references non-exclusive resource %q", ErrInvalidDefinition, service.ID, requirement.ResourceTypeID)
			}
			if requirement.Quantity <= 0 {
				return fmt.Errorf("%w: service %q resource quantity must be positive", ErrInvalidDefinition, service.ID)
			}
			if _, exists := seenResources[requirement.ResourceTypeID]; exists {
				return fmt.Errorf("%w: service %q repeats resource %q", ErrInvalidDefinition, service.ID, requirement.ResourceTypeID)
			}
			seenResources[requirement.ResourceTypeID] = struct{}{}
		}
	}

	if err := validateIdentifiers("customer field", d.RequiredCustomerFields); err != nil {
		return err
	}
	if err := validateIdentifiers("template variable", d.TemplateVariables); err != nil {
		return err
	}
	if err := validateTemplates(d.ReplyTemplates, d.TemplateVariables); err != nil {
		return err
	}
	return nil
}

func validateIdentifiers(kind string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !identifierPattern.MatchString(value) {
			return fmt.Errorf("%w: invalid %s %q", ErrInvalidDefinition, kind, value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%w: duplicate %s %q", ErrInvalidDefinition, kind, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateTemplates(templates map[string]string, declared []string) error {
	declaredSet := make(map[string]struct{}, len(declared))
	for _, variable := range declared {
		declaredSet[variable] = struct{}{}
	}
	for name, content := range templates {
		if strings.TrimSpace(name) == "" || phonePattern.MatchString(content) {
			return fmt.Errorf("%w: reply template %q contains invalid content", ErrInvalidDefinition, name)
		}
		matches := templateVariablePattern.FindAllStringSubmatch(content, -1)
		if strings.Count(content, "{{") != len(matches) || strings.Count(content, "}}") != len(matches) {
			return fmt.Errorf("%w: reply template %q contains malformed variable", ErrInvalidDefinition, name)
		}
		for _, match := range matches {
			variable := match[1]
			if _, exists := declaredSet[variable]; !exists {
				return fmt.Errorf("%w: reply template %q uses undeclared variable %q", ErrInvalidDefinition, name, variable)
			}
		}
	}
	return nil
}

// Registry 是显式注册的 Profile 集合。
type Registry struct {
	profiles map[string]Definition
}

// NewRegistry 创建空的 Profile 注册表。
func NewRegistry() *Registry {
	return &Registry{profiles: make(map[string]Definition)}
}

// Register 校验并注册 Profile；重复 ID 会被拒绝。
func (r *Registry) Register(definition Definition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("%w: registry is nil", ErrInvalidDefinition)
	}
	if r.profiles == nil {
		r.profiles = make(map[string]Definition)
	}
	if _, exists := r.profiles[definition.ID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateID, definition.ID)
	}
	r.profiles[definition.ID] = clone(definition)
	return nil
}

// Get 按 ID 获取 Profile 的副本，避免调用方修改注册表内部状态。
func (r *Registry) Get(id string) (Definition, error) {
	if r == nil {
		return Definition{}, ErrProfileNotFound
	}
	definition, exists := r.profiles[id]
	if !exists {
		return Definition{}, fmt.Errorf("%w: %s", ErrProfileNotFound, id)
	}
	return clone(definition), nil
}

// List 返回按 ID 排序的 Profile 副本。
func (r *Registry) List() []Definition {
	if r == nil {
		return []Definition{}
	}
	ids := make([]string, 0, len(r.profiles))
	for id := range r.profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	profiles := make([]Definition, 0, len(ids))
	for _, id := range ids {
		profiles = append(profiles, clone(r.profiles[id]))
	}
	return profiles
}

func clone(definition Definition) Definition {
	definition.ResourceTypes = append([]ResourceType(nil), definition.ResourceTypes...)
	definition.Services = append([]Service(nil), definition.Services...)
	for index := range definition.Services {
		definition.Services[index].RequiredResources = append([]ResourceRequirement(nil), definition.Services[index].RequiredResources...)
	}
	definition.RequiredCustomerFields = append([]string(nil), definition.RequiredCustomerFields...)
	definition.TemplateVariables = append([]string(nil), definition.TemplateVariables...)
	templates := make(map[string]string, len(definition.ReplyTemplates))
	for key, value := range definition.ReplyTemplates {
		templates[key] = value
	}
	definition.ReplyTemplates = templates
	return definition
}
