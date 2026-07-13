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

package api

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/yuterigele/openbook/sensitive"
)

// metricsHandler exposes the sensitive-word filter's in-process
// counters in Prometheus text exposition format (version 0.0.4).
//
// Mounted at /metrics (unauthenticated by design — that's the
// standard prometheus scrape path). In production this endpoint
// should be protected by one of:
//
//   - Reverse-proxy IP allowlist (nginx / caddy / cloud LB rule)
//   - Internal-only listener (run a second hertz instance on
//     127.0.0.1:9090 just for /metrics)
//   - Bearer-token middleware (set METRICS_TOKEN in env and check
//     Authorization header) — left as a future enhancement.
//
// The endpoint is cheap to call: it reads a small set of atomic
// counters and serializes ~10 lines of text. Safe to scrape at
// 1Hz without any rate limiting.
func metricsHandler(_ context.Context, c *app.RequestContext) {
	c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	c.String(http.StatusOK, sensitive.DefaultMetrics.PrometheusText())
}
