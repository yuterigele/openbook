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

// ratelimit.go — per-customer request rate limiting.
//
// The motivation: a single misbehaving customer (or worse, an
// attacker who spoofs a wecom OpenID) can flood the agent endpoint
// with messages. Each message drives an LLM call worth ¥0.001–0.1
// of token cost. Without rate limiting, one customer can rack up
// ¥10,000+ in DeepSeek charges in a few minutes.
//
// Design:
//   - Token-bucket per customer (golang.org/x/time/rate.Limiter)
//   - LRU cache (container/list + map) caps the number of
//     concurrent limiters — a million unique OpenIDs would OOM
//     otherwise.
//   - Allowed = a small burst + sustained rate. e.g. 5 burst,
//     1/sec sustained. Enough for a real human to chat naturally
//     (1 msg / few sec), not enough to spam.
//
// What this catches:
//   - Same OpenID firing 50+ messages in 10 seconds → 429-equivalent
//     reply ("请稍后再试") + zero LLM cost
//   - Bot traffic (constant stream) → sustained rate blocks
//   - Accidental message loop (client bug) → burst absorbs,
//     sustained rate still limits
//
// What this does NOT catch (out of scope):
//   - Per-shop total token budget (defer to a future "plan billing"
//     layer; needs DB schema for daily-rolled-up counters)
//   - IP-level limits (a single attacker with many OpenIDs can
//     still spread load; wecom OpenID is the natural key here)
//   - LLM output abuse (caller asks LLM to do something harmful —
//     sensitive_check + RAG Workflow filter that upstream)

import (
	"container/list"
	"context"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter is a thread-safe per-key token-bucket rate limiter
// with a bounded LRU cache.
//
// Construction:
//
//	rl := NewRateLimiter(rate.Every(time.Second), 5, 10_000)
//
// where rate.Every(time.Second) is the sustained rate (1 msg/sec)
// and 5 is the burst (5 msgs in a single instant). The 10_000 is
// the LRU size cap — after that, the least-recently-used
// customer's limiter is evicted (and a fresh one created on
// their next request, which is the right behavior for cold
// customers).
type RateLimiter struct {
	rate  rate.Limit
	burst int
	cap   int

	mu  sync.Mutex
	ll  *list.List               // *list.Element values point to entry
	idx map[string]*list.Element // openid → element
}

// entry is the LRU node. We store the rate.Limiter by value
// (it's a small struct) so eviction doesn't need to coordinate
// with the limiter's own goroutine.
type entry struct {
	key   string
	limit *rate.Limiter
}

// NewRateLimiter returns a RateLimiter with the given sustained
// rate, burst size, and LRU capacity. cap == 0 means "no cap" —
// NOT recommended for long-running processes (memory grows
// unboundedly with unique keys).
func NewRateLimiter(r rate.Limit, burst, cap int) *RateLimiter {
	return &RateLimiter{
		rate:  r,
		burst: burst,
		cap:   cap,
		idx:   make(map[string]*list.Element, cap),
		ll:    list.New(),
	}
}

// Allow returns true if a request from key is permitted right now.
// If true, the call also consumes one token from the bucket. If
// false, the call should be rejected with "请稍后再试".
//
// The first request from a new key gets a fresh burst-sized
// bucket, which is the desired behavior: a real customer who
// just opened the chat should be able to send their first
// message without waiting.
//
// Allow is safe for concurrent use. The DefaultRateLimitMetrics
// counter is incremented (Allowed or Throttled) on every call.
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	el, ok := rl.idx[key]
	if !ok {
		// Evict LRU if at capacity.
		if rl.cap > 0 && rl.ll.Len() >= rl.cap {
			oldest := rl.ll.Back()
			if oldest != nil {
				rl.ll.Remove(oldest)
				delete(rl.idx, oldest.Value.(*entry).key)
			}
		}
		// Insert new entry at the front (most recently used).
		el = rl.ll.PushFront(&entry{
			key:   key,
			limit: rate.NewLimiter(rl.rate, rl.burst),
		})
		rl.idx[key] = el
	}
	// Move-to-front on access (true LRU semantics).
	rl.ll.MoveToFront(el)
	limiter := el.Value.(*entry).limit
	rl.mu.Unlock()

	ok = limiter.Allow()
	if ok {
		DefaultRateLimitMetrics.Allowed.Add(1)
	} else {
		DefaultRateLimitMetrics.Throttled.Add(1)
	}
	return ok
}

// Wait is the blocking variant: returns when a token is available,
// or when ctx is canceled. Used by code paths that want to queue
// rather than reject.
//
// We don't use this in the agent main flow (we reject, not queue —
// queuing LLM requests ties up server resources and creates a
// thundering-herd when the rate recovers). It's here for callers
// that prefer the alternative.
func (rl *RateLimiter) Wait(key string) {
	rl.mu.Lock()
	el, ok := rl.idx[key]
	if !ok {
		if rl.cap > 0 && rl.ll.Len() >= rl.cap {
			oldest := rl.ll.Back()
			if oldest != nil {
				rl.ll.Remove(oldest)
				delete(rl.idx, oldest.Value.(*entry).key)
			}
		}
		el = rl.ll.PushFront(&entry{
			key:   key,
			limit: rate.NewLimiter(rl.rate, rl.burst),
		})
		rl.idx[key] = el
	}
	rl.ll.MoveToFront(el)
	limiter := el.Value.(*entry).limit
	rl.mu.Unlock()

	_ = limiter.Wait(context.Background()) //nolint:staticcheck
}

// Size returns the current number of tracked keys. Useful for
// /metrics and tests.
func (rl *RateLimiter) Size() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.ll.Len()
}

// ----- package-level wiring -----

// DefaultRateLimiter is the package-level limiter used by the
// wecom callback handler. Tuned for a single store-front with
// normal traffic: 1 msg/sec sustained, 5 burst.
//
// Production tuning: a busy salon gets 50-200 customers / day, each
// sending 1-3 messages / visit. 1 msg/sec per customer is way
// over the realistic ceiling; the limiter mostly sits idle and
// only kicks in for spam / bugs. Increase RPS only if you have
// evidence a single human is being throttled legitimately.
var DefaultRateLimiter = NewRateLimiter(rate.Every(time.Second), 5, 10_000)

// RateLimitMetrics is a tiny in-memory counter for /metrics. The
// counter is incremented on every Allow() call so the dashboard
// can show "rate-limited requests per minute" — a clean signal
// for "is anyone being abusive right now".
//
// Fields are exported so the api package can render them in the
// /metrics endpoint without an extra accessor.
type RateLimitMetrics struct {
	Allowed   atomic.Int64
	Throttled atomic.Int64
}

// DefaultRateLimitMetrics is the package-level singleton.
var DefaultRateLimitMetrics = &RateLimitMetrics{}

// RateLimitSnapshot is a per-field-consistent snapshot of the
// counters. Returned by Snapshot for the /metrics handler.
type RateLimitSnapshot struct {
	Allowed   int64
	Throttled int64
}

// Snapshot returns the current counter values. Safe for
// concurrent use.
func (m *RateLimitMetrics) Snapshot() RateLimitSnapshot {
	return RateLimitSnapshot{
		Allowed:   m.Allowed.Load(),
		Throttled: m.Throttled.Load(),
	}
}
