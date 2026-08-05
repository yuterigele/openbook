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
	// 突发容量为 5、持续速率为每秒 1 条；新键的首次请求应始终通过。
	rl := newTestRateLimiter(t, rate.Every(time.Second), 5, 100)
	if !rl.Allow("user-A") {
		t.Error("first request from a new key should be allowed")
	}
}

func TestRateLimiter_BurstExhaustion(t *testing.T) {
	// 突发容量为 3、持续速率为每秒 100 条；前三次通过，第四次应被限流。
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
	// user-A 的突发请求不影响 user-B。
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
	// 突发容量为 1、持续速率为每秒 10 条；消耗后等待 200 毫秒应补充约 2 个令牌。
	rl := newTestRateLimiter(t, rate.Every(100*time.Millisecond), 1, 100)

	if !rl.Allow("user-A") {
		t.Fatal("first request should pass")
	}
	if rl.Allow("user-A") {
		t.Fatal("burst exhausted, should throttle")
	}
	time.Sleep(150 * time.Millisecond) // 约补充 1.5 个令牌。
	if !rl.Allow("user-A") {
		t.Error("after refill, request should pass")
	}
}

func TestRateLimiter_LRUEviction(t *testing.T) {
	// 容量为 2：填入两个键后再加入第三个键，应淘汰一个键。
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
	// 最早的键“a”应被淘汰；重新创建时不得恢复突发额度。
	if rl.Allow("a") {
		t.Error("evicted key must restart cold instead of receiving a fresh burst")
	}
}

func TestRateLimiter_LRUAccessRefreshes(t *testing.T) {
	// 访问键会将其提升为最近使用项。容量为 2 时加入第三个键，应保留最近访问的键。
	rl := newTestRateLimiter(t, rate.Every(time.Second), 1, 2)
	rl.Allow("a")
	rl.Allow("b")
	// 将“a”提升为最近使用项。
	rl.Allow("a")
	// 插入“c”后，最久未使用的“b”应被淘汰，“a”应保留。
	rl.Allow("c")
	if rl.Size() != 2 {
		t.Errorf("size = %d, want 2", rl.Size())
	}
	// “a”仍应在 LRU 中，且不得获得新的突发额度。
	if rl.Allow("a") {
		t.Error("a should still be tracked (burst already used)")
	}
	// “b”已被淘汰，重新创建时不得获得新的突发额度。
	if rl.Allow("b") {
		t.Error("b should restart cold after eviction")
	}
}

func TestRateLimiter_EvictedKeyDoesNotRegainBurst(t *testing.T) {
	rl := newTestRateLimiter(t, rate.Every(time.Hour), 5, 1)
	for i := 0; i < 5; i++ {
		if !rl.Allow("a") {
			t.Fatalf("a request %d should consume its initial burst", i+1)
		}
	}
	if rl.Allow("a") {
		t.Fatal("a burst should be exhausted")
	}
	if !rl.Allow("b") { // 淘汰 a。
		t.Fatal("fresh key b should receive its initial burst")
	}
	if rl.Allow("a") { // 从 a 的淘汰标记重建。
		t.Fatal("evicted key a must not regain a burst")
	}
}

func TestRateLimiter_ConcurrentAccess(t *testing.T) {
	// 100 个 goroutine 并发调用 Allow("k")；突发容量为 10 时，预期约 10 次通过、
	// 约 90 次被限流（允许令牌补充竞争带来的误差）。
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
	rl.Allow("u1") // 被限流。

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
