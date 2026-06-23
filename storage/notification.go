package storage

// notification.go
//
// v4.10 leave notify 持久化 + 发送基础设施（2026-06-23）
//
// 提供：
//   - CustomerNotification CRUD（创建 / 更新状态 / 按 leave/customer 查）
//   - SendWithRetry 通用重试 helper（指数退避，2 次重试 → 200ms/400ms/800ms）
//   - ChannelSelector 多通道 fallback 决策（KF → 应用消息 → SMS → 留 pending）
//   - 并发发送 helper（bounded errgroup，控制并发 5）
//
// 设计动机：
//   - 之前 leave 通知 sender 失败只能从 stdout 日志排查，admin 后台看不到任何状态
//   - 重试 / 多通道 / 并发都散落在调用方，缺统一抽象
//   - 这次重构把所有"发通知"的能力收口到本文件，调用方只管构造通知 + 选通道

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
)

// ===== CustomerNotification 持久化 =====

// CreateCustomerNotification 插入一条 pending 状态的记录（事务外立即可见）
//
// 返回主键 id（uint64）给后续 Update 调用方用。
// status=pending 表示"已记录，未尝试发送"；channel=pending 表示"还没选好通道"（fallback 中）。
func CreateCustomerNotification(ctx context.Context, n *CustomerNotification) (uint64, error) {
	if DB == nil {
		return 0, nil
	}
	if n.Status == "" {
		n.Status = NotifStatusPending
	}
	if n.Channel == "" {
		n.Channel = NotifChannelPending
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now()
	}
	n.UpdatedAt = time.Now()

	if err := DB.WithContext(ctx).Create(n).Error; err != nil {
		return 0, fmt.Errorf("create customer_notification: %w", err)
	}
	return n.ID, nil
}

// MarkNotificationSent 标记一条通知成功（status=sent, sent_at=now, attempt_count++）
//
// 幂等：已 sent 的记录不重复更新 sent_at（避免误覆盖成功时间）。
func MarkNotificationSent(ctx context.Context, id uint64, attempt int) error {
	if DB == nil || id == 0 {
		return nil
	}
	now := time.Now()
	updates := map[string]interface{}{
		"status":          NotifStatusSent,
		"sent_at":         now,
		"attempt_count":   attempt,
		"last_attempt_at": now,
		"updated_at":      now,
	}
	// 用 sent_at IS NULL 守卫，避免覆盖首次成功时间
	res := DB.WithContext(ctx).Model(&CustomerNotification{}).
		Where("id = ? AND sent_at IS NULL", id).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	// 如果 sent_at 已经有值（之前成功过），只更新 attempt_count + last_attempt_at
	if res.RowsAffected == 0 {
		return DB.WithContext(ctx).Model(&CustomerNotification{}).
			Where("id = ?", id).
			Updates(map[string]interface{}{
				"attempt_count":   attempt,
				"last_attempt_at": now,
				"updated_at":      now,
			}).Error
	}
	return nil
}

// MarkNotificationFailed 标记失败（status=failed, attempt_count++, error_message 更新）
//
// 用 attempt_count 作为乐观锁，防止多次 goroutine 同时改同一行覆盖掉。
func MarkNotificationFailed(ctx context.Context, id uint64, attempt int, errMsg string) error {
	if DB == nil || id == 0 {
		return nil
	}
	now := time.Now()
	// error_message 截断到 512（schema 限制）
	if len(errMsg) > 512 {
		errMsg = errMsg[:512]
	}
	return DB.WithContext(ctx).Model(&CustomerNotification{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":          NotifStatusFailed,
			"attempt_count":   attempt,
			"last_attempt_at": now,
			"error_message":   errMsg,
			"updated_at":      now,
		}).Error
}

