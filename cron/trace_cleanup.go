package cron

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	robfigcron "github.com/robfig/cron/v3"

	"github.com/yuterigele/openbook/storage"
)

// TraceSpanCleaner 按配置清理短周期技术链路；业务审计不在此处删除。
type TraceSpanCleaner struct {
	scheduler *robfigcron.Cron
	days      int
}

func NewTraceSpanCleaner() *TraceSpanCleaner {
	days := 14
	if parsed, err := strconv.Atoi(os.Getenv("TRACE_RETENTION_DAYS")); err == nil && parsed >= 1 && parsed <= 365 {
		days = parsed
	}
	return &TraceSpanCleaner{scheduler: robfigcron.New(robfigcron.WithSeconds()), days: days}
}

func (c *TraceSpanCleaner) Start(_ context.Context) error {
	if _, err := c.scheduler.AddFunc("0 30 3 * * *", c.scan); err != nil {
		return fmt.Errorf("注册 trace_span cleanup cron 失败: %w", err)
	}
	c.scheduler.Start()
	log.Printf("[cron] 启动 trace_span cleanup: 每天 3:30 清理 %d 天前技术链路", c.days)
	return nil
}

func (c *TraceSpanCleaner) Stop(ctx context.Context) error {
	stopped := c.scheduler.Stop()
	select {
	case <-stopped.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *TraceSpanCleaner) scan() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	deleted, err := storage.DeleteTraceSpansBefore(ctx, time.Now().AddDate(0, 0, -c.days))
	if err != nil {
		log.Printf("[trace-cleanup] 清理失败: %v", err)
		return
	}
	if deleted > 0 {
		log.Printf("[trace-cleanup] 已清理 %d 条技术链路", deleted)
	}
}
