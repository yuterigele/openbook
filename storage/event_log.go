package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// EventType 枚举（与 EventLog.EventType 字段约定）
const (
	EventFirstAppointment    = "first_appointment"
	EventD3Active            = "d3_active"
	EventD15Active           = "d15_active"
	EventD25RenewReminder    = "d25_renew_reminder"
	EventD7ExpiredWarning    = "d7_expired_warning"
	EventRenewed             = "renewed"
	EventExpired             = "expired"
	EventCancelled           = "cancelled"
	EventAppointmentCreated  = "appointment_created"
	EventAppointmentCancelled = "appointment_cancelled"
	EventAppointmentNoShow   = "appointment_noshow"
	EventAppointmentCompleted = "appointment_completed"
	EventBlacklisted           = "customer_blacklisted" // P3 自动黑名单
	// P4 理发师请假
	EventBarberLeaveCreated    = "barber_leave_created"
	EventBarberLeaveCancelled  = "barber_leave_cancelled" // 商户撤销
	EventAppointmentRescheduled = "appointment_rescheduled" // P4 改派
	// EventIdleSlotPush 是前缀（拼 date+customerID），便于幂等
	EventIdleSlotPush = "idle_slot_push"
)

// TrackEvent 写入一条埋点（no-op 当 DB 未初始化）
//
// 用于 PRD §11.2 续费转化漏斗 + 任意业务事件分析。
// 失败只 log 不 panic —— 埋点失败不能阻塞主链路。
func TrackEvent(ctx context.Context, shopID, eventType string, refID string, meta any) {
	if DB == nil {
		return
	}
	var metaStr string
	if meta != nil {
		if b, err := json.Marshal(meta); err == nil {
			metaStr = string(b)
		}
	}
	rec := EventLog{
		ShopID:    shopID,
		EventType: eventType,
		RefID:     refID,
		Meta:      metaStr,
		CreatedAt: time.Now(),
	}
	if err := DB.WithContext(ctx).Create(&rec).Error; err != nil {
		// 埋点失败只 log，不影响业务
		// (生产可接 sentry / datadog)
		// 这里直接 silent —— 避免污染日志
		_ = err
	}
}

// HasShopEvent 判断某店铺是否已经触发过某类事件（用于幂等：首次预约等节点）
func HasShopEvent(ctx context.Context, shopID, eventType string) (bool, error) {
	if DB == nil {
		return false, nil
	}
	var n int64
	err := DB.WithContext(ctx).Model(&EventLog{}).
		Where("shop_id = ? AND event_type = ?", shopID, eventType).
		Count(&n).Error
	return n > 0, err
}

// FindShopsForLifecycle 找出需要触发 D+N 节点的店铺（按 first_appointment 时间计算）
//   - days: 距 first_appointment 多少天
//   - eventType: 准备触发的事件类型
//
// 关于时间字段：
//   - MySQL (go-sql-driver): driver 把 DATETIME 直接转 time.Time
//   - SQLite (modernc.org): driver 返回 string（RFC3339Nano 或 SQLite 默认格式）
// 用 map[string]any 中转 + parseAnyTime 灵活解析，跨 driver 兼容。
func FindShopsForLifecycle(ctx context.Context, days int, eventType string) []string {
	if DB == nil {
		return nil
	}
	var rows []map[string]any
	err := DB.WithContext(ctx).
		Table("event_logs").
		Select("shop_id, MIN(created_at) as first_at").
		Where("event_type = ?", EventFirstAppointment).
		Group("shop_id").
		Scan(&rows).Error
	if err != nil {
		return nil
	}

	now := time.Now()
	var due []string
	for _, r := range rows {
		shopID, _ := r["shop_id"].(string)
		if shopID == "" {
			continue
		}
		firstAt, ok := parseAnyTime(r["first_at"])
		if !ok || firstAt.IsZero() {
			continue
		}
		dueAt := firstAt.AddDate(0, 0, days)
		// 当天容差 ±6 小时
		if now.After(dueAt.Add(-6*time.Hour)) && now.Before(dueAt.Add(6*time.Hour)) {
			// 还没触发过该 event
			has, _ := HasShopEvent(ctx, shopID, eventType)
			if !has {
				due = append(due, shopID)
			}
		}
	}
	return due
}

