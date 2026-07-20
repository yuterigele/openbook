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

// ratelimit.go —— 按顾客维度限制请求频率。
//
// 设计背景：单个异常顾客（或者伪造企业微信 OpenID 的攻击者）可能持续向
// Agent 接口灌入消息。每条消息都会触发一次 LLM 调用，产生 token 成本。
// 如果没有限流，单个顾客可能在几分钟内产生高额模型费用。
//
// 设计方案：
//   - 每个顾客独立使用一个令牌桶（golang.org/x/time/rate.Limiter）。
//   - 使用 LRU 缓存（container/list + map）限制令牌桶数量；否则大量不同
//     OpenID 会持续占用内存，最终可能导致 OOM。
//   - 允许少量突发请求，同时限制持续速率。例如突发容量为 5、持续速率为
//     每秒 1 条，足够真人自然对话，但无法持续刷消息。
//
// 能够拦截：
//   - 同一 OpenID 在短时间内发送大量消息：返回等价于 HTTP 429 的提示，
//     且不产生 LLM 调用成本。
//   - 机器人持续发送消息：由持续速率限制拦截。
//   - 客户端缺陷导致的消息循环：突发容量吸收少量请求，持续速率继续限流。
//
// 当前不负责（超出本地限流器范围）：
//   - 每个门店的 token 总预算：应由套餐计费层负责，并维护按日汇总的计数。
//   - IP 级限流：攻击者仍可使用多个 OpenID 分散请求；本层以企业微信
//     OpenID 作为顾客标识。
//   - LLM 输出内容治理：由 sensitive_check 及上游 RAG 工作流过滤负责。

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

const rateLimitReply = "请求过于频繁，请稍后再试。"

// customerRateLimitKey 将顾客身份限制在指定来源或门店范围内。同一外部联系人
// 可能联系多个门店，因此每个租户必须使用独立令牌桶。长度前缀可以避免直接
// 拼接字符串产生歧义和键冲突。
func customerRateLimitKey(scope, customerID string) string {
	scope = strings.TrimSpace(scope)
	customerID = strings.TrimSpace(customerID)
	return fmt.Sprintf("%d:%s:%s", len(scope), scope, customerID)
}

// RateLimiter 是线程安全的按键令牌桶限流器，内部使用有容量上限的 LRU 缓存。
//
// 构造示例：
//
//	rl, err := NewRateLimiter(rate.Every(time.Second), 5, 10_000)
//
// rate.Every(time.Second) 表示持续速率为每秒 1 条，5 表示瞬间最多允许
// 5 条突发消息，10_000 表示 LRU 容量上限。超过上限后会淘汰最久未使用
// 顾客的令牌桶；该顾客下次请求时会创建一个新的令牌桶。
type RateLimiter struct {
	rate    rate.Limit
	burst   int
	cap     int
	global  *rate.Limiter
	metrics *RateLimitMetrics

	mu  sync.Mutex
	ll  *list.List               // *list.Element 的值指向 entry
	idx map[string]*list.Element // OpenID → LRU 节点
}

// RateLimitReason 表示请求被哪一层限流，用于日志、指标和调用方决策。
type RateLimitReason string

const (
	RateLimitReasonNone     RateLimitReason = ""
	RateLimitReasonCustomer RateLimitReason = "customer_limit"
	RateLimitReasonGlobal   RateLimitReason = "global_limit"
)

// RateLimitDecision 是一次非阻塞限流判断的结果。
type RateLimitDecision struct {
	Allowed bool
	Reason  RateLimitReason
}

// entry 是 LRU 节点。节点保存限流器指针，淘汰节点时无需与其他 goroutine
// 额外协调；rate.Limiter 本身支持并发调用。
type entry struct {
	key   string
	limit *rate.Limiter
}

// NewRateLimiter 根据持续速率、突发容量和 LRU 容量创建限流器。
// 为避免不同键导致内存无限增长，cap 必须大于 0；配置非法时返回错误。
func NewRateLimiter(r rate.Limit, burst, cap int) (*RateLimiter, error) {
	return newRateLimiter(r, burst, cap, 0, 0)
}

