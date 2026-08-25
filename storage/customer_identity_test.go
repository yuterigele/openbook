package storage

import (
	"context"
	"errors"
	"testing"
)

func TestGetCustomerByIDUsesTrustedIdentity(t *testing.T) {
	SetupTestDB(t)
	if err := DB.Create(&Customer{ID: "customer-1", Name: "可信顾客", Phone: "13800000001"}).Error; err != nil {
		t.Fatal(err)
	}
	customer, err := GetCustomerByID(context.Background(), "customer-1")
	if err != nil {
		t.Fatal(err)
	}
	if customer.ID != "customer-1" || customer.Name != "可信顾客" {
		t.Fatalf("unexpected customer: %+v", customer)
	}
	if _, err := GetCustomerByID(context.Background(), "customer-missing"); !errors.Is(err, ErrAppointmentForbidden) {
		t.Fatalf("missing customer should be forbidden, got %v", err)
	}
}
