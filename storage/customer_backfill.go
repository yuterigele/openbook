package storage

// customer_backfill.go —— v4.8 顾客档案自愈
//
// 背景：早期版本 CreateAppointmentWithShop 只写 appointments 表，不建 customers 档案。
// 导致：
//   - admin 顾客列表空（listCustomersHandler 过滤掉无 appointment 关联的顾客）
//   - admin 顾客详情 404（getCustomerDetailHandler 用 customer_id 找不到顾客）
//   - 累计统计 / 黑名单判定都没数据
//
// 修法：InitDB 启动时跑一次幂等回填——
//   1) 找出 appointments.customer_id = '' 且 customer <> '' 的，按 customer 名字去重
//   2) 复用 upsertCustomerInTx 的查找顺序：openID → externalUserID → name（老数据没 openID，会落到 name）
//   3) 把 appointment.customer_id 回填上
//   4) 同时累计 customers.total_visits / no_show_count / last_visit_at 等基础字段
//
// 幂等保证：
//   - 重复跑不会重复建顾客（按 name 唯一匹配）
//   - 重复跑不会重复回填（WHERE customer_id = '' 兜底）
//   - total_visits 等用 SQL 重新算，每次结果一致

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BackfillMissingCustomers 自愈顾客档案（v4.8）
//
// 流程：
//  1. 扫 appointments WHERE customer_id = '' AND customer <> ''，按 customer 去重
//  2. 对每个 name：复用 upsertCustomerInTx（按 name 命中已有顾客 or 新建）
//  3. UPDATE appointments SET customer_id = ? WHERE customer = ? AND customer_id = ''
//  4. 重新累计每个顾客的 total_visits / no_show_count / late_cancel_count / last_visit_at
//
// 日志：[storage] BackfillMissingCustomers: 新建 N / 复用 M / 回填 P 条
func BackfillMissingCustomers(ctx context.Context) error {
	if DB == nil {
		return nil
	}

	// 1) 收集需要处理的顾客名字
	type nameRow struct {
		Name string
	}
	var names []nameRow
	if err := DB.WithContext(ctx).
		Table("appointments").
		Select("DISTINCT customer AS name").
		Where("customer <> '' AND (customer_id = '' OR customer_id IS NULL)").
		Scan(&names).Error; err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}

	var created, reused, apptFilled int
	for _, nr := range names {
		name := nr.Name
		if name == "" {
			continue
		}

		// 2) 复用 upsertCustomerInTx（openID/externalUserID 都空，按 name 走第 3 分支）
		//    这里不能直接调 upsertCustomerInTx（它要 *gorm.DB tx），用单条 SQL 包在事务里更省事
		var cust Customer
		txErr := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("name = ?", name).First(&cust).Error; err == nil {
				reused++
				return nil
			}
			cust = Customer{
				ID:        uuid.NewString(),
				Name:      name,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			if err := tx.Create(&cust).Error; err != nil {
				return err
			}
			created++
			return nil
		})
		if txErr != nil {
			log.Printf("[storage] backfill customer '%s' 失败: %v", name, txErr)
			continue
		}

		// 3) 回填该顾客的 appointment.customer_id
		res := DB.WithContext(ctx).Model(&Appointment{}).
			Where("customer = ? AND (customer_id = '' OR customer_id IS NULL)", name).
			Update("customer_id", cust.ID)
		if res.Error != nil {
			log.Printf("[storage] backfill appt.customer_id for '%s' 失败: %v", name, res.Error)
			continue
		}
		apptFilled += int(res.RowsAffected)
	}

	// 4) 累计每个顾客的统计字段（基于已回填的 appointments）
	//    用 SQL 一次扫完所有顾客，省 N+1
	if err := DB.WithContext(ctx).Exec(`
		UPDATE customers c
		LEFT JOIN (
			SELECT customer_id,
			       SUM(CASE WHEN status IN ('active','completed') THEN 1 ELSE 0 END) AS total_visits,
			       SUM(CASE WHEN status = 'noshow'    THEN 1 ELSE 0 END) AS no_show_count,
			       SUM(CASE WHEN status = 'cancelled' THEN 1 ELSE 0 END) AS late_cancel_count,
			       MAX(STR_TO_DATE(CONCAT(date, ' ', time), '%Y-%m-%d %H:%i')) AS last_visit_at
			FROM appointments
			WHERE customer_id <> ''
			GROUP BY customer_id
		) a ON a.customer_id = c.id
		SET c.total_visits     = COALESCE(a.total_visits, 0),
		    c.no_show_count    = COALESCE(a.no_show_count, 0),
		    c.late_cancel_count = COALESCE(a.late_cancel_count, 0),
		    c.last_visit_at    = a.last_visit_at
	`).Error; err != nil {
		log.Printf("[storage] backfill 累计字段失败: %v", err)
	}

	log.Printf("[storage] BackfillMissingCustomers: 新建=%d 复用=%d 回填预约=%d", created, reused, apptFilled)
	return nil
}