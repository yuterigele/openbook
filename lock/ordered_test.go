package lock

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestOrderedLockKeysSortsAndDeduplicates(t *testing.T) {
	got := OrderedLockKeys([]string{" resource:b ", "staff:a", "resource:b", "", "staff:a", "resource:a"})
	want := []string{"resource:a", "resource:b", "staff:a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered keys = %#v, want %#v", got, want)
	}
}

func TestAcquireOrderedLocksAllowsNoRedisInDevelopment(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("REDIS_REQUIRED", "")
	Client = nil
	set, err := AcquireOrderedLocks(context.Background(), []string{"b", "a", "a"})
	if err != nil {
		t.Fatalf("development lock acquisition failed: %v", err)
	}
	if len(set.locks) != 2 || set.locks[0].client != nil || set.locks[1].client != nil {
		t.Fatalf("unexpected development lock set: %+v", set.locks)
	}
	if err := set.Unlock(context.Background()); err != nil {
		t.Fatalf("unlock failed: %v", err)
	}
}

func TestAcquireOrderedLocksFailsClosedWhenRedisRequired(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("REDIS_REQUIRED", "1")
	Client = nil
	_, err := AcquireOrderedLocks(context.Background(), []string{"a", "b"})
	if !errors.Is(err, ErrRedisUnavailable) {
		t.Fatalf("production lock acquisition should fail closed, got %v", err)
	}
}
