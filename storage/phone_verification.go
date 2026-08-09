package storage

import (
	"context"
	"time"
)

// SaveTestPhoneVerificationCode 保存不发送短信时供测试查询的验证码。
func SaveTestPhoneVerificationCode(ctx context.Context, id, shopID, phone, code string, expiresAt time.Time) error {
	row := PhoneVerificationCode{ID: id, ShopID: shopID, Phone: phone, Code: code, ExpiresAt: expiresAt, CreatedAt: time.Now()}
	return mustDB().WithContext(ctx).Save(&row).Error
}

// DeleteTestPhoneVerificationCode 在验证成功后删除明文测试验证码。
func DeleteTestPhoneVerificationCode(ctx context.Context, id string) error {
	return mustDB().WithContext(ctx).Where("id = ?", id).Delete(&PhoneVerificationCode{}).Error
}
