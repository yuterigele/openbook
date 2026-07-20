package storage

// cancel_policy.go
//
// P3 取消/爽约策略联动（2026-06-21）
//
// 背景：
//   - PRD §11.4 提到 Cancellation/Refund；PRD 没硬要求具体规则
//   - P3 自定义：在工具/接口层加一套"提前多久免爽约标记"的策略
//
// 策略核心：
//   - 顾客取消时间 vs 预约时间，决定 cancel_type：
//     * 提前 ≥ free_window（默认 2h）：early_cancel，无 penalty
//     * 提前 < free_window：late_cancel，+1 Customer.LateCancelCount
//     * 已经过了预约时间：after_due，+1 Customer.NoShowCount，并拒绝取消
//     * 商户在后台操作：admin_cancel，无 penalty
//     * 系统自动（如 noshow scanner 触发）：system_cancel，无 penalty
//
// 黑名单阈值（可在 .env 覆盖）：
//   - LateCancelBlacklistThreshold：累计晚退订 ≥ N 自动加 BLACKLIST
//   - NoShowBlacklistThreshold：累计爽约 ≥ N 自动加 BLACKLIST
//
// 调用方：
//   - tools.CancelAppointmentTool   source = "agent"
//   - api.adminCancelHandler        source = "admin"
//   - cron.noshow.markNoShow        自身走 noshow 路径，不经 CancelAppointment
//
// 兼容性：
//   - 保留原 CancelAppointment(id string) error（包装新版，source = "agent"）

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// CancelSource 取消来源（用于判断是否豁免 penalty）
const (
	CancelSourceAgent  = "agent"  // C 端顾客 Agent 调用
	CancelSourceAdmin  = "admin"  // 商户在后台手动取消
	CancelSourceSystem = "system" // 系统触发（如 noshow scanner 标记后的清理）
)

// CancelType 取消类型（持久化到 Appointment.CancelType）
const (
	CancelTypeEmpty    = ""              // 未取消（默认值）
	CancelTypeEarly    = "early_cancel"  // 提前 ≥ free_window 取消，无 penalty
	CancelTypeLate     = "late_cancel"   // 提前 < free_window 取消，+late_cancel_count
	CancelTypeAfterDue = "after_due"     // 已过预约时间（拒绝取消，标记爽约）
	CancelTypeAdmin    = "admin_cancel"  // 商户操作，无 penalty
	CancelTypeSystem   = "system_cancel" // 系统触发，无 penalty
)

// ErrAfterDueCancel 顾客试图取消"已过预约时间"的预约
//
// 业务语义：此时已不是"取消"而是"未到店"，应该走 noshow 流程。
// 工具层捕获后引导用户改约 / 标记爽约。
var ErrAfterDueCancel = errors.New("appointment time already passed, cannot cancel; please mark as no-show or reschedule")

// CancelPolicy 取消策略（可在 .env / DB 覆盖；MVP 默认硬编码）
type CancelPolicy struct {
	// FreeWindow 提前多久取消算"免费"（>= 这个时间算 early_cancel）
	FreeWindow time.Duration

	// LateCancelBlacklistThreshold 累计 late_cancel ≥ 这个数 → 自动加 BLACKLIST
	LateCancelBlacklistThreshold int

	// NoShowBlacklistThreshold 累计 no_show ≥ 这个数 → 自动加 BLACKLIST
	NoShowBlacklistThreshold int
}

// DefaultCancelPolicy 默认策略
//
// 数值依据：
//   - FreeWindow = 2h：理发店常规 2h 前可退，参考美团/大众点评经验值
//   - LateCancelThreshold = 3：累计 3 次晚退订 → 黑名单（容忍偶尔临时有事）
//   - NoShowThreshold = 2：爽约 2 次直接黑名单（比晚退订更严）
var DefaultCancelPolicy = CancelPolicy{
	FreeWindow:                   2 * time.Hour,
	LateCancelBlacklistThreshold: 3,
	NoShowBlacklistThreshold:     2,
}

// CurrentCancelPolicy 取当前生效策略（MVP 写死 DefaultCancelPolicy）
//
// 后续可改成：从 Shop 表读取 / 从 .env 覆盖 / 从 DB 拉取。
func CurrentCancelPolicy() CancelPolicy {
	return DefaultCancelPolicy
}

