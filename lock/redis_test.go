package lock

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcquireAppointmentLockRequiresRedisInProduction(t *testing.T) {
	old := Client
	Client = nil
	t.Cleanup(func() { Client = old })
	t.Setenv("APP_ENV", "production")
	if _, err := AcquireAppointmentLock(context.Background(), "b", "2099-01-01", "10:00"); !errors.Is(err, ErrRedisUnavailable) {
		t.Fatalf("err = %v, want ErrRedisUnavailable", err)
	}
}

func TestLockGuardContextCanceledWhenOwnershipLost(t *testing.T) {
	l := &Lock{lostCh: make(chan struct{}), stopCh: make(chan struct{})}
	ctx, cancel := l.GuardContext(context.Background())
	defer cancel()
	l.markLost(ErrLockLost)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("guard context was not canceled")
	}
	if !errors.Is(l.Err(), ErrLockLost) {
		t.Fatalf("lock err = %v", l.Err())
	}
}

func TestAppointmentLockTTLFromEnv(t *testing.T) {
	t.Setenv("APPOINTMENT_LOCK_TTL", "3s")
	if got := appointmentLockTTL(); got != 3*time.Second {
		t.Fatalf("ttl = %v", got)
	}
	t.Setenv("APPOINTMENT_LOCK_TTL", "bad")
	if got := appointmentLockTTL(); got != 10*time.Second {
		t.Fatalf("invalid ttl fallback = %v", got)
	}
}
