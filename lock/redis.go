// Package lock 提供 Redis 分布式锁，用于防止并发预约同一时段（PRD §3.3 关键工程决策）
package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client 全局 Redis 客户端（InitRedis 后赋值）
var Client *redis.Client

// ErrLockNotAcquired 拿锁失败
var ErrLockNotAcquired = errors.New("lock not acquired")

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
		return nil, fmt.Errorf("连接 Redis %s 失败: %w",addr, err)
	}
	Client = c
	return c, nil
}

// Lock 单把锁句柄；用完记得 defer Unlock
type Lock struct {
	key   string
	token string
}

// Unlock 释放锁（Lua CAS：只释放自己的锁）
func (l *Lock) Unlock(ctx context.Context) error {
	if l == nil || Client == nil {
		return nil
	}
	const lua = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
else
  return 0
end`
	_, err := Client.Eval(ctx, lua, []string{l.key}, l.token).Result()
	return err
}

// AcquireAppointmentLock 尝试以 (barberID, date, time) 为 key 拿锁
//   - ttl 默认 10s（覆盖业务最长处理时间）
//   - wait 默认 1.5s 内重试
func AcquireAppointmentLock(ctx context.Context, barberID, date, timeStr string) (*Lock, error) {
	if Client == nil {
		// 没接 Redis 时降级为直接放行（开发期本地无 Redis 可用）
		return &Lock{key: "", token: ""}, nil
	}
	key := fmt.Sprintf("lock:appt:%s:%s:%s", barberID, date, timeStr)
	ttl := 10 * time.Second
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
			return &Lock{key: key, token: token}, nil
		}
		if time.Now().After(deadline) {
			return nil, ErrLockNotAcquired
		}
		time.Sleep(50 * time.Millisecond)
	}
}