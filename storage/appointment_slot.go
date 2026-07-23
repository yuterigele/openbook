package storage

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// BackfillAppointmentActiveSlotKeys 修复历史数据并建立运行时不变量：active
// 预约必须拥有唯一槽位键，非 active 预约必须为 NULL。若历史数据已经存在重复
// active 槽位，唯一索引会拒绝回填并阻止服务带病启动。
func BackfillAppointmentActiveSlotKeys(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Appointment{}).
			Where("status <> ? AND active_slot_key IS NOT NULL", "active").
			Update("active_slot_key", nil).Error; err != nil {
			return err
		}

		var active []Appointment
		if err := tx.Where("status = ?", "active").Find(&active).Error; err != nil {
			return err
		}
		for i := range active {
			key := AppointmentActiveSlotKey(active[i].ShopID, active[i].BarberID, active[i].Date, active[i].Time)
			if err := tx.Model(&Appointment{}).
				Where("id = ?", active[i].ID).
				Update("active_slot_key", key).Error; err != nil {
				return fmt.Errorf("active appointment slot conflict (appointment=%s barber=%s date=%s time=%s): %w",
					active[i].ID, active[i].BarberID, active[i].Date, active[i].Time, err)
			}
		}
		return nil
	})
}