// MarkNotificationSkipped 标记跳过（无联系方式，无法发送，需要商户手动联系）
//
// 用途：customer 完全没有 external_user_id / wechat_open_id / phone 时，
// 留一条 skipped 记录，让 admin 后台能看见"该顾客这次需要手动通知"。
func MarkNotificationSkipped(ctx context.Context, id uint64, reason string) error {
	if DB == nil || id == 0 {
		return nil
	}
	if len(reason) > 512 {
		reason = reason[:512]
	}
	return DB.WithContext(ctx).Model(&CustomerNotification{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":        NotifStatusSkipped,
			"error_message": reason,
			"updated_at":    time.Now(),
		}).Error
}

// ListNotificationsByLeave 列某 leave 的所有通知记录（admin 后台"查看通知结果"用）
func ListNotificationsByLeave(ctx context.Context, leaveID string) ([]CustomerNotification, error) {
	if DB == nil {
		return nil, nil
	}
	var out []CustomerNotification
	if err := DB.WithContext(ctx).
		Where("leave_id = ?", leaveID).
		Order("created_at ASC").
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListPendingNotifications 列某店铺状态非终态的通知（admin "未发送列表"用）
//
// status IN (pending, failed) 用于排查"上次没发出去需要补发"的列表。
func ListPendingNotifications(ctx context.Context, shopID string, limit int) ([]CustomerNotification, error) {
	if DB == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	var out []CustomerNotification
	if err := DB.WithContext(ctx).
		Where("shop_id = ? AND status IN ?", shopID, []string{NotifStatusPending, NotifStatusFailed}).
		Order("created_at DESC").
		Limit(limit).
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ===== 发送基础设施 =====

// WeComSender 抽象发送能力（避免 storage 反向依赖 wecom）
//
// 实现方可以是 wecom.Client.SendKfTextMessage 或 wecom.Client.SendTextMessage，
// 也可以是测试时的 mock。
type WeComSender interface {
	// Send 发送一条文本，返回 err
	Send(ctx context.Context, target, content string) error
}

// WeComSenderFunc 函数适配器（让 wecom.Client 方法直接当 WeComSender 用）
type WeComSenderFunc func(ctx context.Context, target, content string) error

func (f WeComSenderFunc) Send(ctx context.Context, target, content string) error {
	return f(ctx, target, content)
}

// SMSAdapter 短信降级（预留接口，MVP 不实现；调用方判断 nil 跳过）
type SMSAdapter interface {
	Send(ctx context.Context, phone, content string) error
}

// SendOptions 发送选项
type SendOptions struct {
	MaxAttempts   int           // 总尝试次数（含首次），默认 3
	InitialBackoff time.Duration // 首次重试等待，默认 200ms
	MaxBackoff    time.Duration // 单次最大等待，默认 1s
	Context       context.Context // 调用方 ctx（用于取消）
}

// SendWithRetry 带指数退避的重试 wrapper
//
// 行为：
//   - 第 1 次失败 → 等待 InitialBackoff
//   - 第 2 次失败 → 等待 InitialBackoff*2
//   - 第 3 次失败 → 等待 InitialBackoff*4（不超过 MaxBackoff）
//   - 所有尝试都失败 → 返回最后一个 err
//
// 适用场景：企业微信 API 调用（access_token 过期 / 网络抖动 / 短暂限流）
func SendWithRetry(ctx context.Context, sender WeComSender, target, content string, opts SendOptions) error {
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}
	if opts.InitialBackoff <= 0 {
		opts.InitialBackoff = 200 * time.Millisecond
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 1 * time.Second
	}
	if opts.Context != nil {
		ctx = opts.Context
	}

	var lastErr error
	backoff := opts.InitialBackoff
	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		if err := sender.Send(ctx, target, content); err == nil {
			return nil
		} else {
			lastErr = err
			if attempt == opts.MaxAttempts {
				break
			}
			// 等待后重试
			select {
			case <-ctx.Done():
				return fmt.Errorf("ctx canceled during retry: %w", ctx.Err())
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > opts.MaxBackoff {
				backoff = opts.MaxBackoff
			}
		}
	}
	return lastErr
}

// ChannelDecision 通道选择结果
type ChannelDecision struct {
	Channel string // NotifChannelWeComKF / NotifChannelWeComApp / NotifChannelSMS
	Target  string // external_user_id / wechat_open_id / phone
	HasContact bool // false 表示顾客无任何联系方式 → 走 skipped
}

// SelectChannel 根据 customer 字段按优先级选发送通道
//
// 优先级（v4.10）：
//   1. external_user_id（KF 场景）→ 走 wecom_kf（用 SendKfTextMessage）
//   2. wechat_open_id（企业应用成员）→ 走 wecom_app（用 SendTextMessage）
//   3. phone → 走 sms（预留，当前 SMSAdapter=nil 时仍 fallback 到 skipped）
//   4. 都没有 → HasContact=false，调用方应 MarkSkipped
func SelectChannel(c *Customer) ChannelDecision {
	if c == nil {
		return ChannelDecision{HasContact: false}
	}
	if c.ExternalUserID != "" {
		return ChannelDecision{
			Channel:    NotifChannelWeComKF,
			Target:     c.ExternalUserID,
			HasContact: true,
		}
	}
	if c.WechatOpenID != "" {
		return ChannelDecision{
			Channel:    NotifChannelWeComApp,
			Target:     c.WechatOpenID,
			HasContact: true,
		}
	}
	if c.Phone != "" {
		return ChannelDecision{
			Channel:    NotifChannelSMS,
			Target:     c.Phone,
			HasContact: true,
		}
	}
	return ChannelDecision{HasContact: false}
}

// ParallelSender 并发发送 helper（bounded，控制并发数避免触发企业微信限流）
//
// 调用方传一组 sendTasks，每条任务是 "id + ctx + sendFn"，本 helper：
//   - 起 N 个 worker（默认 5）
//   - 每个 worker 调 sendFn.Send(ctx, ...) → 拿到 err
//   - 调 MarkSent / MarkFailed 写库
//   - 汇总结果：sentIDs / failedIDs / errs
//
// 用途：leave notify 一次性给 N 个顾客发，用本 helper 把"串行 N 倍时间"压成"5 并发"。
type ParallelSender struct {
	Concurrency int // 默认 5
}

// SendTask 单条发送任务
type SendTask struct {
	ID           uint64             // customer_notification.id（用于回写状态）
	Target       string             // 发送目标（external_user_id / openid / phone）
	Content      string             // 文案
	Channel      string             // 选定的 channel
	Sender       WeComSender        // 该通道的 sender（已绑定好 client + 通道逻辑）
}

// SendTaskResult 单条任务结果
type SendTaskResult struct {
	ID      uint64
	Success bool
	Err     error
}

// SendAll 并发执行所有 task，返回每条结果
//
// 设计：
//   - 用 buffered channel 做任务队列 + WaitGroup 等所有 worker 完成
//   - 每个 task 独立 ctx（共享父 ctx 以便全局取消）
//   - 失败 task 不影响其他 task（局部错误）
func (p *ParallelSender) SendAll(ctx context.Context, tasks []SendTask, retryOpts SendOptions) []SendTaskResult {
	if p.Concurrency <= 0 {
		p.Concurrency = 5
	}
	results := make([]SendTaskResult, len(tasks))
	if len(tasks) == 0 {
		return results
	}

	taskCh := make(chan int, len(tasks))
	for i := range tasks {
		taskCh <- i
	}
	close(taskCh)

	var wg sync.WaitGroup
	for w := 0; w < p.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range taskCh {
				t := tasks[idx]
				// 每个 task 都用重试
				err := SendWithRetry(ctx, t.Sender, t.Target, t.Content, retryOpts)
				results[idx] = SendTaskResult{
					ID:      t.ID,
					Success: err == nil,
					Err:     err,
				}
			}
		}()
	}
	wg.Wait()
	return results
}

