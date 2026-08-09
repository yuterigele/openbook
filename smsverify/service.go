// Package smsverify 提供手机号验证码的发送和校验。
package smsverify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/yuterigele/openbook/lock"
	"github.com/yuterigele/openbook/storage"
)

var (
	ErrCodeRequired = errors.New("phone verification code required")
	ErrCodeInvalid  = errors.New("phone verification code invalid")
	ErrUnavailable  = errors.New("phone verification unavailable")
)

type sender interface {
	SendCode(context.Context, string, string) error
}

type store interface {
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string, time.Duration) error
	SetNX(context.Context, string, string, time.Duration) (bool, error)
	Delete(context.Context, ...string) error
	Increment(context.Context, string, time.Duration) (int64, error)
}

type redisStore struct{ client *redis.Client }

func (s redisStore) Get(ctx context.Context, key string) (string, error) {
	return s.client.Get(ctx, key).Result()
}
func (s redisStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, ttl).Err()
}
func (s redisStore) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	return s.client.SetNX(ctx, key, value, ttl).Result()
}
func (s redisStore) Delete(ctx context.Context, keys ...string) error {
	return s.client.Del(ctx, keys...).Err()
}
func (s redisStore) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	n, err := s.client.Incr(ctx, key).Result()
	if err == nil && n == 1 {
		err = s.client.Expire(ctx, key, ttl).Err()
	}
	return n, err
}

type service struct {
	store       store
	sender      sender
	ttl         time.Duration
	now         func() time.Time
	recordDebug func(context.Context, string, string, string, string, time.Time) error
	deleteDebug func(context.Context, string) error
}

var (
	serviceMu sync.RWMutex
	testSvc   *service
)

// DeliveryEnabled 返回是否向手机号真实发送验证码。
// 无论返回值如何，首次绑定和换号都必须校验验证码。
func DeliveryEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SMS_VERIFICATION_ENABLED")))
	return v == "1" || v == "true" || v == "yes"
}

// Required 返回部署是否显式配置了手机号验证模式。
// 未配置时保留旧部署兼容；.env.example 会显式配置为测试模式 0。
func Required() bool {
	_, ok := os.LookupEnv("SMS_VERIFICATION_ENABLED")
	return ok
}

// VerifyOrSend 校验验证码；code 为空时发送新验证码。
func VerifyOrSend(ctx context.Context, shopID, phone, code string) error {
	svc, err := currentService()
	if err != nil {
		return err
	}
	return svc.verifyOrSend(ctx, shopID, phone, strings.TrimSpace(code))
}

func currentService() (*service, error) {
	serviceMu.RLock()
	defer serviceMu.RUnlock()
	if testSvc != nil {
		return testSvc, nil
	}
	if lock.Client == nil {
		return nil, fmt.Errorf("%w: Redis 未初始化", ErrUnavailable)
	}
	svc := &service{store: redisStore{client: lock.Client}, ttl: envDuration("SMS_CODE_TTL", 5*time.Minute), now: time.Now}
	if DeliveryEnabled() {
		s, err := newTencentSenderFromEnv()
		if err != nil {
			return nil, err
		}
		svc.sender = s
	} else {
		svc.recordDebug = storage.SaveTestPhoneVerificationCode
		svc.deleteDebug = storage.DeleteTestPhoneVerificationCode
	}
	return svc, nil
}

func (s *service) verifyOrSend(ctx context.Context, shopID, phone, code string) error {
	base := "smsverify:" + digest(shopID+"\x00"+phone)
	codeKey, cooldownKey, attemptsKey := base+":code", base+":cooldown", base+":attempts"
	if code == "" {
		ok, err := s.store.SetNX(ctx, cooldownKey, "1", envDuration("SMS_SEND_COOLDOWN", time.Minute))
		if err != nil {
			return fmt.Errorf("%w: 保存发送频率失败", ErrUnavailable)
		}
		if !ok {
			return fmt.Errorf("验证码已发送，请稍后再试: %w", ErrCodeRequired)
		}
		generated, err := randomCode()
		if err != nil {
			_ = s.store.Delete(ctx, cooldownKey)
			return fmt.Errorf("%w: 生成验证码失败", ErrUnavailable)
		}
		if err := s.store.Set(ctx, codeKey, digest(generated), s.ttl); err != nil {
			_ = s.store.Delete(ctx, cooldownKey)
			return fmt.Errorf("%w: 保存验证码失败", ErrUnavailable)
		}
		if s.sender != nil {
			if err := s.sender.SendCode(ctx, phone, generated); err != nil {
				_ = s.store.Delete(ctx, cooldownKey, codeKey)
				return fmt.Errorf("%w: %v", ErrUnavailable, err)
			}
		} else if s.recordDebug != nil {
			if err := s.recordDebug(ctx, base, shopID, phone, generated, s.now().Add(s.ttl)); err != nil {
				_ = s.store.Delete(ctx, cooldownKey, codeKey)
				return fmt.Errorf("%w: 保存测试验证码失败", ErrUnavailable)
			}
		}
		_ = s.store.Delete(ctx, attemptsKey)
		return ErrCodeRequired
	}
	if len(code) != 6 {
		return ErrCodeInvalid
	}
	stored, err := s.store.Get(ctx, codeKey)
	if errors.Is(err, redis.Nil) {
		return fmt.Errorf("验证码已过期，请重新获取: %w", ErrCodeInvalid)
	}
	if err != nil {
		return fmt.Errorf("%w: 读取验证码失败", ErrUnavailable)
	}
	maxAttempts := envInt("SMS_CODE_MAX_ATTEMPTS", 5)
	attempts, err := s.store.Increment(ctx, attemptsKey, s.ttl)
	if err != nil {
		return fmt.Errorf("%w: 记录校验次数失败", ErrUnavailable)
	}
	if attempts > int64(maxAttempts) {
		_ = s.store.Delete(ctx, codeKey)
		return fmt.Errorf("验证码错误次数过多，请重新获取: %w", ErrCodeInvalid)
	}
	want, got := []byte(stored), []byte(digest(code))
	if len(want) != len(got) || subtle.ConstantTimeCompare(want, got) != 1 {
		return ErrCodeInvalid
	}
	if err := s.store.Delete(ctx, codeKey, attemptsKey); err != nil {
		return err
	}
	if s.deleteDebug != nil {
		return s.deleteDebug(ctx, base)
	}
	return nil
}

func randomCode() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	n := (uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])) % 1000000
	return fmt.Sprintf("%06d", n), nil
}

func digest(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key))); err == nil && v > 0 {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err == nil && v > 0 {
		return v
	}
	return fallback
}
