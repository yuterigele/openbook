package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/sdk/toolkit"
	"github.com/yuterigele/openbook/storage"
)

func TestBookingApplicationsRuntimeExecutesTrustedCreate(t *testing.T) {
	storage.SetupTestDB(t)
	shop := storage.MakeShop(t, "main-runtime-shop", "")
	storage.MakeBarber(t, "main-runtime-barber", shop.ID, "Tony")
	customer := storage.MakeCustomer(t, "可信顾客", 0, 0)
	if err := storage.DB.Model(&storage.Customer{}).Where("id = ?", customer.ID).Updates(map[string]any{
		"phone":            "13800000001",
		"external_user_id": "external-main-runtime",
	}).Error; err != nil {
		t.Fatalf("update customer identity: %v", err)
	}
	if err := storage.DB.First(customer, "id = ?", customer.ID).Error; err != nil {
		t.Fatalf("reload customer: %v", err)
	}

	t.Setenv("BOOKING_APPLICATION_RUNTIME", "1")
	applications := newBookingApplications()
	if len(applications) != 1 || applications[0] == nil {
		t.Fatalf("runtime applications = %#v, want one application", applications)
	}

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*60*60)
	}
	date := time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	trusted := v1alpha1.ExecutionContext{
		MerchantID: shop.ID, LocationID: shop.ID, CustomerID: customer.ID,
		PrincipalID: storage.AuditActorAgent,
		Permissions: []v1alpha1.Permission{v1alpha1.PermissionBookingRead, v1alpha1.PermissionBookingWrite},
		TraceID:     "main-runtime-trace", IdempotencyKey: "main-runtime-idempotency",
	}
	response, err := applications[0].Execute(context.Background(), trusted, v1alpha1.Call{
		Operation:  v1alpha1.OperationCreateBooking,
		Parameters: json.RawMessage(`{"barber_name":"Tony","customer":"模型伪造顾客","phone":"13800000002","date":"` + date + `","time":"14:00","service":"剪发"}`),
	})
	if err != nil {
		t.Fatalf("main-wired application create failed: %v", err)
	}
	var result toolkit.Result
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatalf("decode application result: %v", err)
	}
	if result.Code != "booking.created" || result.Status != toolkit.StatusOK {
		t.Fatalf("application result = %+v, want booking.created OK", result)
	}

	var appointment storage.Appointment
	if err := storage.DB.Where("shop_id = ? AND date = ? AND time = ?", shop.ID, date, "14:00").First(&appointment).Error; err != nil {
		t.Fatalf("load created appointment: %v", err)
	}
	if appointment.CustomerID != customer.ID || appointment.Customer != customer.Name {
		t.Fatalf("appointment identity = customer_id:%q customer:%q, want trusted customer %q/%q", appointment.CustomerID, appointment.Customer, customer.ID, customer.Name)
	}
	if appointment.Customer == "模型伪造顾客" {
		t.Fatal("model-provided customer identity must not be persisted")
	}
}
