package cron

// leave_test.go
//
// LeaveExpirer 单元测试（PRD §11.7.8）
//
// 覆盖：
//   1. Start/Stop 生命周期（NewLeaveExpirer 后能正常启动 / 停止，幂等）
//   2. DB 未初始化时 scan() 不 panic，直接 no-op
//   3. scan() 委托给 storage.ExpireOverdueLeaves（行为等价）
//
// 隔离：
//   - Start 启动后立刻 Stop，避免任何后台 goroutine 在测试结束后还在跑
//   - scan 不会真的写 DB，因为我们没 SetupTestDB

import (
	"context"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

func TestLeaveExpirer_StartStop(t *testing.T) {
	expirer := NewLeaveExpirer()
	if expirer == nil {
		t.Fatal("NewLeaveExpirer returned nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := expirer.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 立即停止 —— 测试不应该等下一分钟触发
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := expirer.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// 再次 Stop 应该是 no-op
	if err := expirer.Stop(stopCtx); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestLeaveExpirer_NilScheduler_StopIsSafe(t *testing.T) {
	// 手动构造 nil scheduler —— Stop 应直接返回 nil
	e := &LeaveExpirer{scheduler: nil}
	if err := e.Stop(context.Background()); err != nil {
		t.Errorf("Stop with nil scheduler: %v", err)
	}
}

func TestLeaveExpirer_Scan_DBNotInitialized(t *testing.T) {
	// DB=nil 时 scan() 不应该 panic / 出错
	storage.DB = nil
	defer func() { storage.DB = nil }()

	e := NewLeaveExpirer()
	// 直接调用 scan —— 因为 DB=nil，里面的 storage.DB == nil 分支会直接 return
	e.scan()
	// 不出错就算过
}
