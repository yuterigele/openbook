package smsverify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type memoryItem struct {
	value string
	until time.Time
}

type memoryStore struct {
	mu    sync.Mutex
	items map[string]memoryItem
}

func newMemoryStore() *memoryStore { return &memoryStore{items: map[string]memoryItem{}} }
func (s *memoryStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.items[key]
	if !ok || time.Now().After(v.until) {
		delete(s.items, key)
		return "", redis.Nil
	}
	return v.value, nil
}
func (s *memoryStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key] = memoryItem{value: value, until: time.Now().Add(ttl)}
	return nil
}
func (s *memoryStore) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	if _, err := s.Get(ctx, key); err == nil {
		return false, nil
	}
	return true, s.Set(ctx, key, value, ttl)
}
func (s *memoryStore) Delete(_ context.Context, keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		delete(s.items, key)
	}
	return nil
}
func (s *memoryStore) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	v, err := s.Get(ctx, key)
	var n int64
	if err == nil {
		_, _ = fmt.Sscan(v, &n)
	}
	n++
	return n, s.Set(ctx, key, fmt.Sprint(n), ttl)
}

type captureSender struct{ code string }

func (s *captureSender) SendCode(_ context.Context, _ string, code string) error {
	s.code = code
	return nil
}

func TestVerifyOrSend(t *testing.T) {
	t.Setenv("SMS_SEND_COOLDOWN", "1m")
	t.Setenv("SMS_CODE_MAX_ATTEMPTS", "5")
	sender := &captureSender{}
	svc := &service{store: newMemoryStore(), sender: sender, ttl: 5 * time.Minute, now: time.Now}

	err := svc.verifyOrSend(context.Background(), "shop-1", "13800138000", "")
	if !errors.Is(err, ErrCodeRequired) || len(sender.code) != 6 {
		t.Fatalf("发送验证码结果不正确: err=%v code=%q", err, sender.code)
	}
	if err := svc.verifyOrSend(context.Background(), "shop-1", "13800138000", "000000"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("错误验证码应被拒绝，got %v", err)
	}
	if err := svc.verifyOrSend(context.Background(), "shop-1", "13800138000", sender.code); err != nil {
		t.Fatalf("正确验证码应通过，got %v", err)
	}
	if err := svc.verifyOrSend(context.Background(), "shop-1", "13800138000", sender.code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("验证码必须一次性使用，got %v", err)
	}
}

func TestVerifyOrSendRecordsDebugCodeWithoutSender(t *testing.T) {
	t.Setenv("SMS_SEND_COOLDOWN", "1m")
	var recorded, deleted string
	svc := &service{
		store: newMemoryStore(), ttl: 5 * time.Minute, now: time.Now,
		recordDebug: func(_ context.Context, id, _, _, code string, _ time.Time) error {
			recorded = code
			return nil
		},
		deleteDebug: func(_ context.Context, id string) error {
			deleted = id
			return nil
		},
	}
	if err := svc.verifyOrSend(context.Background(), "shop-1", "13800138000", ""); !errors.Is(err, ErrCodeRequired) {
		t.Fatalf("应生成测试验证码，got %v", err)
	}
	if len(recorded) != 6 {
		t.Fatalf("未记录测试验证码，got %q", recorded)
	}
	if err := svc.verifyOrSend(context.Background(), "shop-1", "13800138000", recorded); err != nil {
		t.Fatalf("测试验证码应通过，got %v", err)
	}
	if deleted == "" {
		t.Fatal("验证成功后应删除数据库测试验证码")
	}
}
