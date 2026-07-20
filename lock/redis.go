// Package lock 提供 Redis 分布式锁，用于防止并发预约同一时段（PRD §3.3 关键工程决策）
package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client 全局 Redis 客户端（InitRedis 后赋值）
var Client *redis.Client

// ErrLockNotAcquired 拿锁失败
var ErrLockNotAcquired = errors.New("lock not acquired")

// ErrLockLost 表示锁在业务完成前过期、被替换或续租失败。
var ErrLockLost = errors.New("lock ownership lost")

// ErrRedisUnavailable 表示生产环境要求 Redis 锁，但客户端不可用。
var ErrRedisUnavailable = errors.New("redis lock unavailable")

// InitRedis 连接 Redis；缺 REDIS_ADDR 时尝试 127.0.0.1:6379
func InitRedis(ctx context.Context) (*redis.Client, error) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	password := os.Getenv("REDIS_PASSWORD")
	db := 0
	if d := os.Getenv("REDIS_DB"); d != "" {
		fmt.Sscanf(d, "%d", &db)
	}

	c := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
	})
	if err := c.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("连接 Redis %s 失败: %w", addr, err)
	}
	Client = c
	return c, nil
}

// Lock 单把锁句柄；用完记得 defer Unlock
type Lock struct {
	key    string
	token  string
	ttl    time.Duration
	client *redis.Client

	stopOnce sync.Once
	lostOnce sync.Once
	stopCh   chan struct{}
	lostCh   chan struct{}
	wg       sync.WaitGroup
	mu       sync.RWMutex
	err      error
}

const compareAndDeleteLua = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
else
  return 0
end`

const compareAndRenewLua = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
else
  return 0
end`

// Unlock 释放锁（Lua CAS：只释放自己的锁）
func (l *Lock) Unlock(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.stop()
	if l.client == nil || l.key == "" {
		return nil
	}
	_, err := l.client.Eval(ctx, compareAndDeleteLua, []string{l.key}, l.token).Result()
	return err
}

// GuardContext 返回一个在父 context 结束或锁丢失时自动取消的 context。
func (l *Lock) GuardContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if l == nil || l.lostCh == nil {
		return ctx, cancel
	}
	go func() {
		select {
		case <-parent.Done():
		case <-l.lostCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// Err 返回看门狗记录的锁丢失原因。
func (l *Lock) Err() error {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.err
}

func (l *Lock) startWatchdog() {
	if l == nil || l.client == nil || l.key == "" {
		return
	}
	interval := l.ttl / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-l.stopCh:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), interval)
				result, err := l.client.Eval(ctx, compareAndRenewLua, []string{l.key}, l.token, l.ttl.Milliseconds()).Int()
				cancel()
				if err != nil {
					l.markLost(fmt.Errorf("renew appointment lock: %w", err))
					return
				}
				if result != 1 {
					l.markLost(ErrLockLost)
					return
				}
			}
		}
	}()
}

func (l *Lock) markLost(err error) {
	l.lostOnce.Do(func() {
		l.mu.Lock()
		l.err = err
		l.mu.Unlock()
		close(l.lostCh)
	})
}

func (l *Lock) stop() {
	if l == nil || l.stopCh == nil {
		return
	}
	l.stopOnce.Do(func() { close(l.stopCh) })
	l.wg.Wait()
}

// AcquireAppointmentLock 尝试以 (barberID, date, time) 为 key 拿锁
//   - ttl 默认 10s，可用 APPOINTMENT_LOCK_TTL 调整
//   - 获取成功后看门狗约每 ttl/3 校验 token 并续租
//   - wait 默认 1.5s 内重试
func AcquireAppointmentLock(ctx context.Context, barberID, date, timeStr string) (*Lock, error) {
	if Client == nil {
		if redisLockRequired() {
			return nil, ErrRedisUnavailable
		}
		// 开发/测试允许依靠数据库 active_slot_key 唯一约束继续运行。
		return &Lock{}, nil
	}
	key := fmt.Sprintf("lock:appt:%s:%s:%s", barberID, date, timeStr)
	ttl := appointmentLockTTL()
	wait := 1500 * time.Millisecond
	deadline := time.Now().Add(wait)
	tokenBytes := make([]byte, 16)
	_, _ = rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)

	for {
		ok, err := Client.SetNX(ctx, key, token, ttl).Result()
		if err != nil {
			return nil, fmt.Errorf("redis SetNX: %w", err)
		}
		if ok {
			l := &Lock{
				key: key, token: token, ttl: ttl, client: Client,
				stopCh: make(chan struct{}), lostCh: make(chan struct{}),
			}
			l.startWatchdog()
			return l, nil
		}
		if time.Now().After(deadline) {
			return nil, ErrLockNotAcquired
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func appointmentLockTTL() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("APPOINTMENT_LOCK_TTL")); raw != "" {
		if ttl, err := time.ParseDuration(raw); err == nil && ttl >= time.Second {
			return ttl
		}
	}
	return 10 * time.Second
}

func redisLockRequired() bool {
	return os.Getenv("REDIS_REQUIRED") == "1" || strings.EqualFold(os.Getenv("APP_ENV"), "production")
}
