package storage

// barber_leave.go
//
// P4 理发师请假（2026-06-21）
//
// 业务场景：理发师临时有事（生病/家里有事/紧急出差），商户在后台点"请假"，
// 系统自动处理该理发师在 [StartAt, EndAt] 区间内的所有未来预约：
//   - action=cancel       : 全部取消 + 微信通知顾客
//   - action=reschedule   : 自动找同档期其他 active 理发师改派；改派失败的兜底取消 + 通知
//
// Penalty 联动（P3）：
//   - 所有取消走 CancelAppointmentWithPolicy(source="admin") → 不计入顾客 late_cancel / no_show
//   - 改派不算取消，只是 appointment.barber_id 改了；走 EventAppointmentRescheduled 埋点
//
// 撤销规则：
//   - 仅当 now < start_at 时允许商户主动撤销（"理发师提前回来了"）
//   - 已开始的请假只能"过期"（自然过渡），不可撤销
//
// 单元隔离：
//   - 本文件只依赖 storage + uuid + wecom/client 通过 sendNotification 注入
//   - sendNotification 抽成函数类型，便于测试时 mock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// LeaveAction 请假处理动作
const (
	LeaveActionCancel     = "cancel"     // 全部取消 + 通知
	LeaveActionReschedule = "reschedule" // 改派优先 + 失败的兜底取消
)

// LeaveStatus 请假状态
const (
	LeaveStatusActive    = "active"    // 生效中
	LeaveStatusCancelled = "cancelled" // 商户主动撤销（仅 StartAt 之前可撤销）
	LeaveStatusExpired   = "expired"   // 已过 EndAt（自然结束）
)

// ErrLeaveNotCancellable 当前请假已开始，不能撤销
//
// 商户想撤销一个 active 但已开始的请假会被这个错挡掉。
var ErrLeaveNotCancellable = errors.New("leave has already started, cannot cancel; please wait for natural expiry")

// NotificationSender 顾客通知发送器（抽象，便于测试时 mock）
//
// 真实实现：wecom.Client.SendTextMessage
// 测试时：用一个把消息记录到 slice 的 fake
type NotificationSender func(ctx context.Context, customerID, text string) error

// defaultNotificationSender 默认实现：从 DB 拿 customer 的 openid/external_userid，调用 wecom
//
// 这里只放接口；具体实现放在 api 层（避免 storage 反向依赖 wecom）。
// 调用方要么注入自己的 sender，要么在调用时把 db_notifications 直接写到 leave_record meta。
var defaultNotificationSender NotificationSender

// LeaveResult 创建/撤销请假的结果
type LeaveResult struct {
	LeaveID           string
	Action            string
	AffectedCount     int      // 受影响的预约总数
	RescheduledCount  int      // 改派成功数
	CancelledCount    int      // 取消数（可能是 action=cancel 全部，也可能是 reschedule 兜底）
	NotifiedCustomers []string // 已通知的 customer_id 列表
}

