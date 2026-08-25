package storage

import (
	"context"
	"time"
)

// ListActiveAppointmentsForCustomer 返回指定门店顾客的未来 active 预约。
// 查询始终同时约束门店和顾客，避免把其他门店或其他顾客的预约交给 Agent。
func ListActiveAppointmentsForCustomer(ctx context.Context, shopID, customerID string) ([]Appointment, error) {
	if shopID == "" || customerID == "" {
		return nil, ErrCustomerIdentityRequired
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	today := time.Now().In(loc).Format("2006-01-02")

	var appointments []Appointment
	err = mustDB().WithContext(ctx).
		Where("shop_id = ? AND customer_id = ? AND status = ? AND date >= ?", shopID, customerID, "active", today).
		Order("date asc, time asc").
		Limit(20).
		Find(&appointments).Error
	return appointments, err
}
