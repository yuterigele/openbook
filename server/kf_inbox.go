package server

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/yuterigele/openbook/storage"
	"github.com/yuterigele/openbook/wecom"
)

const (
	defaultKfInboxMaxAttempts = 5
	defaultKfInboxLeaseSecs   = 120
	defaultKfInboxPollSecs    = 5
)

func kfInboxLease() time.Duration {
	return time.Duration(getEnvInt("KF_INBOX_LEASE_SECONDS", defaultKfInboxLeaseSecs)) * time.Second
}

func kfInboxMaxAttempts() int {
	return getEnvInt("KF_INBOX_MAX_ATTEMPTS", defaultKfInboxMaxAttempts)
}

// enqueueKfInboxRows 把已经领取租约的持久化消息放入现有 per-session debounce。
// 一个 batch 只执行一次 Agent；成功或失败会同步写回 batch 内每条 inbox 消息。
func (s *Server[M]) enqueueKfInboxRows(ctx context.Context, sender replySender, rows []storage.KfInboxMessage) {
	for _, row := range rows {
		row := row
		text := &struct {
			Content string `json:"content"`
		}{Content: row.Content}
		item := &wecom.KfMsgItem{
			Msgid: row.MsgID, OpenKfid: row.OpenKfID, ExternalUserid: row.ExternalUserID,
			SendTime: row.SendTime, Origin: 3, MsgType: "text", Text: text,
			InboxLeaseToken: row.LeaseToken, InboxAttempts: row.Attempts,
		}
		sessionID := "wecom_" + row.ShopID + "_" + row.ExternalUserID
		kfDebounceEnqueue(sessionID, item, func(merged []*wecom.KfMsgItem) {
			s.processKfInboxBatch(context.WithoutCancel(ctx), sender, row.ShopID, merged)
		})
	}
}

func (s *Server[M]) processKfInboxBatch(ctx context.Context, sender replySender, shopID string, merged []*wecom.KfMsgItem) {
	if len(merged) == 0 {
		return
	}
	combined := ""
	for i, item := range merged {
		if i > 0 {
			combined += "\n"
		}
		if item.Text != nil {
			combined += item.Text.Content
		}
	}
	msg := &wecom.MessageXML{
		FromUserName: merged[0].ExternalUserid,
		OpenKfId:     merged[0].OpenKfid,
		MsgType:      "text",
		Content:      combined,
	}

	var processErr error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				processErr = fmt.Errorf("debounce batch panic: %v", recovered)
			}
		}()
		processErr = s.handleWeComMessageWithOpenKfID(ctx, sender, msg, merged[0].OpenKfid, shopID)
	}()

	for _, item := range merged {
		var err error
		if processErr == nil {
			err = storage.MarkKfInboxSucceeded(ctx, item.Msgid, item.InboxLeaseToken)
		} else {
			err = storage.MarkKfInboxFailed(ctx, item.Msgid, item.InboxLeaseToken,
				item.InboxAttempts, kfInboxMaxAttempts(), processErr)
		}
		if err != nil {
			log.Printf("[kf-inbox] 更新处理结果失败 msgid=%s: %v", item.Msgid, err)
		}
	}
	if processErr != nil {
		log.Printf("[kf-inbox] batch处理失败 shop=%s count=%d: %v", shopID, len(merged), processErr)
	}
}

// runKfInboxWorker 恢复 retry、pending 和租约过期的 processing 消息。
// HTTP 人工重放只改状态，最多一个轮询周期后由这里安全领取。
func (s *Server[M]) runKfInboxWorker(ctx context.Context) {
	poll := time.Duration(getEnvInt("KF_INBOX_POLL_SECONDS", defaultKfInboxPollSecs)) * time.Second
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	cleanupTicker := time.NewTicker(24 * time.Hour)
	defer cleanupTicker.Stop()
	log.Printf("[kf-inbox] worker启动 poll=%s lease=%s max_attempts=%d", poll, kfInboxLease(), kfInboxMaxAttempts())
	s.cleanupSucceededKfInbox(ctx)
	for {
		s.drainKfInbox(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-cleanupTicker.C:
			s.cleanupSucceededKfInbox(ctx)
		}
	}
}

func (s *Server[M]) cleanupSucceededKfInbox(ctx context.Context) {
	deleted, err := storage.CleanupSucceededKfInbox(ctx, time.Now().Add(-7*24*time.Hour))
	if err != nil {
		log.Printf("[kf-inbox] 清理成功消息失败: %v", err)
		return
	}
	if deleted > 0 {
		log.Printf("[kf-inbox] 已清理 %d 条超过7天的成功消息", deleted)
	}
}

func (s *Server[M]) drainKfInbox(ctx context.Context) {
	rows, err := storage.ClaimKfInboxDue(ctx, "", 50, kfInboxLease())
	if err != nil {
		log.Printf("[kf-inbox] 领取消息失败: %v", err)
		return
	}
	for _, row := range rows {
		route, ok := s.cfg.WeComRouter.LookupByShopID(row.ShopID)
		if !ok || route.Client == nil {
			err := fmt.Errorf("门店 %s 的微信客服路由不可用", row.ShopID)
			if markErr := storage.MarkKfInboxFailed(ctx, row.MsgID, row.LeaseToken,
				row.Attempts, kfInboxMaxAttempts(), err); markErr != nil {
				log.Printf("[kf-inbox] 路由失败状态写回失败 msgid=%s: %v", row.MsgID, markErr)
			}
			continue
		}
		s.enqueueKfInboxRows(context.WithoutCancel(ctx), route.Client, []storage.KfInboxMessage{row})
	}
}
