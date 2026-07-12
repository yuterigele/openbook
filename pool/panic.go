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
	"log"
	"sync/atomic"
)

// PanicHook is the function called when a worker recovers from a panic.
// Default logs to the standard logger.
//
// Set this from main() to forward panics to your observability stack
// (e.g., Sentry / 飞书告警).
type panicHookFn func(any)

var PanicHook atomic.Pointer[panicHookFn]

func init() {
	// Default: log + count. Tests can override or replace.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	defaultHook := panicHookFn(defaultPanicHook)
	PanicHook.Store(&defaultHook)
}

var panicCount atomic.Uint64

func defaultPanicHook(r any) {
	panicCount.Add(1)
	log.Printf("[pool] worker recovered from panic: %v (total panics: %d)", r, panicCount.Load())
}

// handlePanic is called from worker() via defer/recover. It dispatches
// to the configured PanicHook (always set in init()).
func handlePanic(r any) {
	if hook := PanicHook.Load(); hook != nil {
		(*hook)(r)
	}
}

// PanicCount returns the total number of recovered panics since process
// start. Useful for /metrics or health checks.
func PanicCount() uint64 { return panicCount.Load() }