// CancelResult 取消操作的结果（包含策略副作用，便于调用方提示用户）
type CancelResult struct {
	AppointmentID   string
	CancelType      string // early/late/admin/system/after_due（after_due 通常伴随 ErrAfterDueCancel 返回）
	PenaltyApplied  bool   // 是否触发 +1 penalty 计数
	Blacklisted     bool   // 本次操作是否触发 BLACKLIST 加标签
	BlacklistReason string // 触发原因，便于日志/事件埋点
	Warning         string // 提示语（Agent 调用时可附加到回复："这次晚了，下次请提前 2h 取消"）
}

// CancelAppointmentWithPolicy 带策略的取消预约（PRD §11.4 P3）
//
// 参数：
//   - ctx         上下文
//   - apptID      预约 ID
//   - source      取消来源（agent / admin / system）
//   - reason      取消原因（可选，admin 取消时商户填）
//
// 返回：
//   - 成功：CancelResult（包含策略副作用），nil
//   - 已过预约时间且非 admin/system：返回 ErrAfterDueCancel + CancelResult{Type: after_due}
//     此时 appt.Status 保持 active，等待 noshow scanner 自动标记
//   - 其他错误：appt 状态不变
//
// 事务边界（与 MarkAppointmentCompleted 一致）：
//   - 状态检查 + 更新带 WHERE status='active' 兜底
//   - 顾客字段更新在同一事务
//   - 埋点 TrackEvent 在事务内（参与回滚）
func CancelAppointmentWithPolicy(ctx context.Context, apptID, source, reason string) (*CancelResult, error) {
	return cancelAppointmentWithPolicy(ctx, apptID, source, reason, "", "")
}

// CancelAppointmentForCustomerWithPolicy cancels an appointment only when it
// belongs to the verified customer in the current shop.
func CancelAppointmentForCustomerWithPolicy(ctx context.Context, apptID, shopID, customerID, source, reason string) (*CancelResult, error) {
	if shopID == "" || customerID == "" {
		return nil, ErrCustomerIdentityRequired
	}
	return cancelAppointmentWithPolicy(ctx, apptID, source, reason, shopID, customerID)
}

func cancelAppointmentWithPolicy(ctx context.Context, apptID, source, reason, shopID, customerID string) (*CancelResult, error) {
	if apptID == "" {
		return nil, fmt.Errorf("apptID 不能为空")
	}
	if source == "" {
		source = CancelSourceAgent
	}
	policy := CurrentCancelPolicy()

	res := &CancelResult{
		AppointmentID: apptID,
	}

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)

	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var appt Appointment
		lookup := tx.Where("id = ?", apptID)
		if shopID != "" {
			lookup = lookup.Where("shop_id = ? AND customer_id = ?", shopID, customerID)
		}
		if err := lookup.First(&appt).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if shopID != "" {
					return ErrAppointmentForbidden
				}
				return ErrAppointmentNotFound
			}
			return err
		}
		if appt.Status == "cancelled" {
			return ErrAlreadyCancelled
		}
		if appt.Status != "active" {
			return fmt.Errorf("appointment %s is in status %q, cannot cancel", apptID, appt.Status)
		}

		// 解析预约时间（date + time）
		apptTime, parseErr := time.ParseInLocation("2006-01-02 15:04", appt.Date+" "+appt.Time, loc)
		if parseErr != nil {
			return fmt.Errorf("解析预约时间失败 date=%s time=%s: %w", appt.Date, appt.Time, parseErr)
		}

		// 计算 cancel_type
		var cancelType string
		switch source {
		case CancelSourceAdmin:
			cancelType = CancelTypeAdmin
		case CancelSourceSystem:
			cancelType = CancelTypeSystem
		case CancelSourceAgent:
			// 顾客主动取消：根据时机判定
			if !now.Before(apptTime) {
				// 已经过了预约时间 → 不允许"取消"，让 noshow scanner 接管
				cancelType = CancelTypeAfterDue
				res.CancelType = cancelType
				// 注意：这里不写 DB，因为我们要保留 status=active
				return ErrAfterDueCancel
			}
			hoursAhead := apptTime.Sub(now)
			if hoursAhead >= policy.FreeWindow {
				cancelType = CancelTypeEarly
			} else {
				cancelType = CancelTypeLate
			}
		default:
			return fmt.Errorf("unknown cancel source: %s", source)
		}
		res.CancelType = cancelType

		// 状态更新（带 WHERE status='active' 兜底）
		now2 := time.Now()
		updates := map[string]interface{}{
			"status":          "cancelled",
			"active_slot_key": nil,
			"cancel_type":     cancelType,
			"cancelled_at":    now2,
			"updated_at":      now2,
		}
		if reason != "" {
			updates["cancel_reason"] = reason
		}
		update := tx.Model(&Appointment{}).Where("id = ? AND status = ?", apptID, "active")
		if shopID != "" {
			update = update.Where("shop_id = ? AND customer_id = ?", shopID, customerID)
		}
		txRes := update.
			Updates(updates)
		if txRes.Error != nil {
			return txRes.Error
		}
		if txRes.RowsAffected == 0 {
			return fmt.Errorf("appointment %s status changed, retry", apptID)
		}

		// 副作用：penalty 计数 + 黑名单触发检查
		if cancelType == CancelTypeLate && appt.CustomerID != "" {
			res.PenaltyApplied = true
			res.Warning = fmt.Sprintf("本次取消距离预约时间不足 %s，已记录为'晚退订'，请下次提前取消避免影响预约权益", formatDuration(policy.FreeWindow))
			blacklisted, blReason, err := applyLateCancelPenalty(tx, appt.CustomerID, policy)
			if err != nil {
				return err
			}
			if blacklisted {
				res.Blacklisted = true
				res.BlacklistReason = blReason
			}
		}

		// 埋点（事务内，写 event_logs 也参与回滚）
		rec := EventLog{
			ShopID:     appt.ShopID,
			CustomerID: appt.CustomerID,
			EventType:  EventAppointmentCancelled,
			RefID:      apptID,
			CreatedAt:  now2,
			Meta: mustJSON(map[string]any{
				"cancel_type":     cancelType,
				"source":          source,
				"reason":          reason,
				"hours_ahead":     apptTime.Sub(now).Hours(),
				"penalty_applied": res.PenaltyApplied,
				"blacklisted":     res.Blacklisted,
			}),
		}
		if err := tx.Create(&rec).Error; err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return res, err
	}
	return res, nil
}

