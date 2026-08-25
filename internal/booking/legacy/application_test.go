package legacy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/sdk/toolkit"
	"github.com/yuterigele/openbook/tools"
)

func TestApplicationQueryUsesTrustedLocationAndReturnsEnvelope(t *testing.T) {
	var gotShopID string
	app := NewApplicationForTest(nil, func(ctx context.Context, arguments string) (string, error) {
		gotShopID = tools.ShopIDFromCtx(ctx)
		if arguments != `{"barber_name":"Tony","date":"2026-08-28"}` {
			t.Fatalf("unexpected business arguments: %s", arguments)
		}
		return "理发师 Tony 在 2026-08-28 有可预约时段：14:00", nil
	}, nil)

	response, err := app.Execute(context.Background(), validReadContext(), v1alpha1.Call{
		Operation:  v1alpha1.OperationQueryAvailability,
		Parameters: json.RawMessage(`{"barber_name":"Tony","date":"2026-08-28"}`),
	})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if gotShopID != "location-trusted" {
		t.Fatalf("tool did not receive trusted location: %q", gotShopID)
	}
	var result toolkit.Result
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Code != "availability.found" || result.Status != toolkit.StatusOK {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestApplicationReadToolsUseTrustedLocation(t *testing.T) {
	var serviceShopID, staffShopID string
	app := &Application{
		listServices: func(ctx context.Context, arguments string) (string, error) {
			serviceShopID = tools.ShopIDFromCtx(ctx)
			if arguments != `{}` {
				t.Fatalf("unexpected service arguments: %s", arguments)
			}
			return "剪发 30 分钟", nil
		},
		listStaff: func(ctx context.Context, arguments string) (string, error) {
			staffShopID = tools.ShopIDFromCtx(ctx)
			if arguments != `{}` {
				t.Fatalf("unexpected staff arguments: %s", arguments)
			}
			return "Tony、Kevin", nil
		},
	}

	for _, operation := range []v1alpha1.Operation{v1alpha1.OperationListServices, v1alpha1.OperationListStaff} {
		response, err := app.Execute(context.Background(), validReadContext(), v1alpha1.Call{
			Operation: operation, Parameters: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatalf("%s failed: %v", operation, err)
		}
		var result toolkit.Result
		if err := json.Unmarshal(response.Data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Status != toolkit.StatusOK {
			t.Fatalf("%s returned unexpected result: %+v", operation, result)
		}
	}
	if serviceShopID != "location-trusted" || staffShopID != "location-trusted" {
		t.Fatalf("read tools did not receive trusted location: services=%q staff=%q", serviceShopID, staffShopID)
	}
}

func TestApplicationCreateOverridesModelCustomerIdentity(t *testing.T) {
	var captured map[string]string
	var capturedOpenID string
	app := NewApplicationForTest(func(_ context.Context, trusted v1alpha1.ExecutionContext) (CustomerIdentity, error) {
		if trusted.CustomerID != "customer-trusted" {
			t.Fatalf("resolver received wrong customer id: %q", trusted.CustomerID)
		}
		return CustomerIdentity{
			Name:           "可信顾客",
			Phone:          "13800000001",
			OpenID:         "openid-trusted",
			ExternalUserID: "external-trusted",
		}, nil
	}, func(context.Context, string) (string, error) {
		return "", errors.New("query should not run")
	}, func(ctx context.Context, arguments string) (string, error) {
		capturedOpenID = tools.OpenIDFromCtx(ctx)
		var params map[string]string
		if err := json.Unmarshal([]byte(arguments), &params); err != nil {
			t.Fatal(err)
		}
		captured = params
		return "预约创建成功！预约号：OB-TEST", nil
	})

	response, err := app.Execute(context.Background(), validWriteContext(), v1alpha1.Call{
		Operation:  v1alpha1.OperationCreateBooking,
		Parameters: json.RawMessage(`{"barber_name":"Tony","customer":"模型指定的人","phone":"13800000002","date":"2026-08-28","time":"14:00","service":"剪发"}`),
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if captured["customer"] != "可信顾客" || captured["phone"] != "13800000001" {
		t.Fatalf("model identity was not overridden: %#v", captured)
	}
	if capturedOpenID != "openid-trusted" {
		t.Fatalf("trusted open id was not injected: %q", capturedOpenID)
	}
	if !strings.Contains(string(response.Data), "booking.created") {
		t.Fatalf("created result missing stable code: %s", response.Data)
	}
}

func TestApplicationCancelUsesTrustedCustomerIdentity(t *testing.T) {
	var capturedShopID, capturedOpenID, capturedExternalID string
	app := &Application{
		resolveCustomer: func(_ context.Context, trusted v1alpha1.ExecutionContext) (CustomerIdentity, error) {
			if trusted.IdempotencyKey != "cancel-idempotency-1" {
				t.Fatalf("resolver received wrong idempotency key: %q", trusted.IdempotencyKey)
			}
			return CustomerIdentity{OpenID: "openid-trusted", ExternalUserID: "external-trusted"}, nil
		},
		cancelBooking: func(ctx context.Context, arguments string) (string, error) {
			capturedShopID = tools.ShopIDFromCtx(ctx)
			capturedOpenID = tools.OpenIDFromCtx(ctx)
			capturedExternalID = tools.ExternalUserIDFromCtx(ctx)
			if arguments != `{"appointment_id":"appt-1","reason":"临时有事"}` {
				t.Fatalf("unexpected cancel arguments: %s", arguments)
			}
			return "预约号：OB-TEST 已成功取消。", nil
		},
	}

	response, err := app.Execute(context.Background(), validCancelContext(), v1alpha1.Call{
		Operation:  v1alpha1.OperationCancelBooking,
		Parameters: json.RawMessage(`{"appointment_id":"appt-1","reason":"临时有事"}`),
	})
	if err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	if capturedShopID != "location-trusted" || capturedOpenID != "openid-trusted" || capturedExternalID != "external-trusted" {
		t.Fatalf("trusted identity was not injected: shop=%q open=%q external=%q", capturedShopID, capturedOpenID, capturedExternalID)
	}
	if !strings.Contains(string(response.Data), "booking.cancelled") {
		t.Fatalf("cancel result missing stable code: %s", response.Data)
	}
}

func validCancelContext() v1alpha1.ExecutionContext {
	context := validReadContext()
	context.CustomerID = "customer-trusted"
	context.IdempotencyKey = "cancel-idempotency-1"
	return context
}

func TestApplicationRejectsTrustedFieldsInBusinessParameters(t *testing.T) {
	app := NewApplicationForTest(nil, func(context.Context, string) (string, error) {
		t.Fatal("runner should not be called")
		return "", nil
	}, nil)
	_, err := app.Execute(context.Background(), validReadContext(), v1alpha1.Call{
		Operation:  v1alpha1.OperationQueryAvailability,
		Parameters: json.RawMessage(`{"barber_name":"Tony","merchant_id":"attacker-merchant"}`),
	})
	if v1alpha1.CodeOf(err) != v1alpha1.ErrorCodeInvalidContext {
		t.Fatalf("trusted field should be rejected, got %v", err)
	}
}

func TestApplicationMapsLegacyErrorWithoutLeak(t *testing.T) {
	app := NewApplicationForTest(nil, func(context.Context, string) (string, error) {
		return "", errors.New("SQL password=secret connection refused")
	}, nil)
	response, err := app.Execute(context.Background(), validReadContext(), v1alpha1.Call{
		Operation:  v1alpha1.OperationQueryAvailability,
		Parameters: json.RawMessage(`{"barber_name":"Tony","date":"2026-08-28"}`),
	})
	if v1alpha1.CodeOf(err) != v1alpha1.ErrorCodeUnavailable {
		t.Fatalf("legacy failure should map to unavailable, got %v", err)
	}
	if strings.Contains(string(response.Data), "SQL") || strings.Contains(string(response.Data), "secret") {
		t.Fatalf("legacy error leaked into result: %s", response.Data)
	}
	var result toolkit.Result
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != toolkit.StatusUnavailable {
		t.Fatalf("unexpected error status: %+v", result)
	}
}

func validReadContext() v1alpha1.ExecutionContext {
	return v1alpha1.ExecutionContext{
		MerchantID: "merchant-trusted",
		LocationID: "location-trusted",
		TraceID:    "trace-1",
	}
}

func validWriteContext() v1alpha1.ExecutionContext {
	context := validReadContext()
	context.CustomerID = "customer-trusted"
	context.IdempotencyKey = "idempotency-1"
	return context
}
