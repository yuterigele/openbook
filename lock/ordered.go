package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// OrderedLockKeys 清洗、去重并按稳定字典序排列锁键。
func OrderedLockKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	ordered := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	return ordered
}

// OrderedLockSet 是按稳定顺序持有的一组锁。
type OrderedLockSet struct {
	locks []*Lock
}

// AcquireOrderedLocks 按字典序获取全部锁，任一锁失败则释放已经获取的锁。
func AcquireOrderedLocks(ctx context.Context, keys []string) (*OrderedLockSet, error) {
	ordered := OrderedLockKeys(keys)
	set := &OrderedLockSet{locks: make([]*Lock, 0, len(ordered))}
	for _, key := range ordered {
		item, err := acquireLockKey(ctx, key)
		if err != nil {
			_ = set.Unlock(context.Background())
			return nil, err
		}
		set.locks = append(set.locks, item)
	}
	return set, nil
}

// GuardContext 在任一锁丢失时取消返回的上下文。
func (s *OrderedLockSet) GuardContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if s == nil {
		return ctx, cancel
	}
	for _, item := range s.locks {
		guarded, guardedCancel := item.GuardContext(parent)
		go func() {
			defer guardedCancel()
			select {
			case <-guarded.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}

// Err 返回第一把已丢失的锁错误。
func (s *OrderedLockSet) Err() error {
	if s == nil {
		return nil
	}
	for _, item := range s.locks {
		if err := item.Err(); err != nil {
			return err
		}
	}
	return nil
}

// Unlock 按获取顺序的逆序释放全部锁。
func (s *OrderedLockSet) Unlock(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var firstErr error
	for index := len(s.locks) - 1; index >= 0; index-- {
		if err := s.locks[index].Unlock(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func acquireLockKey(ctx context.Context, key string) (*Lock, error) {
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("lock key is required")
	}
	if IsReadOnly() {
		return nil, ErrRedisUnavailable
	}
	if Client == nil {
		if redisLockRequired() {
			return nil, ErrRedisUnavailable
		}
		return &Lock{}, nil
	}
	ttl := appointmentLockTTL()
	wait := 1500 * time.Millisecond
	deadline := time.Now().Add(wait)
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate lock token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	for {
		ok, err := Client.SetNX(ctx, key, token, ttl).Result()
		if err != nil {
			return nil, fmt.Errorf("redis SetNX: %w", err)
		}
		if ok {
			item := &Lock{
				key: key, token: token, ttl: ttl, client: Client,
				stopCh: make(chan struct{}), lostCh: make(chan struct{}),
			}
			item.startWatchdog()
			return item, nil
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
