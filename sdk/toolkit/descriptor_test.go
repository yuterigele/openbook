package toolkit

import (
	"errors"
	"testing"

	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
)

func TestDescriptorRegistryValidatesMetadata(t *testing.T) {
	registry := NewDescriptorRegistry()
	descriptor := Descriptor{
		Name:        "query_availability",
		Description: "查询可预约时段",
		Operation:   v1alpha1.OperationQueryAvailability,
		Mode:        ModeRead,
		Permission:  v1alpha1.PermissionBookingRead,
		Retry:       ReadRetryPolicy(),
	}
	if err := registry.Register(descriptor); err != nil {
		t.Fatalf("valid descriptor rejected: %v", err)
	}
	got, ok := registry.Get(descriptor.Name)
	if !ok || got.Budget != DefaultBudget {
		t.Fatalf("default budget not applied: got=%+v found=%v", got, ok)
	}
	if err := registry.Register(descriptor); !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("duplicate descriptor should be rejected, got %v", err)
	}

	unsafeWrite := Descriptor{
		Name:        "create_booking",
		Description: "创建预约",
		Operation:   v1alpha1.OperationCreateBooking,
		Mode:        ModeWrite,
		Permission:  v1alpha1.PermissionBookingWrite,
		Retry:       RetryPolicy{MaxAttempts: 2},
	}
	if err := registry.Register(unsafeWrite); !errors.Is(err, ErrInvalidToolSpec) {
		t.Fatalf("write descriptor with retry should be rejected, got %v", err)
	}
}
