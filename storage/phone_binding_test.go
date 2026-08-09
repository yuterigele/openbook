package storage

import (
	"context"
	"errors"
	"testing"
)

func TestBindVerifiedPhoneUpdatesSameMessagingCustomer(t *testing.T) {
	SetupTestDB(t)
	c := MakeCustomer(t, "张三", 0, 0)
	c.Phone = "13800138000"
	c.ExternalUserID = "ext-zhang"
	if err := DB.Save(c).Error; err != nil {
		t.Fatal(err)
	}
	if err := BindVerifiedPhone(context.Background(), "13900139000", c.WechatOpenID, c.ExternalUserID); err != nil {
		t.Fatal(err)
	}
	got := reloadCustomer(t, c.ID)
	if got.Phone != "13900139000" {
		t.Fatalf("手机号未更新，got %q", got.Phone)
	}
	if got.PhoneVerifiedAt == nil {
		t.Fatal("验证成功后必须记录手机号验证时间")
	}
}

func TestBindVerifiedPhoneCreatesVerifiedIdentity(t *testing.T) {
	SetupTestDB(t)
	if err := BindVerifiedPhone(context.Background(), "13800138000", "wx-new", "ext-new"); err != nil {
		t.Fatal(err)
	}
	c, err := GetCustomerByMessagingIdentity(context.Background(), "wx-new", "ext-new")
	if err != nil {
		t.Fatal(err)
	}
	if c.Phone != "13800138000" || c.PhoneVerifiedAt == nil {
		t.Fatalf("首次验证档案不完整: %+v", c)
	}
}

func TestBindVerifiedPhoneRejectsAnotherCustomer(t *testing.T) {
	SetupTestDB(t)
	a := MakeCustomer(t, "甲", 0, 0)
	a.ExternalUserID = "ext-a"
	a.Phone = "13800138000"
	if err := DB.Save(a).Error; err != nil {
		t.Fatal(err)
	}
	b := MakeCustomer(t, "乙", 0, 0)
	b.ExternalUserID = "ext-b"
	b.Phone = "13900139000"
	if err := DB.Save(b).Error; err != nil {
		t.Fatal(err)
	}
	err := BindVerifiedPhone(context.Background(), b.Phone, a.WechatOpenID, a.ExternalUserID)
	if !errors.Is(err, ErrPhoneAlreadyBound) {
		t.Fatalf("应拒绝绑定其他顾客手机号，got %v", err)
	}
}
