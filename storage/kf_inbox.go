package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	KfInboxPending    = "pending"
	KfInboxProcessing = "processing"
	KfInboxRetry      = "retry"
	KfInboxSucceeded  = "succeeded"
	KfInboxDeadLetter = "dead_letter"
)

// KfInboxInput 是 sync_msg 拉取结果写入 inbox 所需的最小字段集合。
// storage 不依赖 wecom 包，避免业务接入层和存储层形成循环依赖。
type KfInboxInput struct {
	MsgID          string
	ShopID         string
	OpenKfID       string
	ExternalUserID string
	Content        string
	SendTime       int64
}

// SaveKfInboxBatchAndCursor 把消息与下一页 cursor 原子落库。
// 只有事务提交后上游才可以认为本次 sync_msg 已经安全接收。
func SaveKfInboxBatchAndCursor(ctx context.Context, openKfID, nextCursor string, inputs []KfInboxInput) error {
	if DB == nil {
		return errors.New("storage.DB not initialized")
	}
	now := time.Now()
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, input := range inputs {
			if strings.TrimSpace(input.MsgID) == "" || strings.TrimSpace(input.ShopID) == "" ||
				strings.TrimSpace(input.OpenKfID) == "" || strings.TrimSpace(input.ExternalUserID) == "" {
				return errors.New("微信客服 inbox 缺少消息、门店、客服账号或顾客标识")
			}
			row := KfInboxMessage{
				MsgID: input.MsgID, ShopID: input.ShopID, OpenKfID: input.OpenKfID,
				ExternalUserID: input.ExternalUserID, Content: input.Content, SendTime: input.SendTime,
				Status: KfInboxPending, NextRetryAt: &now, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
			// 保留旧表供现有运维查询和滚动升级使用，但可靠消费只以 inbox 状态为准。
			seen := KfSeenMsg{MsgID: input.MsgID, SeenAt: now}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "msg_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"seen_at"}),
			}).Create(&seen).Error; err != nil {
				return err
			}
		}
		if nextCursor == "" {
			return nil
		}
		state := KfSyncState{OpenKfID: openKfID, Cursor: nextCursor, CreatedAt: now, UpdatedAt: now}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "open_kf_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"cursor", "updated_at"}),
		}).Create(&state).Error
	})
}

// ClaimKfInboxDue 用带 token 的租约领取待处理消息。processing 租约过期后可以
// 被其他 worker 恢复；旧 worker 因 token 不匹配，不能覆盖新 worker 的结果。
func ClaimKfInboxDue(ctx context.Context, openKfID string, limit int, lease time.Duration) ([]KfInboxMessage, error) {
	if DB == nil {
		return nil, errors.New("storage.DB not initialized")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	now := time.Now()
	query := DB.WithContext(ctx).Model(&KfInboxMessage{}).
		Where("((status IN ?) AND (next_retry_at IS NULL OR next_retry_at <= ?)) OR (status = ? AND lease_until < ?)",
			[]string{KfInboxPending, KfInboxRetry}, now, KfInboxProcessing, now)
	if openKfID != "" {
		query = query.Where("open_kf_id = ?", openKfID)
	}
	var candidates []KfInboxMessage
	if err := query.Order("send_time ASC, created_at ASC").Limit(limit).Find(&candidates).Error; err != nil {
		return nil, err
	}
	claimed := make([]KfInboxMessage, 0, len(candidates))
	for _, candidate := range candidates {
		token := uuid.NewString()
		until := now.Add(lease)
		res := DB.WithContext(ctx).Model(&KfInboxMessage{}).
			Where("msg_id = ? AND (((status IN ?) AND (next_retry_at IS NULL OR next_retry_at <= ?)) OR (status = ? AND lease_until < ?))",
				candidate.MsgID, []string{KfInboxPending, KfInboxRetry}, now, KfInboxProcessing, now).
			Updates(map[string]any{
				"status": KfInboxProcessing, "attempts": gorm.Expr("attempts + 1"),
				"lease_until": until, "lease_token": token, "updated_at": now,
			})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected != 1 {
			continue
		}
		candidate.Status = KfInboxProcessing
		candidate.Attempts++
		candidate.LeaseUntil = &until
		candidate.LeaseToken = token
		claimed = append(claimed, candidate)
	}
	return claimed, nil
}

func MarkKfInboxSucceeded(ctx context.Context, msgID, leaseToken string) error {
	if DB == nil {
		return errors.New("storage.DB not initialized")
	}
	now := time.Now()
	res := DB.WithContext(ctx).Model(&KfInboxMessage{}).
		Where("msg_id = ? AND status = ? AND lease_token = ?", msgID, KfInboxProcessing, leaseToken).
		Updates(map[string]any{
			"status": KfInboxSucceeded, "processed_at": now, "next_retry_at": nil,
			"lease_until": nil, "lease_token": "", "last_error": "", "updated_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errors.New("inbox 消息租约已变化，拒绝覆盖处理结果")
	}
	return nil
}

// MarkKfInboxFailed 安排指数退避；达到最大次数后进入 dead-letter。
func MarkKfInboxFailed(ctx context.Context, msgID, leaseToken string, attempts, maxAttempts int, processErr error) error {
	if DB == nil {
		return errors.New("storage.DB not initialized")
	}
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	now := time.Now()
	status := KfInboxRetry
	next := now.Add(kfInboxBackoff(attempts))
	updates := map[string]any{
		"status": status, "next_retry_at": next, "lease_until": nil, "lease_token": "",
		"last_error": truncateInboxError(processErr), "updated_at": now,
	}
	if attempts >= maxAttempts {
		updates["status"] = KfInboxDeadLetter
		updates["next_retry_at"] = nil
	}
	res := DB.WithContext(ctx).Model(&KfInboxMessage{}).
		Where("msg_id = ? AND status = ? AND lease_token = ?", msgID, KfInboxProcessing, leaseToken).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errors.New("inbox 消息租约已变化，拒绝覆盖失败状态")
	}
	return nil
}

func kfInboxBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<(attempt-1)) * time.Minute
}

