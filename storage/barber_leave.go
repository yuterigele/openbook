package storage

// barber_leave.go
//
// P4 理发师请假（2026-06-21，v4.10 leave notify 改造 2026-06-23）
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
// v4.10 leave notify 通知链路：
//   - 持久化：所有 leave 通知落到 customer_notifications 表（type=leave_cancel/leave_reschedule/leave_no_contact）
//   - 并发：请假导致 N 个顾客要通知 → ParallelSender 5 worker 并发发
//   - 重试：每个 send task 包 SendWithRetry（3 次指数退避 200ms/400ms/800ms）
//   - 降级：ChannelSelector 按 external_userid → wechat_open_id → phone 选通道
//   - 文案：顾客名优先用 Customer.Name（反查），兜底用 Appointment.Customer，最后为空时省略"亲爱的 X"前缀
//   - 隐私：顾客面向文案走 CustomerFacingReason，兜底"师傅临时有事"，避免暴露"痔疮手术""陪老婆产检"等
//
// 单元隔离：
//   - 本文件只依赖 storage + uuid；wecom 通过 sender 注入
//   - 旧 NotificationSender 签名保留（向后兼容测试），新逻辑走 LeaveNotificationSender 新签名
//   - 旧签名路径：直接串行发 → 仅写 LeaveResult.NotifiedCustomers
//   - 新签名路径：写 customer_notifications 行 + 并发 → 回写 status + 收集 NotifiedCustomers/SkippedCustomers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// NotificationSender 旧版顾客通知发送器（仅保留兼容测试）
//
// 真实实现：wecom.Client.SendTextMessage
// 测试时：用一个把消息记录到 slice 的 fake
//
// 警告：生产代码请用 LeaveNotificationSender 新签名，能拿到 appt 上下文；
// 旧签名无法做持久化/重试/多店路由，仅适合老测试代码的 happy path。
type NotificationSender func(ctx context.Context, customerID, text string) error

// LeaveNotificationSender v4.10 新版顾客通知发送器
//
// 真实实现：wecom.Router → 按 shopID 找 client → ChannelSelector 选通道 → SendWithRetry
// 测试时：用 fakeSender 记录到 slice
//
// 收到 (ctx, appt, text) 后：
//   - 从 appt.CustomerID 反查 customer（拿 external_user_id / wechat_open_id / phone）
//   - ChannelSelector 按优先级选通道
//   - 按 appt.ShopID 找 router 对应 client + openKfID（多店路由）
//   - SendWithRetry 包 3 次指数退避
//   - customerID 空 / 无联系方式 → 返回 ErrNoCustomerContact 让 storage 写 skipped row
//
// 返回 error 时 storage 会写 failed row + 计数 attempt_count，便于排查 / 补发。
type LeaveNotificationSender func(ctx context.Context, appt *Appointment, text string) error

