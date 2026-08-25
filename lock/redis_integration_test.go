//go:build redis_integration

package lock

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRedisLockWatchdogAndOwnershipLoss(t *testing.T) {
	if os.Getenv("REDIS_ADDR") == "" {
		t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	}
	t.Setenv("APP_ENV", "staging")
	t.Setenv("REDIS_REQUIRED", "1")
	t.Setenv("APPOINTMENT_LOCK_TTL", "1s")

	oldClient := Client
	client, err := InitRedis(context.Background())
	if err != nil {
		t.Skipf("Redis 集成依赖不可用: %v", err)
	}
	t.Cleanup(func() {
		Client = oldClient
		_ = client.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	barberID := "integration-" + uuid.NewString()
	date := "2099-01-02"
	timeStr := "10:00"
	l, err := AcquireAppointmentLock(ctx, barberID, date, timeStr)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Unlock(context.Background())

	key := "lock:appt:" + barberID + ":" + date + ":" + timeStr
	guarded, guardedCancel := l.GuardContext(ctx)
	defer guardedCancel()

	// 等待超过原始 TTL，确认看门狗仍持有自己的锁。
	time.Sleep(1500 * time.Millisecond)
	if value, err := client.Get(ctx, key).Result(); err != nil || value != l.token {
		t.Fatalf("watchdog did not renew lock: value=%q err=%v", value, err)
	}

	// 模拟锁被外部客户端替换；看门狗必须报告丢锁并取消业务上下文。
	if err := client.Set(ctx, key, "foreign-owner", time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-guarded.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("guarded context was not canceled after ownership loss")
	}
	if !errors.Is(l.Err(), ErrLockLost) {
		t.Fatalf("lock error = %v, want ErrLockLost", l.Err())
	}
	if value, err := client.Get(ctx, key).Result(); err != nil || value != "foreign-owner" {
		t.Fatalf("unlock must not delete a foreign lock: value=%q err=%v", value, err)
	}
}

func TestRedisLockContenderCanAcquireAfterRelease(t *testing.T) {
	if os.Getenv("REDIS_ADDR") == "" {
		t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	}
	t.Setenv("APP_ENV", "staging")
	t.Setenv("REDIS_REQUIRED", "1")

	oldClient := Client
	client, err := InitRedis(context.Background())
	if err != nil {
		t.Skipf("Redis 集成依赖不可用: %v", err)
	}
	t.Cleanup(func() {
		Client = oldClient
		_ = client.Close()
	})

	keySuffix := uuid.NewString()
	first, err := acquireLockKey(context.Background(), "lock:integration:"+keySuffix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLockKey(context.Background(), "lock:integration:"+keySuffix); !errors.Is(err, ErrLockNotAcquired) {
		t.Fatalf("contender error = %v, want ErrLockNotAcquired", err)
	}
	if err := first.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}

	second, err := acquireLockKey(context.Background(), "lock:integration:"+keySuffix)
	if err != nil {
		t.Fatalf("lock should be acquirable after release: %v", err)
	}
	if err := second.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
}
