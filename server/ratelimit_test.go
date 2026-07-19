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
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/yuterigele/openbook/mem"
	"github.com/yuterigele/openbook/wecom"
	"golang.org/x/time/rate"
)

func newTestRateLimiter(t *testing.T, r rate.Limit, burst, capacity int) *RateLimiter {
	t.Helper()
	rl, err := NewRateLimiter(r, burst, capacity)
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}
	return rl
}

func TestRateLimiter_FirstRequestAllowed(t *testing.T) {
	// Burst of 5, sustained 1/sec. First request from a brand-new
	// key must always succeed (it's a fresh bucket).
	rl := newTestRateLimiter(t, rate.Every(time.Second), 5, 100)
	if !rl.Allow("user-A") {
		t.Error("first request from a new key should be allowed")
	}
}

func TestRateLimiter_BurstExhaustion(t *testing.T) {
	// Burst of 3, sustained 100/sec (effectively infinite for this
	// test). The first 3 should pass; the 4th should be throttled.
	rl := newTestRateLimiter(t, rate.Every(10*time.Millisecond), 3, 100)
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
	rl := newTestRateLimiter(t, rate.Every(time.Second), 1, 100)

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
	rl := newTestRateLimiter(t, rate.Every(100*time.Millisecond), 1, 100)

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
	rl := newTestRateLimiter(t, rate.Every(time.Second), 1, 2)
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
	rl := newTestRateLimiter(t, rate.Every(time.Second), 1, 2)
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
	rl := newTestRateLimiter(t, rate.Every(time.Hour), 10, 100)
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
	rl := newTestRateLimiter(t, rate.Every(time.Hour), 2, 100)
	rl.Allow("u1")
	rl.Allow("u1")
	rl.Allow("u1") // throttled

	snap := rl.Metrics().Snapshot()
	if snap.Allowed != 2 {
		t.Errorf("Allowed = %d, want 2", snap.Allowed)
	}
	if snap.Throttled != 1 {
		t.Errorf("Throttled = %d, want 1", snap.Throttled)
	}
}

func TestRateLimitMetrics_AreIsolatedPerLimiter(t *testing.T) {
	a := newTestRateLimiter(t, rate.Every(time.Hour), 1, 10)
	b := newTestRateLimiter(t, rate.Every(time.Hour), 1, 10)
	a.Allow("user")
	a.Allow("user")
	if got := b.Metrics().Snapshot(); got.Allowed != 0 || got.Throttled != 0 {
		t.Fatalf("limiter B metrics were polluted by limiter A: %+v", got)
	}
}

func TestRateLimiter_InvalidCapacity(t *testing.T) {
	if _, err := NewRateLimiter(rate.Every(time.Second), 1, 0); err == nil {
		t.Fatal("capacity=0 should be rejected")
	}
}

func TestRateLimiter_WaitHonorsContext(t *testing.T) {
	rl := newTestRateLimiter(t, rate.Every(time.Hour), 1, 10)
	if err := rl.Wait(context.Background(), "user-A"); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := rl.Wait(ctx, "user-A"); err == nil {
		t.Fatal("Wait should return when context expires")
	}
}

func TestRateLimiter_EvictionMetrics(t *testing.T) {
	rl := newTestRateLimiter(t, rate.Every(time.Second), 1, 2)
	rl.Allow("a")
	rl.Allow("b")
	rl.Allow("c")
	snap := rl.Metrics().Snapshot()
	if snap.Evicted != 1 || snap.ActiveKeys != 2 {
		t.Fatalf("metrics = %+v, want Evicted=1 ActiveKeys=2", snap)
	}
}

func TestLayeredRateLimiter_DistinguishesReasons(t *testing.T) {
	rl, err := NewLayeredRateLimiter(rate.Every(time.Hour), 1, 100, rate.Every(time.Hour), 2)
	if err != nil {
		t.Fatalf("NewLayeredRateLimiter: %v", err)
	}
	if got := rl.AllowDecision("user-A"); !got.Allowed {
		t.Fatalf("first decision = %+v", got)
	}
	if got := rl.AllowDecision("user-A"); got.Reason != RateLimitReasonCustomer {
		t.Fatalf("customer decision = %+v", got)
	}
	if got := rl.AllowDecision("user-B"); got.Reason != RateLimitReasonGlobal {
		t.Fatalf("global decision = %+v", got)
	}
	snap := rl.Metrics().Snapshot()
	if snap.CustomerThrottled != 1 || snap.GlobalThrottled != 1 {
		t.Fatalf("metrics = %+v", snap)
	}
}

func TestLayeredRateLimiter_GlobalLimitConcurrent(t *testing.T) {
	rl, err := NewLayeredRateLimiter(rate.Limit(10_000), 1, 200, rate.Every(time.Hour), 20)
	if err != nil {
		t.Fatalf("NewLayeredRateLimiter: %v", err)
	}
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if rl.AllowDecision(fmt.Sprintf("user-%d", n)).Allowed {
				allowed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := allowed.Load(); got != 20 {
		t.Fatalf("allowed = %d, want global burst 20", got)
	}
}

func TestCustomerRateLimitKey_IsScopedByShop(t *testing.T) {
	shopA := customerRateLimitKey("shop-a", "customer-1")
	shopB := customerRateLimitKey("shop-b", "customer-1")
	if shopA == shopB {
		t.Fatal("the same customer in different shops must use independent rate-limit keys")
	}
}

func TestCustomerRateLimitKey_AvoidsAmbiguousConcatenation(t *testing.T) {
	a := customerRateLimitKey("ab", "c")
	b := customerRateLimitKey("a", "bc")
	if a == b {
		t.Fatal("length-prefixed keys must not collide")
	}
}

func TestHandleWeComMessage_RateLimitSkipsAgent(t *testing.T) {
	store, err := mem.NewStore[*schema.Message](t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	srv := New(Config[*schema.Message]{
		Agent:       simpleReplyAgent("ok"),
		Store:       store,
		RateLimiter: newTestRateLimiter(t, rate.Every(time.Hour), 1, 100),
	})
	sender := &fakeReplySender{}
	msg := &wecom.MessageXML{
		FromUserName: "customer-1",
		OpenKfId:     "kf-1",
		MsgType:      "text",
		Content:      "我要预约",
	}

	srv.handleWeComMessageWithOpenKfID(context.Background(), sender, msg, msg.OpenKfId, "shop-1")
	srv.handleWeComMessageWithOpenKfID(context.Background(), sender, msg, msg.OpenKfId, "shop-1")

	if sender.kfCalls != 2 {
		t.Fatalf("expected an Agent reply and a rate-limit reply, got %d sends", sender.kfCalls)
	}
	if sender.kfLastContent != rateLimitReply {
		t.Fatalf("last reply = %q, want %q", sender.kfLastContent, rateLimitReply)
	}
	sess, err := store.GetOrCreate("wecom_shop-1_customer-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if got := len(sess.GetMessages()); got != 2 {
		t.Fatalf("rate-limited request reached Agent/history: got %d messages, want 2", got)
	}
}
