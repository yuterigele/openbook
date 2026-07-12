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

package pool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNew_InvalidSize(t *testing.T) {
	if _, err := New(0, 10); err == nil {
		t.Error("New(0, 10) should error")
	}
	if _, err := New(-1, 10); err == nil {
		t.Error("New(-1, 10) should error")
	}
}

func TestNew_InvalidQueueSize(t *testing.T) {
	if _, err := New(2, -1); err == nil {
		t.Error("New(2, -1) should error")
	}
	// 0 is allowed (strict backpressure)
	if _, err := New(2, 0); err != nil {
		t.Errorf("New(2, 0) should be allowed, got %v", err)
	}
}

func TestSubmit_BasicRun(t *testing.T) {
	p, err := New(2, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	var ran atomic.Int32
	for i := 0; i < 5; i++ {
		if err := p.Submit(func() { ran.Add(1) }); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	// Wait for all jobs to finish.
	deadline := time.Now().Add(2 * time.Second)
	for ran.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ran.Load() != 5 {
		t.Errorf("ran=%d, want 5", ran.Load())
	}
}

func TestSubmit_NilJob(t *testing.T) {
	p, _ := New(2, 10)
	defer p.Close()
	if err := p.Submit(nil); err == nil {
		t.Error("Submit(nil) should error")
	}
}

func TestTrySubmit_QueueFull(t *testing.T) {
	// Setup: 1 worker, queue size 1. Push a slow job so the worker
	// takes it, then push another so it sits in the queue. The queue
	// is now full, so TrySubmit must return ErrQueueFull.
	p, _ := New(1, 1)

	workerStarted := make(chan struct{})
	if err := p.Submit(func() {
		close(workerStarted)
		time.Sleep(200 * time.Millisecond)
	}); err != nil {
		t.Fatal(err)
	}
	<-workerStarted // worker has picked up the first job, so the queue is empty

	// Fill the queue with a quick job.
	if err := p.Submit(func() {}); err != nil {
		t.Fatal(err)
	}

	// Queue is now full (size 1, both queued). TrySubmit should fail.
	if err := p.TrySubmit(func() {}); !errors.Is(err, ErrQueueFull) {
		t.Errorf("TrySubmit: got %v, want ErrQueueFull", err)
	}
	p.Close() // wait for the slow job to finish
}

func TestSubmitCtx_Cancel(t *testing.T) {
	p, _ := New(1, 0)
	defer p.Close()

	// Block the worker.
	hold := make(chan struct{})
	p.Submit(func() { <-hold })
	defer close(hold)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if err := p.SubmitCtx(ctx, func() {}); !errors.Is(err, context.Canceled) {
		t.Errorf("SubmitCtx canceled: got %v, want context.Canceled", err)
	}
}

func TestClose_StopsAccepting(t *testing.T) {
	p, _ := New(2, 10)
	p.Close()
	if err := p.Submit(func() {}); !errors.Is(err, ErrPoolClosed) {
		t.Errorf("after Close, Submit: got %v, want ErrPoolClosed", err)
	}
}

func TestClose_WaitsForInFlight(t *testing.T) {
	p, _ := New(1, 10)
	hold := make(chan struct{})
	done := make(chan struct{})
	p.Submit(func() {
		<-hold
		close(done)
	})
	closeDone := make(chan struct{})
	go func() {
		p.Close()
		close(closeDone)
	}()
	// Close should be blocked on the in-flight job.
	select {
	case <-closeDone:
		t.Error("Close returned before in-flight job finished")
	case <-time.After(50 * time.Millisecond):
		// expected: still blocked
	}
	close(hold)
	<-done
	<-closeDone
}

func TestClose_TwiceSafe(t *testing.T) {
	p, _ := New(2, 10)
	p.Close()
	p.Close() // should not panic
}

func TestPanicRecovery(t *testing.T) {
	p, _ := New(2, 10)
	defer p.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	p.Submit(func() {
		defer wg.Done()
		panic("test panic")
	})
	wg.Wait()
	// Give the recovery hook a moment to run.
	time.Sleep(20 * time.Millisecond)
	if c := PanicCount(); c == 0 {
		t.Error("expected panic to be recorded, count = 0")
	}
}

func TestPoolAfterPanic_KeepsRunning(t *testing.T) {
	p, _ := New(1, 10)
	defer p.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	p.Submit(func() { defer wg.Done(); panic("boom") })
	p.Submit(func() { defer wg.Done() })
	wg.Wait()
	// If the worker died, the second job would never run.
}

func TestSizeAndQueueCap(t *testing.T) {
	p, _ := New(3, 5)
	defer p.Close()
	if p.Size() != 3 {
		t.Errorf("Size=%d, want 3", p.Size())
	}
	if p.QueueCap() != 5 {
		t.Errorf("QueueCap=%d, want 5", p.QueueCap())
	}
}