// NewLayeredRateLimiter 创建“全局 + 每顾客”两级限流器。请求必须同时通过
// 全局令牌桶和对应顾客的令牌桶，任意一层拒绝都不会进入 Agent。
func NewLayeredRateLimiter(customerRate rate.Limit, customerBurst, cap int, globalRate rate.Limit, globalBurst int) (*RateLimiter, error) {
	return newRateLimiter(customerRate, customerBurst, cap, globalRate, globalBurst)
}

func newRateLimiter(r rate.Limit, burst, cap int, globalRate rate.Limit, globalBurst int) (*RateLimiter, error) {
	if r <= 0 {
		return nil, errors.New("每秒持续速率配置错误")
	}
	if burst <= 0 {
		return nil, errors.New("突发容量配置错误")
	}
	if cap <= 0 {
		return nil, errors.New("LRU 最大容量配置错误")
	}
	if globalRate < 0 || globalBurst < 0 {
		return nil, errors.New("全局持续速率和突发容量不能为负数")
	}
	if (globalRate <= 0) != (globalBurst <= 0) {
		return nil, errors.New("全局持续速率和突发容量必须同时配置")
	}
	rl := &RateLimiter{
		rate:    r,
		burst:   burst,
		cap:     cap,
		idx:     make(map[string]*list.Element, cap),
		ll:      list.New(),
		metrics: &RateLimitMetrics{},
	}
	if globalRate > 0 {
		rl.global = rate.NewLimiter(globalRate, globalBurst)
	}
	return rl, nil
}

// Allow 判断指定键的请求当前是否可以通过。返回 true 时会从令牌桶消费一个
// 令牌；返回 false 时，调用方应拒绝请求并提示顾客稍后再试。
//
// 新键首次请求会获得一个装满突发令牌的新桶，因此刚进入会话的真实顾客无需
// 等待即可发送第一条消息。
//
// Allow 支持并发调用。每次调用都会更新当前限流器实例自己的指标，不会污染
// 默认实例或其他测试实例的统计。
func (rl *RateLimiter) Allow(key string) bool {
	return rl.AllowDecision(key).Allowed
}

// AllowDecision 执行两级非阻塞判断，并明确返回被拒绝的层级。
func (rl *RateLimiter) AllowDecision(key string) RateLimitDecision {
	if rl.global != nil && !rl.global.Allow() {
		rl.metrics.Throttled.Add(1)
		rl.metrics.GlobalThrottled.Add(1)
		return RateLimitDecision{Reason: RateLimitReasonGlobal}
	}

	limiter := rl.limiterFor(key)
	if !limiter.Allow() {
		rl.metrics.Throttled.Add(1)
		rl.metrics.CustomerThrottled.Add(1)
		return RateLimitDecision{Reason: RateLimitReasonCustomer}
	}
	rl.metrics.Allowed.Add(1)
	return RateLimitDecision{Allowed: true}
}

func (rl *RateLimiter) limiterFor(key string) *rate.Limiter {
	rl.mu.Lock()
	el, ok := rl.idx[key]
	if !ok {
		// 达到容量上限时淘汰最久未使用的节点。
		if rl.cap > 0 && rl.ll.Len() >= rl.cap {
			oldest := rl.ll.Back()
			if oldest != nil {
				rl.ll.Remove(oldest)
				delete(rl.idx, oldest.Value.(*entry).key)
				rl.metrics.Evicted.Add(1)
			}
		}
		// 在链表头部插入新节点（最近使用）。
		el = rl.ll.PushFront(&entry{
			key:   key,
			limit: rate.NewLimiter(rl.rate, rl.burst),
		})
		rl.idx[key] = el
		rl.metrics.ActiveKeys.Store(int64(rl.ll.Len()))
	}
	// 每次访问后移到链表头部，保持严格的 LRU 语义。
	rl.ll.MoveToFront(el)
	limiter := el.Value.(*entry).limit
	rl.mu.Unlock()
	return limiter
}

