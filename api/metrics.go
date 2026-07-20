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
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/sensitive"
	"github.com/yuterigele/openbook/server"
)

// metricsHandler exposes the in-process observability counters
// (sensitive-word filter + LLM token usage + per-customer rate
// limiter) in Prometheus text exposition format (version 0.0.4).
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
// counters and serializes ~30 lines of text. Safe to scrape at
// 1Hz without any rate limiting.
func metricsHandler(_ context.Context, c *app.RequestContext) {
	c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	body := sensitive.DefaultMetrics.PrometheusText() +
		"\n" + chatmodel.DefaultUsageTracker.PrometheusText() +
		"\n" + rateLimitPromText()
	c.String(http.StatusOK, body)
}

// rateLimitPromText renders the per-customer rate-limiter counters
// in prom format. Lives here (not in server/) to keep all metrics
// output in one place for /metrics.
func rateLimitPromText() string {
	snap := server.DefaultRateLimitMetrics.Snapshot()
	return fmt.Sprintf(
		"# HELP openbook_ratelimit_allowed_total Total customer requests that passed the per-customer token-bucket rate limiter.\n"+
			"# TYPE openbook_ratelimit_allowed_total counter\n"+
			"openbook_ratelimit_allowed_total %d\n"+
			"# HELP openbook_ratelimit_throttled_total Total customer requests rejected by the per-customer token-bucket rate limiter (suspected abuse / bot).\n"+
			"# TYPE openbook_ratelimit_throttled_total counter\n"+
			"openbook_ratelimit_throttled_total %d\n"+
			"# HELP openbook_ratelimit_customer_throttled_total Requests rejected by the per-customer limit.\n"+
			"# TYPE openbook_ratelimit_customer_throttled_total counter\n"+
			"openbook_ratelimit_customer_throttled_total %d\n"+
			"# HELP openbook_ratelimit_global_throttled_total Requests rejected by the process-wide global limit.\n"+
			"# TYPE openbook_ratelimit_global_throttled_total counter\n"+
			"openbook_ratelimit_global_throttled_total %d\n"+
			"# HELP openbook_ratelimit_evicted_total Customer limiter entries evicted from the LRU cache.\n"+
			"# TYPE openbook_ratelimit_evicted_total counter\n"+
			"openbook_ratelimit_evicted_total %d\n"+
			"# HELP openbook_ratelimit_active_keys Current customer keys tracked by the local limiter.\n"+
			"# TYPE openbook_ratelimit_active_keys gauge\n"+
			"openbook_ratelimit_active_keys %d\n",
		snap.Allowed, snap.Throttled, snap.CustomerThrottled,
		snap.GlobalThrottled, snap.Evicted, snap.ActiveKeys,
	)
}