// FindAppointmentsInRange 查找某理发师在 [start, end] 区间内、状态 active 的预约
//
// 用于 P4 创建假前预览 / 实际处理。
// 性能优化：先用 date 范围粗筛（start 当天 - end+1 当天），再在 Go 侧做 time 精筛。
func FindAppointmentsInRange(ctx context.Context, barberID string, start, end time.Time) ([]Appointment, error) {
	if DB == nil {
		return nil, nil
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	dateFrom := start.In(loc).Format("2006-01-02")
	dateTo := end.In(loc).Format("2006-01-02")

	var appts []Appointment
	if err := DB.WithContext(ctx).
		Where("barber_id = ? AND status = ? AND date >= ? AND date <= ?", barberID, "active", dateFrom, dateTo).
		Find(&appts).Error; err != nil {
		return nil, err
	}
	// 精筛：拼接 date+time → time.Time，确认落在 [start, end] 区间
	var out []Appointment
	for _, a := range appts {
		t, err := time.ParseInLocation("2006-01-02 15:04", a.Date+" "+a.Time, loc)
		if err != nil {
			continue
		}
		// 区间定义为 [start, end]：含端点
		// 注意：end 包含到 end 当天的最后一秒（end = 23:59:59）
		if (t.Equal(start) || t.After(start)) && (t.Equal(end) || t.Before(end)) {
			out = append(out, a)
		}
	}
	return out, nil
}

// CreateBarberLeave 创建一条理发师请假记录，并原子处理受影响预约
//
// 事务边界：
//   - leave row + 预约状态更新在同一事务
//   - 微信通知**不在**事务内（外部 IO 不参与 DB 回滚；失败只 log，不影响 leave row）
//   - 测试时可通过 sender 注入 mock
func CreateBarberLeave(ctx context.Context, leave BarberLeave, sender NotificationSender) (*LeaveResult, error) {
	if leave.BarberID == "" || leave.ShopID == "" {
		return nil, fmt.Errorf("barber_id 和 shop_id 不能为空")
	}
	if !leave.StartAt.Before(leave.EndAt) && !leave.StartAt.Equal(leave.EndAt) {
		return nil, fmt.Errorf("start_at 必须早于 end_at")
	}
	if leave.Action != LeaveActionCancel && leave.Action != LeaveActionReschedule {
		return nil, fmt.Errorf("action 必须是 cancel 或 reschedule，得到 %q", leave.Action)
	}

	// 校验理发师存在
	var barber Barber
	if err := DB.WithContext(ctx).Where("id = ?", leave.BarberID).First(&barber).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("理发师 %s 不存在", leave.BarberID)
		}
		return nil, err
	}

	// 填充冗余 + 默认值
	if leave.ID == "" {
		leave.ID = uuid.NewString()
	}
	leave.BarberName = barber.Name
	if leave.Status == "" {
		leave.Status = LeaveStatusActive
	}
	now := time.Now()
	leave.CreatedAt = now
	leave.UpdatedAt = now

	result := &LeaveResult{
		LeaveID: leave.ID,
		Action:  leave.Action,
	}

	// 受影响的预约
	appts, err := FindAppointmentsInRange(ctx, leave.BarberID, leave.StartAt, leave.EndAt)
	if err != nil {
		return nil, fmt.Errorf("查询受影响预约失败: %w", err)
	}
	result.AffectedCount = len(appts)

	// 事务内：写 leave row + 处理所有受影响预约
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1) 写 leave row
		if err := tx.Create(&leave).Error; err != nil {
			return err
		}

		// 2) 处理每个 appt
		for i := range appts {
			appt := &appts[i]
			switch leave.Action {
			case LeaveActionCancel:
				// 走 P3 策略：source=admin，不计 penalty
				_, err := cancelApptInTx(tx, appt.ID, CancelSourceAdmin,
					fmt.Sprintf("理发师请假：%s", leave.Reason))
				if err != nil {
					return fmt.Errorf("取消预约 %s 失败: %w", appt.ID, err)
				}
				result.CancelledCount++
			case LeaveActionReschedule:
				// 尝试找同档期其他 active 理发师
				newBarberID, found, err := findAlternateBarber(ctx, tx, appt)
				if err != nil {
					return err
				}
				if found {
					// 改派：更新 barber_id / barber_name + 写 reschedule 事件
					if err := tx.Model(&Appointment{}).
						Where("id = ? AND status = ?", appt.ID, "active").
						Updates(map[string]interface{}{
							"barber_id":   newBarberID,
							"barber_name": getBarberName(tx, newBarberID),
							"updated_at":  now,
						}).Error; err != nil {
						return err
					}
					TrackEventInTx(ctx, tx, appt.ShopID, EventAppointmentRescheduled, appt.ID, map[string]any{
						"from_barber_id":   leave.BarberID,
						"from_barber_name": leave.BarberName,
						"to_barber_id":     newBarberID,
						"reason":           "barber_leave:" + leave.Reason,
						"leave_id":         leave.ID,
					})
					result.RescheduledCount++
				} else {
					// 兜底取消
					_, err := cancelApptInTx(tx, appt.ID, CancelSourceAdmin,
						fmt.Sprintf("理发师请假且无替代师傅：%s", leave.Reason))
					if err != nil {
						return fmt.Errorf("改派失败兜底取消 %s 失败: %w", appt.ID, err)
					}
					result.CancelledCount++
				}
			}
		}

		// 3) 更新 leave row 的统计字段
		if err := tx.Model(&BarberLeave{}).
			Where("id = ?", leave.ID).
			Updates(map[string]interface{}{
				"affected_count":     result.AffectedCount,
				"rescheduled_count":  result.RescheduledCount,
				"cancelled_count":    result.CancelledCount,
				"updated_at":         now,
			}).Error; err != nil {
			return err
		}

		// 4) 写 barber_leave_created 事件
		TrackEventInTx(ctx, tx, leave.ShopID, EventBarberLeaveCreated, leave.ID, map[string]any{
			"barber_id":         leave.BarberID,
			"barber_name":       leave.BarberName,
			"action":            leave.Action,
			"affected_count":    result.AffectedCount,
			"rescheduled_count": result.RescheduledCount,
			"cancelled_count":   result.CancelledCount,
			"start_at":          leave.StartAt,
			"end_at":            leave.EndAt,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 事务外：发微信通知（失败只 log）
	if sender != nil {
		for i := range appts {
			appt := &appts[i]
			text := buildLeaveNotification(appt, &leave, result.Action)
			if err := sender(ctx, appt.CustomerID, text); err != nil {
				// log 但不 return —— 部分失败不应影响整体结果
				fmt.Printf("[leave] 通知顾客 %s 失败: %v\n", appt.CustomerID, err)
				continue
			}
			result.NotifiedCustomers = append(result.NotifiedCustomers, appt.CustomerID)
		}
	}

	return result, nil
}

// CancelBarberLeave 撤销一条请假记录
//
// 仅当 now < leave.StartAt 时允许；已开始的不允许撤销。
// 撤销不影响已经被影响/改派的预约（那些已经按 leave 时点的策略处理完了）。
func CancelBarberLeave(ctx context.Context, leaveID, operator string) (*BarberLeave, error) {
	if DB == nil {
		return nil, fmt.Errorf("DB 未初始化")
	}
	var leave BarberLeave
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", leaveID).First(&leave).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("leave %s 不存在", leaveID)
			}
			return err
		}
		if leave.Status == LeaveStatusCancelled {
			return fmt.Errorf("leave %s 已是 cancelled 状态", leaveID)
		}
		now := time.Now()
		if !now.Before(leave.StartAt) {
			return ErrLeaveNotCancellable
		}
		updates := map[string]interface{}{
			"status":     LeaveStatusCancelled,
			"updated_at": now,
		}
		if err := tx.Model(&BarberLeave{}).
			Where("id = ? AND status = ?", leaveID, LeaveStatusActive).
			Updates(updates).Error; err != nil {
			return err
		}
		leave.Status = LeaveStatusCancelled
		leave.UpdatedAt = now

		TrackEventInTx(ctx, tx, leave.ShopID, EventBarberLeaveCancelled, leave.ID, map[string]any{
			"barber_id":   leave.BarberID,
			"barber_name": leave.BarberName,
			"start_at":    leave.StartAt,
			"end_at":      leave.EndAt,
			"operator":    operator,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &leave, nil
}

// ListBarberLeaves 列某理发师的请假历史（最新在前）
//
// limit=0 时取全部（用于后台查看完整历史）。
func ListBarberLeaves(ctx context.Context, barberID string, limit int) ([]BarberLeave, error) {
	if DB == nil {
		return nil, nil
	}
	q := DB.WithContext(ctx).Where("barber_id = ?", barberID).Order("start_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []BarberLeave
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListActiveLeaves 列出当前所有 active 的请假（用于 cron / dashboard）
//
// active 定义：status='active' AND now < end_at
func ListActiveLeaves(ctx context.Context, shopID string) ([]BarberLeave, error) {
	if DB == nil {
		return nil, nil
	}
	now := time.Now()
	q := DB.WithContext(ctx).
		Where("status = ? AND end_at > ?", LeaveStatusActive, now)
	if shopID != "" {
		q = q.Where("shop_id = ?", shopID)
	}
	var out []BarberLeave
	if err := q.Order("start_at ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// IsBarberOnLeaveAt 检查某理发师在指定时刻是否处于请假状态
//
// 用于 P4 集成：顾客创建预约前，先确认理发师没在请假，避免"预约成功→立即被取消"的体验事故。
// 区间定义：[start_at, end_at]，含两端点（end_at 当天最后一秒仍算在请假内）。
//
// 返回：
//   - (true, &leave, nil)  在请假中
//   - (false, nil, nil)    不在请假中（含未开始 / 已结束 / 已取消 / 已过期 几种情况）
//   - (false, nil, err)    DB 错误
//
// 实现说明：
//   - 用一条 SQL 同时校验 status='active' + start_at <= at <= end_at
//   - 若有多条匹配（极端情况：重叠请假），取最早结束的那条作为代表，调用方按文案展示即可
func IsBarberOnLeaveAt(ctx context.Context, barberID string, at time.Time) (bool, *BarberLeave, error) {
	if DB == nil {
		return false, nil, nil
	}
	var leave BarberLeave
	err := DB.WithContext(ctx).
		Where("barber_id = ? AND status = ? AND start_at <= ? AND end_at >= ?",
			barberID, LeaveStatusActive, at, at).
		Order("end_at ASC").
		First(&leave).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	return true, &leave, nil
}

// ListBarberLeavesInRange 列出某理发师在某时间区间内生效的 active 请假记录
//
// 用于 query_schedule / list_barbers 这类 UI 渲染场景，需要知道"该理发师未来 N 天
// 有没有请假、有几次、每次什么时间"。
//
// 返回结果按 start_at ASC；不区分过期 / 已撤销——只过滤 status='active' 且区间有交集。
func ListBarberLeavesInRange(ctx context.Context, barberID string, from, to time.Time) ([]BarberLeave, error) {
	if DB == nil {
		return nil, nil
	}
	var out []BarberLeave
	// 区间相交：leave.start_at <= to AND leave.end_at >= from
	err := DB.WithContext(ctx).
		Where("barber_id = ? AND status = ? AND start_at <= ? AND end_at >= ?",
			barberID, LeaveStatusActive, to, from).
		Order("start_at ASC").
		Find(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ============ 内部辅助 ============

// cancelApptInTx 在事务内取消预约（包一层避免事务嵌套）
//
// 注意：CancelAppointmentWithPolicy 自己起事务，这里**不能直接调用**。
// 所以展开核心逻辑：写 status/cancel_type/cancelled_at + event_log + （admin 路径不计 penalty）
func cancelApptInTx(tx *gorm.DB, apptID, source, reason string) (*CancelResult, error) {
	var appt Appointment
	if err := tx.Where("id = ?", apptID).First(&appt).Error; err != nil {
		return nil, err
	}
	if appt.Status != "active" {
		return nil, fmt.Errorf("appointment %s is in status %q, cannot cancel", apptID, appt.Status)
	}
	now := time.Now()
	res := tx.Model(&Appointment{}).
		Where("id = ? AND status = ?", apptID, "active").
		Updates(map[string]interface{}{
			"status":        "cancelled",
			"cancel_type":   "admin_cancel",
			"cancel_reason": reason,
			"cancelled_at":  now,
			"updated_at":    now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, fmt.Errorf("appointment %s status changed, retry", apptID)
	}
	rec := EventLog{
		ShopID:     appt.ShopID,
		CustomerID: appt.CustomerID,
		EventType:  EventAppointmentCancelled,
		RefID:      apptID,
		CreatedAt:  now,
		Meta: mustJSON(map[string]any{
			"cancel_type": "admin_cancel",
			"source":      source,
			"reason":      reason,
			"penalty_applied": false,
		}),
	}
	if err := tx.Create(&rec).Error; err != nil {
		return nil, err
	}
	return &CancelResult{
		AppointmentID: apptID,
		CancelType:    CancelTypeAdmin,
	}, nil
}

// findAlternateBarber 在事务内找一个"同档期 active 状态"的其他理发师
//
// 改派策略（MVP 简化版）：
//   - 取本店铺所有 active 理发师，排除原 barber
//   - 检查候选理发师在 (appt.Date, appt.Time) 是否已有 active 预约
//   - 取第一个可用
//
// 后续可优化：按技能匹配 / 按评分 / 按距离 ...
func findAlternateBarber(ctx context.Context, tx *gorm.DB, appt *Appointment) (string, bool, error) {
	var candidates []Barber
	if err := tx.Where("shop_id = ? AND active = ? AND id != ?", appt.ShopID, true, appt.BarberID).
		Order("name asc").
		Find(&candidates).Error; err != nil {
		return "", false, err
	}
	for _, c := range candidates {
		// 检查该候选理发师在 (appt.Date, appt.Time) 是否已被预约
		var conflictCount int64
		if err := tx.Model(&Appointment{}).
			Where("barber_id = ? AND date = ? AND time = ? AND status = ?",
				c.ID, appt.Date, appt.Time, "active").
			Count(&conflictCount).Error; err != nil {
			return "", false, err
		}
		if conflictCount == 0 {
			return c.ID, true, nil
		}
	}
	return "", false, nil
}

// getBarberName 通过 ID 拿理发师名（不拿 id 错误返回 ""；callers 用完自己兜底）
func getBarberName(tx *gorm.DB, barberID string) string {
	var b Barber
	if err := tx.Where("id = ?", barberID).First(&b).Error; err != nil {
		return ""
	}
	return b.Name
}

// TrackEventInTx 在事务内写事件埋点（参考 TrackEvent 但接受 tx）
func TrackEventInTx(ctx context.Context, tx *gorm.DB, shopID, eventType string, refID string, meta any) {
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
	if err := tx.Create(&rec).Error; err != nil {
		_ = err // 同 TrackEvent，埋点失败不阻塞业务
	}
}

// buildLeaveNotification 构造请假通知文案（取消 / 改派 两种）
func buildLeaveNotification(appt *Appointment, leave *BarberLeave, actionTaken string) string {
	switch actionTaken {
	case LeaveActionCancel:
		return fmt.Sprintf(
			"亲爱的 %s，抱歉地通知您：\n\n"+
				"您预约的 %s %s（%s师傅）因师傅临时有事（%s）被取消。\n"+
				"给您带来不便敬请谅解！请跟我说\"重新预约\"，我帮您换个时间。",
			appt.Customer, appt.Date, appt.Time, leave.BarberName, leave.Reason,
		)
	case LeaveActionReschedule:
		// 改派成功：取新 barber 名（在 notification 阶段 appt 已是新 barber）
		return fmt.Sprintf(
			"亲爱的 %s，温馨提示：\n\n"+
				"您预约的 %s %s 因 %s师傅临时有事（%s），已为您改派到 %s师傅，时段不变。\n"+
				"如需调整请跟我说\"改时间\"。",
			appt.Customer, appt.Date, appt.Time, leave.BarberName, leave.Reason, appt.BarberName,
		)
	default:
		return fmt.Sprintf("您 %s %s 的预约有变动，请联系商家确认。", appt.Date, appt.Time)
	}
}