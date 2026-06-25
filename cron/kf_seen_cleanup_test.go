package cron

// kf_seen_cleanup_test.go
//
// KfSeenMsgCleaner 单元测试（v4.13.1）
//
// 覆盖：
//  1. Start/Stop 生命周期（无后台 goroutine 残留）
//  2. DB 未初始化时 scan() no-op，不 panic
//  3. scan() 调 CleanupKfSeenMsgs 删 7 天前的记录

import (
	"context"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

func TestKfSeenMsgCleaner_StartStop(t *testing.T) {
	c := NewKfSeenMsgCleaner()
	if c == nil {
		t.Fatal("NewKfSeenMsgCleaner returned nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := c.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// 二次 Stop 应 no-op
	if err := c.Stop(stopCtx); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestKfSeenMsgCleaner_NilDB_ScanIsSafe(t *testing.T) {
	// 没调 SetupTestDB，DB=nil。scan() 应直接 no-op，不 panic
	c := &KfSeenMsgCleaner{}
	c.scan() // 应当立即返回
}

// TestKfSeenMsgCleaner_ScanDeletesOldEntries 集成：scan() 实际清理
func TestKfSeenMsgCleaner_ScanDeletesOldEntries(t *testing.T) {
	storage.SetupTestDB(t)

	// 写 3 条：1 条新 + 2 条旧
	if err := storage.MarkKfMsgSeen("msg-fresh"); err != nil {
		t.Fatalf("Mark fresh: %v", err)
	}
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	storage.DB.Create(&storage.KfSeenMsg{MsgID: "msg-old-1", SeenAt: oldTime})
	storage.DB.Create(&storage.KfSeenMsg{MsgID: "msg-old-2", SeenAt: oldTime})

	c := &KfSeenMsgCleaner{}
	c.scan()

	// 验证：1 条新仍在 + 2 条旧已删
	var count int64
	storage.DB.Model(&storage.KfSeenMsg{}).Count(&count)
	if count != 1 {
		t.Errorf("清理后应只剩 1 条（5 分钟前的），got %d", count)
	}
}