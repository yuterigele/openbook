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

// Package pool implements a bounded worker pool for concurrent job
// execution.
//
// Why a hand-rolled pool (instead of `go func()` everywhere):
//   - Bounded concurrency: at most N goroutines run user code at once,
//     preventing accidental goroutine blow-up under traffic spikes.
//   - Backpressure: Submit() blocks when the queue is full, giving
//     upstream callers a chance to slow down instead of OOMing.
//   - Deterministic shutdown: Close() waits for in-flight jobs and stops
//     accepting new ones, so we never leak goroutines on restart.
//
// Use cases in this repo:
//   - server/parallel pre-checks (sensitive filter + intent classify +
//     history load) on each user message — see server.PreCheckPool.
//   - storage batch operations (e.g. bulk notify dispatch).
//
// What this pool is NOT:
//   - Not a replacement for errgroup (use errgroup for fan-out of N tasks
//     you want results from; use this pool for a long-lived worker
//     population processing a stream of jobs).
//   - Not a scheduler. No priorities, no deadlines, no fairness.
package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrPoolClosed is returned by Submit / SubmitCtx when the pool has been
// closed via Close().
var ErrPoolClosed = errors.New("pool closed")

// ErrQueueFull is returned by TrySubmit when the queue is at capacity.
// Callers should fall back to blocking Submit or shed load.
var ErrQueueFull = errors.New("queue full")

// Job is the unit of work submitted to the pool. The pool calls f() in
// one of its worker goroutines.
type Job func()

// Pool is a bounded worker pool with a fixed queue size. Zero value is
// unusable; always construct via New.
type Pool struct {
	size    int           // number of workers
	queue   chan Job      // buffered job queue
	wg      sync.WaitGroup // tracks in-flight workers
	closeMu sync.Mutex
	closed  bool
}

// New returns a started Pool with `size` workers and a queue that holds
// up to `queueSize` pending jobs.
//
// Sizing rules of thumb:
//   - size: 2-4x CPU count for I/O-bound work, 1x for CPU-bound.
//   - queueSize: 0 for strict backpressure (Submit blocks immediately),
//     larger for absorbing traffic spikes.
//
// Returns an error if size <= 0 or queueSize < 0.
func New(size, queueSize int) (*Pool, error) {
	if size <= 0 {
		return nil, fmt.Errorf("pool: size must be > 0, got %d", size)
	}
	if queueSize < 0 {
		return nil, fmt.Errorf("pool: queueSize must be >= 0, got %d", queueSize)
	}
	p := &Pool{
		size:  size,
		queue: make(chan Job, queueSize),
	}
	for i := 0; i < size; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p, nil
}

// worker is the goroutine body. It pops jobs from the queue and runs
// them with panic recovery so one bad job doesn't kill the worker.
func (p *Pool) worker() {
	defer p.wg.Done()
	for job := range p.queue {
		p.runJob(job)
	}
}

// runJob executes a job with panic recovery. A panicking job is logged
// via the package-level logger hook (set via SetPanicHook).
func (p *Pool) runJob(job Job) {
	defer func() {
		if r := recover(); r != nil {
			handlePanic(r)
		}
	}()
	job()
}

// Submit blocks until the job is accepted into the queue (or the pool
// is closed). Use this when you want backpressure.
func (p *Pool) Submit(job Job) error {
	if job == nil {
		return fmt.Errorf("pool: nil job")
	}
	p.closeMu.Lock()
	if p.closed {
		p.closeMu.Unlock()
		return ErrPoolClosed
	}
	p.closeMu.Unlock()
	p.queue <- job
	return nil
}

// TrySubmit is the non-blocking variant: returns ErrQueueFull immediately
// if the queue is at capacity. Use this when you'd rather drop the job
// than block.
func (p *Pool) TrySubmit(job Job) error {
	if job == nil {
		return fmt.Errorf("pool: nil job")
	}
	p.closeMu.Lock()
	if p.closed {
		p.closeMu.Unlock()
		return ErrPoolClosed
	}
	p.closeMu.Unlock()
	select {
	case p.queue <- job:
		return nil
	default:
		return ErrQueueFull
	}
}

// SubmitCtx is Submit + ctx cancellation. Returns ctx.Err() if the
// context fires before the job is queued.
func (p *Pool) SubmitCtx(ctx context.Context, job Job) error {
	if job == nil {
		return fmt.Errorf("pool: nil job")
	}
	p.closeMu.Lock()
	if p.closed {
		p.closeMu.Unlock()
		return ErrPoolClosed
	}
	p.closeMu.Unlock()
	select {
	case p.queue <- job:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops accepting new jobs and waits for in-flight ones to finish.
// Calling Close twice is a no-op.
func (p *Pool) Close() {
	p.closeMu.Lock()
	if p.closed {
		p.closeMu.Unlock()
		return
	}
	p.closed = true
	close(p.queue)
	p.closeMu.Unlock()
	p.wg.Wait()
}

// Size returns the number of workers (read-only).
func (p *Pool) Size() int { return p.size }

// QueueCap returns the queue capacity (read-only).
func (p *Pool) QueueCap() int { return cap(p.queue) }

// QueueLen returns the current queue depth (for monitoring).
func (p *Pool) QueueLen() int { return len(p.queue) }
