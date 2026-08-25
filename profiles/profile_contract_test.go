package profiles_test

import (
	"testing"
	"time"

	"github.com/yuterigele/openbook/internal/booking/domain"
	"github.com/yuterigele/openbook/profiles"
	"github.com/yuterigele/openbook/profiles/beauty"
	"github.com/yuterigele/openbook/profiles/hair"
	"github.com/yuterigele/openbook/sdk/profile"
)

func TestReferenceRegistryContainsValidatedProfiles(t *testing.T) {
	registry, err := profiles.NewReferenceRegistry()
	if err != nil {
		t.Fatal(err)
	}
	got := registry.List()
	if len(got) != 2 || got[0].ID != "beauty" || got[1].ID != "hair" {
		t.Fatalf("reference profiles = %+v", got)
	}
	for _, definition := range got {
		if err := definition.Validate(); err != nil {
			t.Errorf("profile %s is invalid: %v", definition.ID, err)
		}
		if len(definition.RequiredCustomerFields) == 0 || len(definition.ReplyTemplates) == 0 {
			t.Errorf("profile %s is missing customer fields or templates", definition.ID)
		}
	}
}

func TestHairServicesHaveDifferentOccupancyDurations(t *testing.T) {
	definition := hair.Definition()
	services := serviceMap(definition.Services)
	if services["haircut"].Duration != 30*time.Minute || services["color"].Duration != 120*time.Minute || services["perm"].Duration != 150*time.Minute {
		t.Fatalf("hair service durations are not differentiated: %+v", services)
	}
	if len(services["haircut"].RequiredResources) != 0 {
		t.Fatal("haircut should demonstrate an employee-only service")
	}
	if len(services["color"].RequiredResources) != 1 || services["color"].RequiredResources[0].ResourceTypeID != "station" {
		t.Fatal("color should require a station")
	}
}

func TestBeautyServiceRequiresJointResources(t *testing.T) {
	definition := beauty.Definition()
	services := serviceMap(definition.Services)
	facialCare := services["facial_care"]
	if len(facialCare.RequiredResources) != 3 {
		t.Fatalf("facial care should require room, bed and device: %+v", facialCare.RequiredResources)
	}
	resourceTypes := make(map[string]profile.ResourceType, len(definition.ResourceTypes))
	for _, resource := range definition.ResourceTypes {
		resourceTypes[resource.ID] = resource
	}
	for _, requirement := range facialCare.RequiredResources {
		resource, exists := resourceTypes[requirement.ResourceTypeID]
		if !exists || !resource.Exclusive || requirement.Quantity != 1 {
			t.Fatalf("invalid joint resource requirement: %+v", requirement)
		}
	}

	zone := time.FixedZone("CST", 8*60*60)
	start := time.Date(2026, 8, 26, 10, 0, 0, 0, zone)
	candidate, err := domain.NewServiceInterval(start, facialCare.Duration, facialCare.BufferBefore, facialCare.BufferAfter)
	if err != nil {
		t.Fatal(err)
	}
	staffOccupied, err := domain.NewServiceInterval(start.Add(-time.Hour), time.Hour, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	bedOccupied, err := domain.NewServiceInterval(start, time.Hour, 0, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if domain.HasConflict(candidate, []domain.Interval{staffOccupied}) {
		t.Fatal("staff should be available at the adjacent start boundary")
	}
	if !domain.HasConflict(candidate, []domain.Interval{bedOccupied}) {
		t.Fatal("occupied bed must block an otherwise available staff member")
	}
}

func serviceMap(services []profile.Service) map[string]profile.Service {
	result := make(map[string]profile.Service, len(services))
	for _, service := range services {
		result[service.ID] = service
	}
	return result
}
