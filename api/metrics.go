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
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/cron"
	"github.com/yuterigele/openbook/lock"
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
		"\n" + rateLimitPromText() +
		"\n" + agentMetricsPromText() +
		"\n" + availabilityPromText() +
		"\n" + cron.DefaultTaskMetrics.PrometheusText()
	c.String(http.StatusOK, body)
}

func availabilityPromText() string {
	readOnly := 0
	if lock.IsReadOnly() {
		readOnly = 1
	}
	return fmt.Sprintf("# HELP openbook_redis_read_only Whether Redis safety mode has disabled business writes (1=true).\n"+
		"# TYPE openbook_redis_read_only gauge\n"+
		"openbook_redis_read_only %d\n"+
		"# HELP openbook_llm_runtime_fallbacks_total Total runtime LLM provider failovers.\n"+
		"# TYPE openbook_llm_runtime_fallbacks_total counter\n"+
		"openbook_llm_runtime_fallbacks_total %d\n", readOnly, chatmodel.RuntimeFallbackAttempts())
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

func agentMetricsPromText() string {
	snap := server.DefaultAgentMetrics.Snapshot()
	body := fmt.Sprintf(
		"# HELP openbook_agent_tasks_started_total Total Agent tasks started.\n"+
			"# TYPE openbook_agent_tasks_started_total counter\n"+
			"openbook_agent_tasks_started_total %d\n"+
			"# HELP openbook_agent_tasks_succeeded_total Total Agent tasks completed successfully.\n"+
			"# TYPE openbook_agent_tasks_succeeded_total counter\n"+
			"openbook_agent_tasks_succeeded_total %d\n"+
			"# HELP openbook_agent_tasks_failed_total Total Agent tasks that failed.\n"+
			"# TYPE openbook_agent_tasks_failed_total counter\n"+
			"openbook_agent_tasks_failed_total %d\n",
		snap.TasksStarted, snap.TasksSucceeded, snap.TasksFailed,
	)
	body += "# HELP openbook_agent_tool_calls_total Total Agent tool calls by tool name.\n" +
		"# TYPE openbook_agent_tool_calls_total counter\n" +
		"# HELP openbook_agent_tool_failures_total Total failed Agent tool calls by tool name.\n" +
		"# TYPE openbook_agent_tool_failures_total counter\n"
	for _, tool := range snap.Tools {
		label := strconv.Quote(tool.Name)
		body += fmt.Sprintf(
			"openbook_agent_tool_calls_total{tool=%s} %d\n"+
				"openbook_agent_tool_failures_total{tool=%s} %d\n",
			label, tool.Calls, label, tool.Failed,
		)
	}
	body += fmt.Sprintf(
		"# HELP openbook_agent_tool_success_rate Tool execution success rate for this process.\n"+
			"# TYPE openbook_agent_tool_success_rate gauge\n"+
			"openbook_agent_tool_success_rate %.6f\n"+
			"# HELP openbook_agent_tool_success_target Tool execution success-rate SLO target.\n"+
			"# TYPE openbook_agent_tool_success_target gauge\n"+
			"openbook_agent_tool_success_target %.6f\n"+
			"# HELP openbook_agent_tool_slo_met Whether the tool success-rate SLO is currently met after the minimum sample size.\n"+
			"# TYPE openbook_agent_tool_slo_met gauge\n"+
			"openbook_agent_tool_slo_met %d\n",
		snap.ToolSuccessRate, snap.ToolSuccessTarget, boolToInt(snap.ToolSLOMet),
	)
	return body
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
