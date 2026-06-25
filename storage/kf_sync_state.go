package storage

// kf_sync_state.go
//
// 微信客服消息同步的持久化状态（v4.13.1）
//
// 背景：
//   - v4.13.1 之前 cursor 和 seenMsgIDs 都存在进程内 kfMessageTracker (server.go:1525)
//   - 进程重启 → 全部清零 → 下次 sync_msg 把历史消息拉一遍
//   - 后果：用户消息被首次拉取逻辑"前 N-1 条跳过"静默丢弃 / agent 多条回复 spam
//
// 修法：
//   - cursor 持久化到 kf_sync_state 表（按 open_kf_id 维度，多客服账号隔离）
//   - msgid seen 持久化到 kf_seen_msg 表（主键 msg_id，TTL 7 天由清理函数回收）
//   - 启动后从 DB 恢复，进程重启不再丢状态

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// kfSeenTTL msgid 去重表保留时长
// 7 天足够覆盖任何 sync_msg 重试窗口 + 周末 / 长假（企业最常在周末不维护）。
const kfSeenTTL = 7 * 24 * time.Hour

// GetKfCursor 读 cursor（返回 "" 表示首次）
//
// 幂等：open_kf_id 不存在时返回 ""（调用方据此判断"首次拉取"）
func GetKfCursor(openKfID string) (string, error) {
	if DB == nil {
		return "", errors.New("storage.DB not initialized")
	}
	var s KfSyncState
	err := DB.Where("open_kf_id = ?", openKfID).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil // 首次
	}
	if err != nil {
		return "", err
	}
	return s.Cursor, nil
}

// SetKfCursor 持久化 cursor（UPSERT）
//
// 每次 sync_msg 拿到 next_cursor 后调用。重启后 GetKfCursor 会拿到这个值。
func SetKfCursor(openKfID, cursor string) error {
	if DB == nil {
		return errors.New("storage.DB not initialized")
	}
	now := time.Now()
	// UPSERT：open_kf_id 主键冲突时刷新 cursor + updated_at（不刷 created_at）
	//   - MySQL: INSERT ... ON DUPLICATE KEY UPDATE
	//   - SQLite (测试): 通过 clause.OnConflict 实现
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "open_kf_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"cursor", "updated_at"}),
	}).Create(&KfSyncState{
		OpenKfID:  openKfID,
		Cursor:    cursor,
		UpdatedAt: now,
		CreatedAt: now,
	}).Error
}

// IsKfMsgSeen 判断 msgid 是否已处理过（用于去重）
func IsKfMsgSeen(msgID string) (bool, error) {
	if DB == nil {
		return false, errors.New("storage.DB not initialized")
	}
	if msgID == "" {
		// 没有 msgid（理论上不应该，但 fallback 保护）—— 视为未处理，让下游逻辑决定
		return false, nil
	}
	var m KfSeenMsg
	err := DB.Where("msg_id = ?", msgID).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// MarkKfMsgSeen 标记 msgid 为已处理（UPSERT）
//
// 多次调用幂等：msgid 已存在时只刷 seen_at。
func MarkKfMsgSeen(msgID string) error {
	if DB == nil {
		return errors.New("storage.DB not initialized")
	}
	if msgID == "" {
		return nil // 保护：空 msgid 不入库
	}
	now := time.Now()
	return DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "msg_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"seen_at"}),
	}).Create(&KfSeenMsg{
		MsgID:  msgID,
		SeenAt: now,
	}).Error
}

// CleanupKfSeenMsgs 清理 seen_at < now-7d 的去重记录
//
// 返回删除条数。生产环境由 cron 定时调用（避免表无限增长）。
// 测试环境可手动调。
func CleanupKfSeenMsgs() (int64, error) {
	if DB == nil {
		return 0, errors.New("storage.DB not initialized")
	}
	cutoff := time.Now().Add(-kfSeenTTL)
	res := DB.Where("seen_at < ?", cutoff).Delete(&KfSeenMsg{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}