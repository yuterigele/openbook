package v1alpha1

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestExecutionContextValidateFor(t *testing.T) {
	base := ExecutionContext{
		MerchantID:  "merchant-1",
		LocationID:  "location-1",
		CustomerID:  "customer-1",
		TraceID:     "trace-1",
		Permissions: []Permission{PermissionBookingRead, PermissionBookingWrite},
	}

	if err := base.ValidateFor(OperationQueryAvailability); err != nil {
		t.Fatalf("query availability context should be valid: %v", err)
	}
	if err := base.ValidateFor(OperationCreateBooking); err == nil {
		t.Fatal("write operation without idempotency key should fail")
	}

	base.IdempotencyKey = "idem-1"
	if err := base.ValidateFor(OperationCreateBooking); err != nil {
		t.Fatalf("create booking context should be valid: %v", err)
	}
	if CodeOf(errForMissingCustomer(base)) != ErrorCodeInvalidContext {
		t.Fatal("missing customer should use invalid context code")
	}
	if err := base.ValidateFor(Operation("shell")); CodeOf(err) != ErrorCodeInvalidOperation {
		t.Fatalf("unknown operation should be rejected, got %v", err)
	}
}

func errForMissingCustomer(base ExecutionContext) error {
	base.CustomerID = ""
	return base.ValidateFor(OperationCreateBooking)
}

func TestCallDoesNotContainTrustedIdentityFields(t *testing.T) {
	call := Call{Operation: OperationCreateBooking, Parameters: json.RawMessage(`{"service":"haircut"}`)}
	b, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"merchant_id", "location_id", "customer_id", "permissions"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("call parameters must not carry trusted field %q: %s", forbidden, b)
		}
	}
}

func TestApplicationPortAndErrorCode(t *testing.T) {
	var _ Application = fakeApplication{}
	cause := errors.New("database detail")
	err := WrapError(ErrorCodeConflict, "slot is occupied", cause)
	if CodeOf(err) != ErrorCodeConflict {
		t.Fatalf("unexpected error code: %s", CodeOf(err))
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause should remain available to internal error handling")
	}
	if err.Error() != "booking.conflict: slot is occupied" {
		t.Fatalf("unexpected safe error text: %v", err)
	}
	_ = context.Background()
}

type fakeApplication struct{}

func (fakeApplication) Execute(_ context.Context, _ ExecutionContext, call Call) (Response, error) {
	return Response{Operation: call.Operation}, nil
}
