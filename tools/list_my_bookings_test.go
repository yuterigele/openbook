package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/yuterigele/openbook/storage"
)

func TestListMyBookingsToolScopesShopAndCustomer(t *testing.T) {
	setupToolsTestDB(t)
	shop := makeToolsShop(t, "shop-bookings")
	customer := makeToolsCustomer(t, "Alice", 0)
	otherCustomer := makeToolsCustomer(t, "Mallory", 0)

	future := "2099-01-01"
	first := makeToolsAppointment(t, shop.ID, customer.ID, customer.Name, "Tony", future, "10:00")
	second := makeToolsAppointment(t, shop.ID, customer.ID, customer.Name, "Kevin", future, "14:00")
	makeToolsAppointment(t, shop.ID, otherCustomer.ID, otherCustomer.Name, "Tony", future, "11:00")
	makeToolsAppointment(t, "other-shop", customer.ID, customer.Name, "Tony", future, "12:00")

	ctx := WithOpenID(WithShopID(context.Background(), shop.ID), customer.WechatOpenID)
	output, err := (&ListMyBookingsTool{}).InvokableRun(ctx, `{}`)
	if err != nil {
		t.Fatalf("list bookings failed: %v", err)
	}
	if !strings.Contains(output, "2 个未来预约") || !strings.Contains(output, appointmentDisplayNumber(first.ID)) || !strings.Contains(output, appointmentDisplayNumber(second.ID)) {
		t.Fatalf("unexpected booking output: %s", output)
	}
	if strings.Contains(output, first.ID) || strings.Contains(output, second.ID) || (customer.Phone != "" && strings.Contains(output, customer.Phone)) || strings.Contains(output, "11:00") || strings.Contains(output, "12:00") {
		t.Fatalf("booking output leaked out-of-scope or sensitive data: %s", output)
	}
}

func TestListMyBookingsToolRequiresTrustedCustomerIdentity(t *testing.T) {
	setupToolsTestDB(t)
	ctx := WithShopID(context.Background(), "shop-bookings")
	_, err := (&ListMyBookingsTool{}).InvokableRun(ctx, `{}`)
	if err == nil || !strings.Contains(err.Error(), "身份") {
		t.Fatalf("missing customer identity should fail safely, got %v", err)
	}
}

func makeToolsShop(t *testing.T, id string) *storage.Shop {
	t.Helper()
	return storage.MakeShop(t, id, "")
}
