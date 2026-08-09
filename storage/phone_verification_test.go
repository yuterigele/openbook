package storage

import (
	"context"
	"testing"
	"time"
)

func TestSaveAndDeleteTestPhoneVerificationCode(t *testing.T) {
	SetupTestDB(t)
	ctx := context.Background()
	if err := SaveTestPhoneVerificationCode(ctx, "code-1", "shop-1", "13800138000", "123456", time.Now().Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var row PhoneVerificationCode
	if err := DB.First(&row, "id = ?", "code-1").Error; err != nil {
		t.Fatal(err)
	}
	if row.Code != "123456" {
		t.Fatalf("验证码不正确，got %q", row.Code)
	}
	if err := DeleteTestPhoneVerificationCode(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	var count int64
	DB.Model(&PhoneVerificationCode{}).Where("id = ?", row.ID).Count(&count)
	if count != 0 {
		t.Fatal("验证成功后测试验证码未删除")
	}
}