// parseAnyTime 把数据库驱动返回的任意时间值转成 time.Time。
//
// 支持：
//   - time.Time（MySQL go-sql-driver）
//   - string / []byte（SQLite modernc.org），按多种格式解析
//   - int64（Unix 秒）
//   - nil → 返回 zero time, ok=false
func parseAnyTime(v any) (time.Time, bool) {
	if v == nil {
		return time.Time{}, false
	}
	switch x := v.(type) {
	case time.Time:
		return x, true
	case []byte:
		return parseTimeString(string(x))
	case string:
		return parseTimeString(x)
	case int64:
		return time.Unix(x, 0), true
	case int:
		return time.Unix(int64(x), 0), true
	default:
		return time.Time{}, false
	}
}

func parseTimeString(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, f := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.999999999Z",
	} {
		if t, err := time.Parse(f, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// CountShopEvents 统计某店铺某事件触发次数（eventType 为空时统计所有）
func CountShopEvents(ctx context.Context, shopID, eventType string) int64 {
	if DB == nil {
		return 0
	}
	var n int64
	q := DB.WithContext(ctx).Model(&EventLog{}).Where("shop_id = ?", shopID)
	if eventType != "" {
		q = q.Where("event_type = ?", eventType)
	}
	q.Count(&n)
	return n
}

// MarkAppointmentCompleted 把预约标记为 completed（商户在后台点"已完成"）
//
// 事务边界：
//   - status 检查 + 更新必须带 WHERE status='active' 条件，防并发把已 noshow 的改成 completed
//   - 顾客 total_visits +1 必须在同一事务内
//   - TrackEvent 在事务内调用（写 event_logs 也参与回滚）
func MarkAppointmentCompleted(ctx context.Context, apptID string) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var appt Appointment
		if err := tx.Where("id = ?", apptID).First(&appt).Error; err != nil {
			return err
		}
		if appt.Status == "completed" {
			return nil // 幂等
		}
		if appt.Status != "active" {
			return fmt.Errorf("appointment %s is in status %q, cannot mark completed", apptID, appt.Status)
		}
		now := time.Now()
		// 带 WHERE status='active' 兜底，并发安全
		res := tx.Model(&Appointment{}).
			Where("id = ? AND status = ?", apptID, "active").
			Updates(map[string]interface{}{
				"status":     "completed",
				"updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("appointment %s status changed, retry", apptID)
		}
		// 同一事务内增加 total_visits + 更新 last_visit_at
		if appt.CustomerID != "" {
			if err := tx.Model(&Customer{}).
				Where("id = ?", appt.CustomerID).
				Updates(map[string]interface{}{
					"total_visits":  gorm.Expr("total_visits + 1"),
					"last_visit_at": now,
					"updated_at":    now,
				}).Error; err != nil {
				return err
			}

			// 自动加 FREQUENT 标签（≥5 次）
			// 注意：cust.TotalVisits 已是 +1 后的值（上面已 UPDATE），所以直接 ≥ 5 判断即可
			var cust Customer
			if err := tx.Where("id = ?", appt.CustomerID).First(&cust).Error; err == nil {
				if cust.TotalVisits >= 5 && !NewTagSet(cust.Tags).Has(TagFrequent) {
					ts := NewTagSet(cust.Tags)
					ts.Add(TagFrequent)
					tx.Model(&cust).Update("tags", ts.String())
				}
			}
		}
		// 埋点（也走事务）
		rec := EventLog{
			ShopID:    appt.ShopID,
			EventType: EventAppointmentCompleted,
			RefID:     apptID,
			CreatedAt: now,
		}
		if err := tx.Create(&rec).Error; err != nil {
			return err
		}
		return nil
	})
}