// Wait 是阻塞式版本：持续等待，直到获得可用令牌。适用于希望排队而非直接
// 拒绝请求的调用路径。
//
// Agent 主流程不使用该方法，因为排队的 LLM 请求会长期占用服务端资源，并在
// 令牌恢复时造成惊群；主流程选择立即拒绝。该方法仅保留给确实需要排队的调用方。
func (rl *RateLimiter) Wait(ctx context.Context, key string) error {
	if rl.global != nil {
		if err := rl.global.Wait(ctx); err != nil {
			return err
		}
	}
	if err := rl.limiterFor(key).Wait(ctx); err != nil {
		return err
	}
	rl.metrics.Allowed.Add(1)
	return nil
}

// Size 返回当前跟踪的键数量，可用于监控指标和测试。
func (rl *RateLimiter) Size() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.ll.Len()
}

// ----- 包级默认实例与指标 -----

// DefaultRateLimiter 是企业微信回调等入口默认使用的包级限流器。参数面向普通
// 单店流量设置：每顾客持续速率每秒 1 条、突发容量 5 条；全局持续速率
// 每秒 100 条、突发容量 200 条。
//
// 生产调优参考：繁忙门店每天约有 50～200 位顾客，每位顾客每次通常发送
// 1～3 条消息。每位顾客每秒 1 条已经远高于正常真人频率，因此该限流器通常
// 不会介入，只会拦截刷消息或客户端异常。只有确认真实顾客被误限流后，才应
// 提高每秒请求数。
var DefaultRateLimiter = mustNewLayeredRateLimiter(
	rate.Every(time.Second), 5, 10_000,
	rate.Limit(100), 200,
)

// RateLimitMetrics 是供 /metrics 使用的轻量级内存计数器。每次调用 Allow 都会
// 更新计数，使监控面板能够展示每分钟限流请求数，用于识别当前是否存在异常流量。
//
// 字段保持导出，方便 api 包直接在 /metrics 端点输出，无需额外访问方法。
type RateLimitMetrics struct {
	Allowed           atomic.Int64
	Throttled         atomic.Int64
	CustomerThrottled atomic.Int64
	GlobalThrottled   atomic.Int64
	Evicted           atomic.Int64
	ActiveKeys        atomic.Int64
}

// Metrics 返回当前限流器独立拥有的指标实例。
func (rl *RateLimiter) Metrics() *RateLimitMetrics { return rl.metrics }

// DefaultRateLimitMetrics 仅指向默认限流器的指标，不再接收测试或自定义实例数据。
var DefaultRateLimitMetrics = DefaultRateLimiter.Metrics()

// RateLimitSnapshot 是限流计数器的字段快照，由 Snapshot 返回给指标处理器。
type RateLimitSnapshot struct {
	Allowed           int64
	Throttled         int64
	CustomerThrottled int64
	GlobalThrottled   int64
	Evicted           int64
	ActiveKeys        int64
}

// Snapshot 返回当前计数，支持并发调用。
func (m *RateLimitMetrics) Snapshot() RateLimitSnapshot {
	return RateLimitSnapshot{
		Allowed:           m.Allowed.Load(),
		Throttled:         m.Throttled.Load(),
		CustomerThrottled: m.CustomerThrottled.Load(),
		GlobalThrottled:   m.GlobalThrottled.Load(),
		Evicted:           m.Evicted.Load(),
		ActiveKeys:        m.ActiveKeys.Load(),
	}
}

func mustNewLayeredRateLimiter(customerRate rate.Limit, customerBurst, cap int, globalRate rate.Limit, globalBurst int) *RateLimiter {
	rl, err := NewLayeredRateLimiter(customerRate, customerBurst, cap, globalRate, globalBurst)
	if err != nil {
		panic(err)
	}
	return rl
}
