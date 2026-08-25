package toolkit

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
)

func TestResultGoldenFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/availability-found.json")
	if err != nil {
		t.Fatal(err)
	}
	result := NewOK("availability.found", "8 月 28 日下午有 3 个可预约时段", map[string]any{
		"date": "2026-08-28",
		"slots": []any{
			map[string]any{
				"start_at": "2026-08-28T14:00:00+08:00",
				"end_at":   "2026-08-28T15:00:00+08:00",
				"timezone": "Asia/Shanghai",
				"display":  "8 月 28 日 14:00-15:00",
			},
		},
	}, "选择时段", "更换服务人员")
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(encoded)) != strings.TrimSpace(string(fixture)) {
		t.Fatalf("golden mismatch\n got: %s\nwant: %s", encoded, fixture)
	}
}

func TestResultValidateContract(t *testing.T) {
	valid := NewOK("availability.found", "有可预约时段", map[string]any{
		"slot": TimeFact{
			StartAt:  "2026-08-28T14:00:00+08:00",
			EndAt:    "2026-08-28T15:00:00+08:00",
			Timezone: "Asia/Shanghai",
			Display:  "8 月 28 日 14:00-15:00",
		},
	}, "选择时段")
	if err := valid.Validate(DefaultBudget); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}

	invalidStatus := valid
	invalidStatus.Status = Status("working")
	if err := invalidStatus.Validate(DefaultBudget); err == nil {
		t.Fatal("unknown status should be rejected")
	}

	sensitive := NewOK("availability.found", "有可预约时段", map[string]any{"phone": "13812345678"})
	if err := sensitive.Validate(DefaultBudget); err == nil {
		t.Fatal("phone must not be exposed in tool facts")
	}

	tooManyFields := NewOK("availability.found", "有可预约时段", map[string]any{
		"a": 1, "b": 2, "c": 3,
	})
	if err := tooManyFields.Validate(Budget{MaxFields: 2, MaxChars: 6000}); err == nil {
		t.Fatal("field budget should be enforced")
	}
}

func TestTimeFactAndErrorMapping(t *testing.T) {
	invalidTime := NewOK("availability.found", "有可预约时段", map[string]any{
		"slot": map[string]any{
			"start_at": "2026-08-28 14:00",
			"end_at":   "2026-08-28T15:00:00+08:00",
			"timezone": "Asia/Shanghai",
			"display":  "14:00-15:00",
		},
	})
	if err := invalidTime.Validate(DefaultBudget); err == nil {
		t.Fatal("non-RFC3339 time must be rejected")
	}

	maintenance := FromError(v1alpha1.NewError(v1alpha1.ErrorCodeMaintenance, "database password=secret"))
	if maintenance.Status != StatusMaintenance || maintenance.Code != "booking.maintenance" {
		t.Fatalf("unexpected maintenance result: %+v", maintenance)
	}
	if strings.Contains(maintenance.Summary, "database") || strings.Contains(maintenance.Summary, "secret") {
		t.Fatal("error details must not leak into model summary")
	}
	if err := maintenance.Validate(DefaultBudget); err != nil {
		t.Fatalf("maintenance result invalid: %v", err)
	}
}