// applyLateCancelPenalty 增加 late_cancel_count，并检查黑名单阈值
//
// 在调用方的 transaction 内运行，所有副作用参与回滚。
//
// 返回 (blacklisted, reason, err)：
//   - blacklisted: 本次操作是否触发 BLACKLIST 加标签
//   - reason: 触发原因（"late_cancel_threshold" 等）
//   - err: DB 错误
func applyLateCancelPenalty(tx *gorm.DB, customerID string, policy CancelPolicy) (bool, string, error) {
	if customerID == "" {
		return false, "", nil
	}
	now := time.Now()
	// +1 late_cancel_count
	if err := tx.Model(&Customer{}).
		Where("id = ?", customerID).
		Updates(map[string]interface{}{
			"late_cancel_count": gorm.Expr("late_cancel_count + 1"),
			"updated_at":        now,
		}).Error; err != nil {
		return false, "", err
	}

	// 检查是否触达黑名单阈值（晚退订 ≥ N → BLACKLIST）
	var cust Customer
	if err := tx.Where("id = ?", customerID).First(&cust).Error; err != nil {
		return false, "", err
	}
	ts := NewTagSet(cust.Tags)
	if ts.Has(TagBlacklist) {
		return false, "", nil // 已经是黑名单，无需重复加
	}
	threshold := policy.LateCancelBlacklistThreshold
	if cust.LateCancelCount+1 >= threshold {
		ts.Add(TagBlacklist)
		if err := tx.Model(&cust).Updates(map[string]interface{}{
			"tags":       ts.String(),
			"updated_at": now,
		}).Error; err != nil {
			return false, "", err
		}
		// 黑名单事件埋点
		rec := EventLog{
			CustomerID: customerID,
			EventType:  EventBlacklisted,
			RefID:      customerID,
			CreatedAt:  now,
			Meta: mustJSON(map[string]any{
				"reason":            "late_cancel_threshold",
				"late_cancel_count": cust.LateCancelCount + 1,
				"threshold":         threshold,
			}),
		}
		if err := tx.Create(&rec).Error; err != nil {
			return false, "", err
		}
		return true, "late_cancel_threshold", nil
	}
	return false, "", nil
}

// formatDuration 把 duration 格式化为"X 小时" / "Y 分钟"
func formatDuration(d time.Duration) string {
	if d >= time.Hour {
		hours := int(d / time.Hour)
		return fmt.Sprintf("%d 小时", hours)
	}
	minutes := int(d / time.Minute)
	return fmt.Sprintf("%d 分钟", minutes)
}

// mustJSON 序列化失败时返回空字符串（埋点不阻塞业务）
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
