/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestRateLimiter_FirstRequestAllowed(t *testing.T) {
	// Burst of 5, sustained 1/sec. First request from a brand-new
	// key must always succeed (it's a fresh bucket).
	rl := NewRateLimiter(rate.Every(time.Second), 5, 100)
	if !rl.Allow("user-A") {
		t.Error("first request from a new key should be allowed")
	}
}

func TestRateLimiter_BurstExhaustion(t *testing.T) {
	// Burst of 3, sustained 100/sec (effectively infinite for this
	// test). The first 3 should pass; the 4th should be throttled.
	rl := NewRateLimiter(rate.Every(10*time.Millisecond), 3, 100)
	for i := 0; i < 3; i++ {
		if !rl.Allow("user-A") {
			t.Errorf("burst request %d should be allowed", i)
		}
	}
	if rl.Allow("user-A") {
		t.Error("4th request should be throttled (burst=3 exhausted)")
	}
}

func TestRateLimiter_IndependentKeys(t *testing.T) {
	// user-A bursting does not affect user-B.
	rl := NewRateLimiter(rate.Every(time.Second), 1, 100)

	if !rl.Allow("user-A") {
		t.Fatal("user-A first request should be allowed")
	}
	if rl.Allow("user-A") {
		t.Fatal("user-A burst exhausted")
	}
	if !rl.Allow("user-B") {
		t.Error("user-B should have its own bucket")
	}
}

func TestRateLimiter_RefillOverTime(t *testing.T) {
	// Burst 1, sustained 10/sec. After consuming, wait 200ms, should
	// refill ~2 tokens and let one more through.
	rl := NewRateLimiter(rate.Every(100*time.Millisecond), 1, 100)

	if !rl.Allow("user-A") {
		t.Fatal("first request should pass")
	}
	if rl.Allow("user-A") {
		t.Fatal("burst exhausted, should throttle")
	}
	time.Sleep(150 * time.Millisecond) // ~1.5 tokens refilled
	if !rl.Allow("user-A") {
		t.Error("after refill, request should pass")
	}
}

func TestRateLimiter_LRUEviction(t *testing.T) {
	// cap=2, fill with 2 keys, then add a 3rd → 1 key evicted.
	rl := NewRateLimiter(rate.Every(time.Second), 1, 2)
	rl.Allow("a")
	rl.Allow("b")
	if rl.Size() != 2 {
		t.Fatalf("size after 2 inserts = %d, want 2", rl.Size())
	}
	rl.Allow("c")
	if rl.Size() != 2 {
		t.Errorf("size after 3 inserts with cap=2 = %d, want 2", rl.Size())
	}
	// The oldest ("a") should be evicted; accessing it again
	// creates a fresh bucket (burst=1, so this call passes).
	if !rl.Allow("a") {
		t.Error("evicted key should be re-creatable as fresh bucket")
	}
}

func TestRateLimiter_LRUAccessRefreshes(t *testing.T) {
	// Accessing a key promotes it to MRU. With cap=2 and 3 keys,
	// the key that was accessed most recently before the overflow
	// is the one that survives.
	rl := NewRateLimiter(rate.Every(time.Second), 1, 2)
	rl.Allow("a")
	rl.Allow("b")
	// Promote "a" to MRU.
	rl.Allow("a")
	// Now insert "c" — "b" (LRU) should be evicted, "a" should
	// remain.
	rl.Allow("c")
	if rl.Size() != 2 {
		t.Errorf("size = %d, want 2", rl.Size())
	}
	// "a" should still be in the LRU (and not get a fresh burst).
	if rl.Allow("a") {
		t.Error("a should still be tracked (burst already used)")
	}
	// "b" was evicted — accessing it now creates a fresh bucket
	// (burst=1, so this passes).
	if !rl.Allow("b") {
		t.Error("b should have been evicted and re-created with fresh burst")
	}
}

func TestRateLimiter_ConcurrentAccess(t *testing.T) {
	// 100 goroutines hit Allow("k") concurrently. With burst=10,
	// we expect ~10 to be allowed and ~90 to be throttled (within
	// tolerance for refill races).
	rl := NewRateLimiter(rate.Every(time.Hour), 10, 100)
	const goroutines = 100

	var allowed, throttled atomic.Int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if rl.Allow("k") {
				allowed.Add(1)
			} else {
				throttled.Add(1)
			}
		}()
	}
	wg.Wait()

	if allowed.Load() < 9 || allowed.Load() > 11 {
		t.Errorf("allowed = %d, want ~10 (burst)", allowed.Load())
	}
	if throttled.Load() < 89 || throttled.Load() > 91 {
		t.Errorf("throttled = %d, want ~90", throttled.Load())
	}
}

func TestRateLimitMetrics_CountsAllow(t *testing.T) {
	DefaultRateLimitMetrics.Allowed.Store(0)
	DefaultRateLimitMetrics.Throttled.Store(0)

	rl := NewRateLimiter(rate.Every(time.Hour), 2, 100)
	rl.Allow("u1")
	rl.Allow("u1")
	rl.Allow("u1") // throttled

	snap := DefaultRateLimitMetrics.Snapshot()
	if snap.Allowed != 2 {
		t.Errorf("Allowed = %d, want 2", snap.Allowed)
	}
	if snap.Throttled != 1 {
		t.Errorf("Throttled = %d, want 1", snap.Throttled)
	}
}

func TestRateLimiter_ZeroCap(t *testing.T) {
	// cap=0 → "no cap" mode (unlimited LRU). Mostly there to document
	// the behavior; we just verify it doesn't panic.
	rl := NewRateLimiter(rate.Every(time.Second), 1, 0)
	for i := 0; i < 50; i++ {
		rl.Allow("k")
	}
	if rl.Size() < 1 {
		t.Error("zero-cap limiter should still track the key")
	}
}