// LeaveResult 创建/撤销请假的结果
type LeaveResult struct {
	LeaveID           string
	Action            string
	AffectedCount     int      // 受影响的预约总数
	RescheduledCount  int      // 改派成功数
	CancelledCount    int      // 取消数（可能是 action=cancel 全部，也可能是 reschedule 兜底）
	NotifiedCustomers []string // 成功通知的 customer_id 列表（v4.10 新签名路径）
	SkippedCustomers  []string // 跳过的 customer_id 列表（v4.10：新签名路径中无联系方式的）
	FailedCustomers   []string // 失败的 customer_id 列表（v4.10：新签名路径中重试 3 次仍失败的）
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
//
// sender 参数同时兼容两种签名（v4.10 兼容老测试）：
//   - LeaveNotificationSender 新签名：推荐生产路径，完整持久化 + 并发 + 重试
//   - NotificationSender 旧签名：仅 happy path 串行发，不持久化（向后兼容老测试）
//
// 调用方识别方式：尝试类型断言，断言成功走新路径，否则走旧路径。
func CreateBarberLeave(ctx context.Context, leave BarberLeave, sender interface{}) (*LeaveResult, error) {
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
					newBarberName := getBarberName(tx, newBarberID)
					// 改派：更新 barber_id / barber_name + 写 reschedule 事件
					if err := tx.Model(&Appointment{}).
						Where("id = ? AND status = ?", appt.ID, "active").
						Updates(map[string]interface{}{
							"barber_id":   newBarberID,
							"barber_name": newBarberName,
							"updated_at":  now,
						}).Error; err != nil {
						return err
					}
					// v4.10 修复：同步更新内存 slice，事务外通知文案用得到
					// （之前只改 DB 不改 appts[i].BarberName，导致文案仍用旧名）
					appt.BarberID = newBarberID
					appt.BarberName = newBarberName
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

	// 事务外：发微信通知
	// v4.10：分发到新签名（持久化 + 并发 + 重试）或旧签名（向后兼容）
	if sender != nil {
		switch s := sender.(type) {
		case LeaveNotificationSender:
			// typed nil 函数：var x LeaveNotificationSender; x == nil 传进来判为非 nil 实际是 nil
			if s == nil {
				// leave row 已写完，不报错也不发（静默 no-op 兼容测试场景）
			} else {
				sendLeaveNotificationsV2(ctx, s, &leave, appts, result)
			}
		case NotificationSender:
			if s == nil {
				// typed nil 函数：同上
			} else {
				// 旧签名：串行发，无持久化（保留老测试 happy path）
				for i := range appts {
					appt := &appts[i]
					text := buildLeaveNotification(ctx, appt, &leave, result.Action)
					if err := s(ctx, appt.CustomerID, text); err != nil {
						fmt.Printf("[leave] 通知顾客 %s 失败: %v\n", appt.CustomerID, err)
						continue
					}
					result.NotifiedCustomers = append(result.NotifiedCustomers, appt.CustomerID)
				}
			}
		case func(context.Context, string, string) error:
			if s == nil {
				// typed nil 函数：同上
			} else {
				// 方法值 fallback：底层类型 func(ctx, string, string) error（老测试 f.send 用法）
				ns := NotificationSender(s)
				for i := range appts {
					appt := &appts[i]
					text := buildLeaveNotification(ctx, appt, &leave, result.Action)
					if err := ns(ctx, appt.CustomerID, text); err != nil {
						fmt.Printf("[leave] 通知顾客 %s 失败: %v\n", appt.CustomerID, err)
						continue
					}
					result.NotifiedCustomers = append(result.NotifiedCustomers, appt.CustomerID)
				}
			}
		case func(context.Context, *Appointment, string) error:
			// 方法值 fallback：底层类型 func(ctx, *Appointment, string) error（v4.10 新签名 f.send 用法）
			if s == nil {
				// typed nil 函数
			} else {
				ns := LeaveNotificationSender(s)
				sendLeaveNotificationsV2(ctx, ns, &leave, appts, result)
			}
		default:
			// 未知类型：和旧代码一样直接 return，不报错（避免误伤测试）
			_ = s
		}
	}

	return result, nil
}

// sendLeaveNotificationsV2 v4.10 新签名路径：持久化 + 串行 + 重试 + skipped
//
// 流程：
//   1) 对每个 appt 写一条 pending customer_notification row（事务外、立即可见）
//   2) 构造 text（buildLeaveNotification，内部反查 customer.Name + 隐私脱敏 reason）
//   3) 写 text_preview 到 row
//   4) 调 sender 闭包发送（sender 内部已含 ChannelSelector + SendWithRetry）
//   5) 成功 → MarkNotificationSent；失败 → MarkNotificationFailed；顾客无联系方式 → MarkNotificationSkipped
//   6) 收集结果到 LeaveResult.NotifiedCustomers / SkippedCustomers / FailedCustomers
//
// 注意事项：
//   - 这里用串行是因为 sender 闭包内已含 SendWithRetry（3 次退避），并发 N 倍会让总时长爆炸
//   - 如果业务量大（>50 顾客），可在 caller 层包 ParallelSender
//   - 不在事务内写 row：发送是慢 IO，事务内写会阻塞 leave row 提交；用事务外立即可见即可
func sendLeaveNotificationsV2(
	ctx context.Context,
	sender LeaveNotificationSender,
	leave *BarberLeave,
	appts []Appointment,
	result *LeaveResult,
) {
	for i := range appts {
		appt := &appts[i]

		// 通知类型：action=cancel → leave_cancel；action=reschedule → leave_reschedule
		// 但 customerID 为空 → leave_no_contact（merchant 需手动联系）
		notifType := NotifTypeLeaveReschedule
		if result.Action == LeaveActionCancel {
			notifType = NotifTypeLeaveCancel
		}
		if appt.CustomerID == "" {
			notifType = NotifTypeLeaveNoContact
		}

		// 构造文案（已含 customer.Name 反查 + CustomerFacingReason 兜底）
		text := buildLeaveNotification(ctx, appt, leave, result.Action)

		// 写 pending row + text_preview（事务外、立即可见）
		notif := &CustomerNotification{
			LeaveID:       leave.ID,
			AppointmentID: appt.ID,
			ShopID:        leave.ShopID,
			CustomerID:    appt.CustomerID,
			Type:          notifType,
			Channel:       NotifChannelPending,
			Status:        NotifStatusPending,
			TextPreview:   TruncatePreview(text, 256),
		}
		notifID, err := CreateCustomerNotification(ctx, notif)
		if err != nil {
			fmt.Printf("[leave] 写 notification row 失败 appt=%s: %v\n", appt.ID, err)
			// 不阻断，继续尝试发
		}

		// customerID 空：直接写 skipped row（不调 sender，避免 sender 自己再反查报错）
		if appt.CustomerID == "" {
			fmt.Printf("[leave] 预约 %s customerID 为空，跳过通知（需商户手动联系）\n", appt.ID)
			if notifID > 0 {
				_ = MarkNotificationSkipped(ctx, notifID, "appointment.customer_id 为空")
			}
			result.SkippedCustomers = append(result.SkippedCustomers, "")
			continue
		}

		// 调用 sender（sender 内部已含 ChannelSelector + SendWithRetry）
		err = sender(ctx, appt, text)
		if err == nil {
			if notifID > 0 {
				_ = MarkNotificationSent(ctx, notifID, 1)
			}
			result.NotifiedCustomers = append(result.NotifiedCustomers, appt.CustomerID)
			continue
		}

		// 区分 skipped vs failed
		if errors.Is(err, ErrNoCustomerContact) {
			fmt.Printf("[leave] 顾客 %s 无联系方式（需手动通知）: %v\n", appt.CustomerID, err)
			if notifID > 0 {
				_ = MarkNotificationSkipped(ctx, notifID, err.Error())
			}
			result.SkippedCustomers = append(result.SkippedCustomers, appt.CustomerID)
			continue
		}

		// 真失败
		fmt.Printf("[leave] 通知顾客 %s 失败: %v\n", appt.CustomerID, err)
		if notifID > 0 {
			_ = MarkNotificationFailed(ctx, notifID, 1, err.Error())
		}
		result.FailedCustomers = append(result.FailedCustomers, appt.CustomerID)
	}
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

// ExpireOverdueLeaves 把所有 end_at < now 的 active 请假标记为 expired（cron 兜底）
//
// 设计要点：
//   - 一次 UPDATE 完成（避免 SELECT + N 个 UPDATE 的来回）
//   - WHERE 既过滤 status=active 又过滤 end_at<now，原子性由 SQL 引擎保证
//   - 拿到 RowsAffected 后逐条写 barber_leave_expired 事件（用于后续分析）
//   - 顾客通知已在 CreateBarberLeave 时一次发完，expire 不再发微信
//
// 返回：本次被过期的 leave 数；DB 错误时返回 (0, err)
//
// 调用方：cron/leave.go 的 LeaveExpirer，每分钟一次
func ExpireOverdueLeaves(ctx context.Context, now time.Time) (int, error) {
	if DB == nil {
		return 0, nil
	}
	// 1) 找出所有"将过期"的 leave（用于写埋点）
	var toExpire []BarberLeave
	if err := DB.WithContext(ctx).
		Where("status = ? AND end_at < ?", LeaveStatusActive, now).
		Find(&toExpire).Error; err != nil {
		return 0, fmt.Errorf("查询待过期 leave 失败: %w", err)
	}
	if len(toExpire) == 0 {
		return 0, nil
	}

	// 2) 一次 UPDATE 把全部标 expired（带 status=active 守卫，防止和 cancel 抢）
	res := DB.WithContext(ctx).Model(&BarberLeave{}).
		Where("status = ? AND end_at < ?", LeaveStatusActive, now).
		Updates(map[string]interface{}{
			"status":     LeaveStatusExpired,
			"updated_at": now,
		})
	if res.Error != nil {
		return 0, fmt.Errorf("更新 leave 状态失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// 极端情况：刚被 cancel 抢了，正常返回 0
		return 0, nil
	}

	// 3) 写埋点（best-effort，失败只 log 不影响 return）
	for i := range toExpire {
		leave := &toExpire[i]
		TrackEvent(ctx, leave.ShopID, EventBarberLeaveExpired, leave.ID, map[string]any{
			"barber_id":   leave.BarberID,
			"barber_name": leave.BarberName,
			"start_at":    leave.StartAt,
			"end_at":      leave.EndAt,
			"reason":      leave.Reason,
			"expired_at":  now,
		})
	}
	return int(res.RowsAffected), nil
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
// 改派策略（PRD §11.7.11 v3.7 升级）：
//   1. 第一档（Skills 匹配）：候选理发师 Skills 包含 appt.Service 且时段空闲
//   2. 第二档（Skills 为空兜底）：候选理发师 Skills 为空（未填写）且时段空闲 —
//      把"未标记技能"和"标记了技能但不匹配"区分开，标记了的不能假装能染发
//   3. 兜底（不匹配 Skills）：以上都没有时回退到"任何 active 且时段空闲" —
//      保底可用性，避免出现"全部匹配不上就一个都改派不出去"
//
// 同档内按 name ASC 排序（稳定、可预测）。
// 匹配是包含关系：Skills="剪发,染发" 同时匹配 Service="染发" 和 "剪发"。
//
// 设计理由：
//   - 真实场景：顾客预约"染发"，Tony 请假了；Kevin 会剪发+染发，Bob 只剪发
//     → 应该优先 Kevin（真会染发），而非 Bob（不会）
//   - Bob 虽然不染发，但作为"什么都能做"的兜底人选是合理的
func findAlternateBarber(ctx context.Context, tx *gorm.DB, appt *Appointment) (string, bool, error) {
	// 拿候选：取本店所有 active + 排除原 barber + 按 name ASC
	var candidates []Barber
	if err := tx.Where("shop_id = ? AND active = ? AND id != ?", appt.ShopID, true, appt.BarberID).
		Order("name asc").
		Find(&candidates).Error; err != nil {
		return "", false, err
	}

	// 时段空闲检查 helper
	isSlotFree := func(barberID string) (bool, error) {
		var n int64
		if err := tx.Model(&Appointment{}).
			Where("barber_id = ? AND date = ? AND time = ? AND status = ?",
				barberID, appt.Date, appt.Time, "active").
			Count(&n).Error; err != nil {
			return false, err
		}
		return n == 0, nil
	}

	// 第一档：Skills 包含 appt.Service（真会这门手艺）
	for _, c := range candidates {
		if c.Skills == "" || !skillContains(c.Skills, appt.Service) {
			continue
		}
		free, err := isSlotFree(c.ID)
		if err != nil {
			return "", false, err
		}
		if free {
			return c.ID, true, nil
		}
	}

	// 第二档：Skills 为空（未填写）的理发师 —— 视作"全能"
	for _, c := range candidates {
		if c.Skills != "" {
			continue
		}
		free, err := isSlotFree(c.ID)
		if err != nil {
			return "", false, err
		}
		if free {
			return c.ID, true, nil
		}
	}

	// 兜底：忽略 Skills 匹配，取任何 active 且时段空闲（保底可用性）
	for _, c := range candidates {
		free, err := isSlotFree(c.ID)
		if err != nil {
			return "", false, err
		}
		if free {
			return c.ID, true, nil
		}
	}

	return "", false, nil
}

// skillContains 检查逗号分隔的 skills 字符串是否包含 needle（精确匹配单项）
//
//  - 匹配是精确匹配单项：Skills="剪发,染发" 包含 "染发" 和 "剪发"，但不含 "染"
//  - 自动 TrimSpace 容忍 "剪发, 染发" 这种带空格的写法
//  - needle 为空时返回 false（避免空匹配全 true）
func skillContains(skills, needle string) bool {
	if needle == "" {
		return false
	}
	for _, s := range strings.Split(skills, ",") {
		if strings.TrimSpace(s) == needle {
			return true
		}
	}
	return false
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
//
// v4.13.0 简化：
//   - 顾客姓名：优先用 customer.Name（反查，避免 appt.Customer 冗余字段为空时显示"亲爱的 ，..."）
//     → fallback 用 appt.Customer
//     → 都为空时省略"亲爱的 X"前缀，直接开始正文（避免半句话尴尬）
//   - 请假原因：**永远 hardcode "师傅临时有事"**，绝不返 leave.Reason（内部原因）
//     之前有过 resolveCustomerFacingReason + 白名单机制（"病假/家中有事"返，其他兜底），
//     但白名单外的"陪老婆产检"等仍可能走白名单误判，统一硬编码更安全
func buildLeaveNotification(ctx context.Context, appt *Appointment, leave *BarberLeave, actionTaken string) string {
	name := resolveCustomerName(ctx, appt)
	reason := "师傅临时有事" // v4.13.0 隐私：永不暴露内部 Reason

	switch actionTaken {
	case LeaveActionCancel:
		if name == "" {
			return fmt.Sprintf(
				"抱歉地通知您：\n\n"+
					"您预约的 %s %s（%s师傅）因%s被取消。\n"+
					"给您带来不便敬请谅解！请跟我说\"重新预约\"，我帮您换个时间。",
				appt.Date, appt.Time, leave.BarberName, reason,
			)
		}
		return fmt.Sprintf(
			"亲爱的 %s，抱歉地通知您：\n\n"+
				"您预约的 %s %s（%s师傅）因%s被取消。\n"+
				"给您带来不便敬请谅解！请跟我说\"重新预约\"，我帮您换个时间。",
			name, appt.Date, appt.Time, leave.BarberName, reason,
		)
	case LeaveActionReschedule:
		// 改派成功：取新 barber 名（在 notification 阶段 appt 已是新 barber）
		if name == "" {
			return fmt.Sprintf(
				"温馨提示：\n\n"+
					"您预约的 %s %s 因 %s师傅%s，已为您改派到 %s师傅，时段不变。\n"+
					"如需调整请跟我说\"改时间\"。",
				appt.Date, appt.Time, leave.BarberName, reason, appt.BarberName,
			)
		}
		return fmt.Sprintf(
			"亲爱的 %s，温馨提示：\n\n"+
				"您预约的 %s %s 因 %s师傅%s，已为您改派到 %s师傅，时段不变。\n"+
				"如需调整请跟我说\"改时间\"。",
			name, appt.Date, appt.Time, leave.BarberName, reason, appt.BarberName,
		)
	default:
		return fmt.Sprintf("您 %s %s 的预约有变动，请联系商家确认。", appt.Date, appt.Time)
	}
}

// resolveCustomerName 反查 customer.Name（v4.10 P0-2 修复）
//
// 优先级：customer.Name → appt.Customer → ""
//
// 失败原因：
//   - appt.CustomerID 空：直接 fallback（不要 log，appt 可能本就没绑 customer）
//   - DB 查不到：log warn 后 fallback（不阻断通知）
//   - DB 错误：log warn 后 fallback（不阻断通知）
//
// 注意：此函数在 leave 通知路径中调用，单条失败不能影响整批。
func resolveCustomerName(ctx context.Context, appt *Appointment) string {
	if appt.CustomerID == "" {
		return appt.Customer
	}
	if DB == nil {
		return appt.Customer
	}
	var cust Customer
	if err := DB.WithContext(ctx).Select("name").Where("id = ?", appt.CustomerID).First(&cust).Error; err != nil {
		// log 但不 return error —— 单条顾客查不到不应影响整批通知
		fmt.Printf("[leave] 反查顾客 %s 姓名失败: %v\n", appt.CustomerID, err)
		return appt.Customer
	}
	if cust.Name == "" {
		return appt.Customer
	}
	return cust.Name
}

// v4.13.0 删除：resolveCustomerFacingReason + publicReasonWhitelist
//   - 之前有过 "Reason 白名单 → 原样展示" 机制（"病假"等公开短语返，其他兜底）
//   - 实际问题：白名单不全（"陪老婆产检"白名单外但仍可能误判其他情况）
//   - 简化方案：buildLeaveNotification 直接 hardcode "师傅临时有事"
//   - 删除理由：1 个函数 + 1 个白名单 = 减少 30+ 行代码 + 4 个测试 + 1 个 DB 列
//   - 等价安全：所有 reason（"病假""陪老婆产检"等）统一走兜底，对顾客端体验**一致**