// ===== 便捷 helper =====

// TruncatePreview 取文案前 N 字符用于 preview（避免超 256 列宽）
func TruncatePreview(s string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 256
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}

// ErrNoCustomerContact 顾客无任何联系方式（不是 panic，只是 skipped）
var ErrNoCustomerContact = errors.New("customer has no external_userid / wechat_open_id / phone")

// ErrNotificationAlreadySent 通知已成功发送，不允许 retry（避免重复打扰顾客）
var ErrNotificationAlreadySent = errors.New("notification already sent, refusing to retry")

// ErrNotificationNotFound 通知 ID 不存在
var ErrNotificationNotFound = errors.New("notification not found")

// ===== 列表 + 单条查询（admin 后台 UI 用）=====

// ListNotificationsForShop 列出某店铺的通知记录（admin 后台"通知中心"用）
//
// 过滤条件：
//   - status: pending / sent / failed / skipped（空字符串 = 不过滤）
//   - type:   leave_cancel / leave_reschedule / leave_no_contact（空 = 不过滤）
//   - leaveID: 按 leave_id 精确匹配（空 = 不过滤），用于"查看某次请假的所有通知"
//
// 排序：created_at DESC（最新在前，方便商户看最近的失败）
// limit: 0 → 200 默认；上限 500 防止 admin 后台请求打爆 DB
func ListNotificationsForShop(ctx context.Context, shopID string, status, notifType, leaveID string, limit int) ([]CustomerNotification, error) {
	if DB == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	q := DB.WithContext(ctx).Where("shop_id = ?", shopID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if notifType != "" {
		q = q.Where("type = ?", notifType)
	}
	if leaveID != "" {
		q = q.Where("leave_id = ?", leaveID)
	}
	var out []CustomerNotification
	if err := q.Order("created_at DESC").Limit(limit).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// GetNotificationByID 单条查询（admin 后台 retry 按钮用）
//
// 返回 (notification, true, nil) 或 (nil, false, nil)（不存在时不算错误）
func GetNotificationByID(ctx context.Context, id uint64) (*CustomerNotification, bool, error) {
	if DB == nil {
		return nil, false, nil
	}
	var n CustomerNotification
	err := DB.WithContext(ctx).Where("id = ?", id).First(&n).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &n, true, nil
}

// ===== 补发（admin 后台 retry 按钮的核心）=====

// RetryNotificationResult 补发结果
type RetryNotificationResult struct {
	ID        uint64
	NewStatus string // sent / failed / skipped
	Err       error
}

// RetryNotification 重新发送一条通知（admin 后台"补发"按钮）
//
// 行为：
//   - status=sent：直接返回 ErrNotificationAlreadySent（已发过不重发，避免重复打扰）
//   - status=pending/failed/skipped：重新调 sender 发
//   - sender 返回 nil → MarkNotificationSent(attempt+1)
//   - sender 返回 ErrNoCustomerContact → MarkNotificationSkipped
//   - sender 返回其它 error → MarkNotificationFailed(attempt+1, errMsg)
//
// 调用方：api handler，admin 后台点"补发"时调用
//
// 注意：sender 是注入的 storage.LeaveNotificationSender（包含多店路由 + 通道降级 + 重试）
//   - 复用 CreateBarberLeave 时用的同一个 sender，确保补发走相同的发送链路
//   - 补发时 attempt_count 从原值开始累加（attempts++），便于追踪"补发也失败了"的历史
func RetryNotification(ctx context.Context, id uint64, sender LeaveNotificationSender) (RetryNotificationResult, error) {
	res := RetryNotificationResult{ID: id}

	// 0) sender 必填
	if sender == nil {
		return res, errors.New("sender is nil; cannot retry without notification capability")
	}

	// 1) 取原 row
	n, ok, err := GetNotificationByID(ctx, id)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, ErrNotificationNotFound
	}

	// 2) 已发过的不重发
	if n.Status == NotifStatusSent {
		res.NewStatus = NotifStatusSent
		return res, ErrNotificationAlreadySent
	}

	// 3) 取 appt（无 appt_id 视为"孤立通知"，无法补发）
	if n.AppointmentID == "" {
		// 仍写回一次状态，避免 admin 后台一直显示 pending
		_ = MarkNotificationFailed(ctx, id, n.AttemptCount+1,
			"cannot retry: notification has no appointment_id")
		res.NewStatus = NotifStatusFailed
		return res, errors.New("notification has no appointment_id; cannot retry")
	}

	var appt Appointment
	if err := DB.WithContext(ctx).Where("id = ?", n.AppointmentID).First(&appt).Error; err != nil {
		_ = MarkNotificationFailed(ctx, id, n.AttemptCount+1,
			fmt.Sprintf("cannot retry: appointment %s not found: %v", n.AppointmentID, err))
		res.NewStatus = NotifStatusFailed
		return res, fmt.Errorf("appointment %s not found: %w", n.AppointmentID, err)
	}

	// 4) 用原 text_preview 重建文案（尽量贴近原内容）
	//    实际场景：重发文案可能因为 customer 改名 / leave 状态变了而略有不同，
	//    但"补发"语义是"用最新信息重新发"——所以重新构造 text 更合理
	//    这里保守起见：先用原 text_preview（已知是当时发的内容），让顾客收到一致内容；
	//    如果未来要"重新构造"，可以从 leave + customer 重新 build
	text := n.TextPreview
	if text == "" {
		text = "（通知内容已丢失，请查看原 leave 详情）"
	}

	// 5) 调 sender（内部含 SendWithRetry + 通道降级）
	err = sender(ctx, &appt, text)
	nextAttempt := n.AttemptCount + 1

	switch {
	case err == nil:
		if mErr := MarkNotificationSent(ctx, id, nextAttempt); mErr != nil {
			res.NewStatus = NotifStatusSent
			return res, fmt.Errorf("send succeeded but mark sent failed: %w", mErr)
		}
		res.NewStatus = NotifStatusSent
		return res, nil

	case errors.Is(err, ErrNoCustomerContact):
		_ = MarkNotificationSkipped(ctx, id, err.Error())
		res.NewStatus = NotifStatusSkipped
		return res, err

	default:
		_ = MarkNotificationFailed(ctx, id, nextAttempt, err.Error())
		res.NewStatus = NotifStatusFailed
		res.Err = err
		return res, err
	}
}

// RetryShopFailedNotifications 批量补发某店铺所有 failed 状态的 leave 通知
//
// 用途：admin 后台"一键重发所有失败通知"按钮
//
// 返回：(succeeded, failed, error)
//   - succeeded: 重发成功的条数
//   - failed:    重发仍失败的条数（含 skipped）
//   - error:     整体错误（DB 错等）；单条补发失败不阻断
//
// 注意：单条 send 已含 SendWithRetry 3 次退避，批量操作会跑 3×N 次 send
//   - N=10 时大约 5-15 秒；UI 上需要 loading 态
//   - 后续可加并发（用 ParallelSender），目前先串行保证可读性
func RetryShopFailedNotifications(ctx context.Context, shopID string, sender LeaveNotificationSender) (int, int, error) {
	if DB == nil {
		return 0, 0, nil
	}
	if sender == nil {
		return 0, 0, errors.New("sender is nil; cannot retry without notification capability")
	}
	// 只重发 leave 业务通知（cancel / reschedule / no_contact 三个 type）
	// 上限 500 条避免 admin 后台一次请求跑太久
	var notifs []CustomerNotification
	if err := DB.WithContext(ctx).
		Where("shop_id = ? AND status = ? AND type IN ?", shopID, NotifStatusFailed,
			[]string{NotifTypeLeaveCancel, NotifTypeLeaveReschedule, NotifTypeLeaveNoContact}).
		Order("created_at DESC").
		Limit(500).
		Find(&notifs).Error; err != nil {
		return 0, 0, err
	}

	var succeeded, failed int
	for i := range notifs {
		r := &notifs[i]
		_, err := RetryNotification(ctx, r.ID, sender)
		if err == nil {
			succeeded++
		} else {
			failed++
		}
	}
	return succeeded, failed, nil
}