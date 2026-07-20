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
	EventFirstAppointment     = "first_appointment"
	EventD3Active             = "d3_active"
	EventD15Active            = "d15_active"
	EventD25RenewReminder     = "d25_renew_reminder"
	EventD7ExpiredWarning     = "d7_expired_warning"
	EventRenewed              = "renewed"
	EventExpired              = "expired"
	EventCancelled            = "cancelled"
	EventAppointmentCreated   = "appointment_created"
	EventAppointmentCancelled = "appointment_cancelled"
	EventAppointmentNoShow    = "appointment_noshow"
	EventAppointmentCompleted = "appointment_completed"
	EventBlacklisted          = "customer_blacklisted" // P3 自动黑名单
	// P4 理发师请假
	EventBarberLeaveCreated     = "barber_leave_created"
	EventBarberLeaveCancelled   = "barber_leave_cancelled"  // 商户撤销
	EventBarberLeaveExpired     = "barber_leave_expired"    // cron 自然过期（end_at < now）
	EventAppointmentRescheduled = "appointment_rescheduled" // P4 改派
	// EventIdleSlotPush 是前缀（拼 date+customerID），便于幂等
	EventIdleSlotPush = "idle_slot_push"
	// EventWeeklyReport 周报触发埋点（v4.3 PRD §11.12）
	//   - 每周一 9:00 触发，给每家店写一条
	//   - 同一 shop 同一天只写一次（cron 频率 = 每周 1 次，本身就幂等）
	//   - ref_id = shopID；meta = {week_start, week_end, total_appointments, recipients}
	EventWeeklyReport = "weekly_report"
	// EventHandoffToHuman Agent 主动把顾客转给人工客服（MVP 兜底）
	//   - ref_id = 顾客标识（wechat_open_id 或 customer name）
	//   - meta.reason = Agent 给出的转人工原因（"无法识别意图" / "顾客明确要求" / "业务超出 Agent 能力"）
	//   - meta.last_user_message = 顾客最后一条原文（让商户知道转接上下文）
	EventHandoffToHuman = "handoff_to_human"
	// EventHandoffResolved 商户在后台把转人工标为已处理（v4.6 增量）
	//   - ref_id = 商户后台用户名（或 admin id）
	//   - meta.resolved_from = 关联的 handoff_to_human event_log.id
	//   - meta.customer_id = 顾客标识（便于反查）
	//   - meta.note = 可选备注（商户留的一句话说明）
	//   - 配套 handoff_to_human: 一个 handoff 可以被"已处理"多次（不强制 1:1），
	//     前端 UI 把 resolved event 关联回原 handoff，按"已解决"过滤。
	EventHandoffResolved = "handoff_resolved"
	// EventDemoReply v4.13.1 demo 兜底：mock 模式下"回复"不真正发企业微信，写 event_logs
	//   - AGENT_REPLY_MODE=mock 时启用
	//   - ref_id = "" (无 appointment 关联)
	//   - meta.to_user / meta.open_kfid / meta.reply 携带完整内容
	//   - 商户后台事件流能直接看到 demo 期间的"回复历史"
	EventDemoReply = "demo_reply"
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
//
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

// parseAnyTime 把数据库驱动返回的任意时间值转成 time.Time（包内使用）。
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

// ParseAnyTime 是 parseAnyTime 的导出版本，供其他包（api/cron）复用。
// 见 parseAnyTime 的注释。
func ParseAnyTime(v any) (time.Time, bool) {
	return parseAnyTime(v)
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
				"status":          "completed",
				"active_slot_key": nil,
				"updated_at":      now,
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

// UncompleteAppointment 撤销 MarkAppointmentCompleted（v4.16 P2.2）
//
// 把 status 从 completed 改回 active，同时：
//   - 顾客 total_visits -1（如果 >0）
//   - 写一条 uncomplete 事件（不增加累计，区别于 complete 事件）
//
// 限制：
//   - 必须在 created_at 后 5 分钟内（防止追溯混乱）
//   - 仅当 status='completed' 才允许
func UncompleteAppointment(ctx context.Context, apptID string) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var appt Appointment
		if err := tx.Where("id = ?", apptID).First(&appt).Error; err != nil {
			return err
		}
		if appt.Status != "completed" {
			return fmt.Errorf("appointment %s is in status %q, cannot uncomplete", apptID, appt.Status)
		}
		// 5 分钟内允许撤销（用 updated_at 近似 "完成时间"）
		if time.Since(appt.UpdatedAt) > 5*time.Minute {
			return fmt.Errorf("完成已超过 5 分钟，无法撤销（请在顾客详情手动改状态）")
		}
		now := time.Now()
		activeSlotKey := AppointmentActiveSlotKey(appt.ShopID, appt.BarberID, appt.Date, appt.Time)
		res := tx.Model(&Appointment{}).
			Where("id = ? AND status = ?", apptID, "completed").
			Updates(map[string]interface{}{
				"status":          "active",
				"active_slot_key": activeSlotKey,
				"updated_at":      now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("appointment %s status changed, retry", apptID)
		}
		// 顾客 total_visits -1（用 Exec 直接走 SQL，不被 GORM 的 map 模式吞掉 Where 子句）
		if appt.CustomerID != "" {
			if err := tx.Exec(
				"UPDATE customers SET total_visits = total_visits - 1, updated_at = ? WHERE id = ? AND total_visits > 0",
				now, appt.CustomerID,
			).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// UncancelAppointment 撤销 CancelAppointmentWithPolicy（v4.16 P2.2）
//
// 把 status 从 cancelled 改回 active，清空 cancel_type / cancel_reason / cancelled_at
// 限制：5 分钟内（created_at 后）
func UncancelAppointment(ctx context.Context, apptID string) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var appt Appointment
		if err := tx.Where("id = ?", apptID).First(&appt).Error; err != nil {
			return err
		}
		if appt.Status != "cancelled" {
			return fmt.Errorf("appointment %s is in status %q, cannot uncancel", apptID, appt.Status)
		}
		// cancelled_at 非空：用它判断；否则 fallback 到 updated_at
		checkpoint := appt.UpdatedAt
		if appt.CancelledAt != nil {
			checkpoint = *appt.CancelledAt
		}
		if time.Since(checkpoint) > 5*time.Minute {
			return fmt.Errorf("取消已超过 5 分钟，无法撤销")
		}
		now := time.Now()
		activeSlotKey := AppointmentActiveSlotKey(appt.ShopID, appt.BarberID, appt.Date, appt.Time)
		res := tx.Model(&Appointment{}).
			Where("id = ? AND status = ?", apptID, "cancelled").
			Updates(map[string]interface{}{
				"status":          "active",
				"active_slot_key": activeSlotKey,
				"cancel_type":     "",
				"cancel_reason":   "",
				"cancelled_at":    nil,
				"updated_at":      now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("appointment %s status changed, retry", apptID)
		}
		return nil
	})
}
