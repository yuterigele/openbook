package storage

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// GetCustomerByID 按服务端已经确认的顾客 ID 读取顾客资料。
func GetCustomerByID(ctx context.Context, customerID string) (*Customer, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	customerID = strings.TrimSpace(customerID)
	if customerID == "" {
		return nil, ErrCustomerIdentityRequired
	}
	var customer Customer
	if err := DB.WithContext(ctx).Where("id = ?", customerID).First(&customer).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAppointmentForbidden
		}
		return nil, err
	}
	return &customer, nil
}