func truncateInboxError(err error) string {
	if err == nil {
		return "处理失败"
	}
	s := strings.TrimSpace(err.Error())
	if len(s) > 1000 {
		s = s[:1000]
	}
	return s
}

// ListKfInboxMessages 按门店隔离查询。shopID 为空只允许平台层调用。
func ListKfInboxMessages(ctx context.Context, shopID, status string, limit int) ([]KfInboxMessage, error) {
	if DB == nil {
		return nil, errors.New("storage.DB not initialized")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := DB.WithContext(ctx).Model(&KfInboxMessage{})
	if shopID != "" {
		q = q.Where("shop_id = ?", shopID)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	} else {
		q = q.Where("status IN ?", []string{KfInboxRetry, KfInboxDeadLetter})
	}
	var rows []KfInboxMessage
	err := q.Order("updated_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func GetKfInboxMessage(ctx context.Context, msgID, shopID string, platform bool) (*KfInboxMessage, error) {
	if DB == nil {
		return nil, errors.New("storage.DB not initialized")
	}
	q := DB.WithContext(ctx).Where("msg_id = ?", msgID)
	if !platform {
		q = q.Where("shop_id = ?", shopID)
	}
	var row KfInboxMessage
	if err := q.First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// ReplayKfInboxMessage 只允许重放失败或死信消息。重置自动重试次数，保留
// last_error 供后台追溯；实际处理仍由 worker 领取，不在 HTTP 请求内执行 Agent。
func ReplayKfInboxMessage(ctx context.Context, msgID, shopID string, platform bool) error {
	if DB == nil {
		return errors.New("storage.DB not initialized")
	}
	now := time.Now()
	q := DB.WithContext(ctx).Model(&KfInboxMessage{}).
		Where("msg_id = ? AND status IN ?", msgID, []string{KfInboxRetry, KfInboxDeadLetter})
	if !platform {
		q = q.Where("shop_id = ?", shopID)
	}
	res := q.Updates(map[string]any{
		"status": KfInboxPending, "attempts": 0, "next_retry_at": now,
		"lease_until": nil, "lease_token": "", "replayed_at": now, "updated_at": now,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("失败消息不存在、无权访问或当前状态不可重放")
	}
	return nil
}

// CleanupSucceededKfInbox 删除已经成功且超过保留期的消息正文。失败和死信不自动
// 删除，必须保留给管理员排查；默认成功记录只保留 7 天，减少顾客原文长期沉淀。
func CleanupSucceededKfInbox(ctx context.Context, before time.Time) (int64, error) {
	if DB == nil {
		return 0, errors.New("storage.DB not initialized")
	}
	res := DB.WithContext(ctx).Where("status = ? AND processed_at < ?", KfInboxSucceeded, before).
		Delete(&KfInboxMessage{})
	return res.RowsAffected, res.Error
}
