package wecom

import (
	"context"
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/yuterigele/openbook/storage"
)

// MarkMessageProcessed 用 MsgId 唯一索引做幂等去重。
//   - 第一次见到 MsgId：写入记录，返回 (true, nil)
//   - 重复消息：返回 (false, nil)，调用方应直接 ack 不再处理
//
// 错误情况（DB 不可用等）返回 error，由调用方决定是否重试。
//
// 重要：企业微信回调可能重试 N 次，必须用 DB 唯一索引保证重启不丢。
func MarkMessageProcessed(ctx context.Context, msg *MessageXML) (bool, error) {
	if storage.DB == nil {
		// 没接 DB 时降级为不去重（仅本地开发用）
		return true, nil
	}
	if msg.MsgId == 0 {
		// 事件类回调可能没 MsgId，按 token + event 去重
		if msg.Event != "" && msg.Token != "" {
			msg.MsgId = hashTokenAndEvent(msg.Token, msg.Event, msg.FromUserName, msg.CreateTime)
		} else {
			return true, nil
		}
	}

	rec := storage.WecomMessageLog{
		MsgID:        msg.MsgId,
		MsgType:      msg.MsgType,
		Event:        msg.Event,
		OpenKfID:     msg.OpenKfId,
		FromUserName: msg.FromUserName,
		ToUserName:   msg.ToUserName,
		Processed:    true,
		ReceivedAt:   time.Unix(msg.CreateTime, 0),
		CreatedAt:    time.Now(),
	}

	err := storage.DB.WithContext(ctx).
		Where(storage.WecomMessageLog{MsgID: msg.MsgId}).
		Attrs(rec).
		Create(&rec).Error
	if err == nil {
		return true, nil // 首次
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) || isDuplicateKeyErr(err) {
		return false, nil // 重复
	}
	return false, err
}

func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	// MySQL 1062 错误：Duplicate entry
	return contains(err.Error(), "Duplicate entry") || contains(err.Error(), "1062")
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// hashTokenAndEvent 把 token+event+from+time 哈希成一个稳定的 int64 当 MsgId 用
func hashTokenAndEvent(token, event, from string, ts int64) int64 {
	h := int64(1469598103934665603) // FNV offset basis
	for _, c := range []byte(token + "|" + event + "|" + from + "|" + strconv.FormatInt(ts, 10)) {
		h ^= int64(c)
		h *= 1099511628211
	}
	// MySQL BIGINT 是 int64，但 MsgId 字段类型可能不够大；取绝对值避免负数主键歧义
	if h < 0 {
		h = -h
	}
	return h
}