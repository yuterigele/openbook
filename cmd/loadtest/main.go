// Command loadtest simulates concurrent inbound traffic against OpenBook's
// production rate-limiter configuration. It never starts the HTTP server,
// calls an LLM, or writes business data, so it is safe to run locally.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yuterigele/openbook/server"
	"golang.org/x/time/rate"
)

type report struct {
	Requests              int64   `json:"requests"`
	Concurrency           int     `json:"concurrency"`
	Customers             int     `json:"customers"`
	Allowed               int64   `json:"allowed"`
	Throttled             int64   `json:"throttled"`
	GlobalRejected        int64   `json:"global_rejected"`
	CustomerRejected      int64   `json:"customer_rejected"`
	TimedOut              int64   `json:"timed_out"`
	RequestTimeoutMS      int64   `json:"request_timeout_ms"`
	ElapsedMS             float64 `json:"elapsed_ms"`
	RPS                   float64 `json:"rps"`
	P50US                 int64   `json:"p50_us"`
	P95US                 int64   `json:"p95_us"`
	P99US                 int64   `json:"p99_us"`
	BaselineInputTokens   int64   `json:"baseline_input_tokens"`
	BaselineOutputTokens  int64   `json:"baseline_output_tokens"`
	OptimizedInputTokens  int64   `json:"optimized_input_tokens"`
	OptimizedOutputTokens int64   `json:"optimized_output_tokens"`
	BaselineTokens        int64   `json:"baseline_tokens"`
	OptimizedTokens       int64   `json:"optimized_tokens"`
	SavedTokens           int64   `json:"saved_tokens"`
	SavedPercent          float64 `json:"saved_percent"`
	BaselineCost          float64 `json:"baseline_cost_cny"`
	OptimizedCost         float64 `json:"optimized_cost_cny"`
	SavedCost             float64 `json:"saved_cost_cny"`
}

func main() {
	requests := flag.Int("requests", 10000, "total simulated requests")
	concurrency := flag.Int("concurrency", 100, "simultaneous workers")
	customers := flag.Int("customers", 1000, "distinct customers, distributed round-robin")
	inputTokens := flag.Int64("input-tokens", 600, "estimated input tokens for each LLM call")
	outputTokens := flag.Int64("output-tokens", 200, "estimated output tokens for each LLM call")
	inputCNYPer1K := flag.Float64("input-cny-per-1k", 0.001, "estimated CNY price per 1,000 input tokens")
	outputCNYPer1K := flag.Float64("output-cny-per-1k", 0.001, "estimated CNY price per 1,000 output tokens")
	requestTimeout := flag.Duration("request-timeout", 3*time.Second, "maximum time a simulated request may occupy a worker")
	simulatedWork := flag.Duration("simulated-work", 0, "optional per-request processing delay, used only to verify timeout handling")
	jsonOut := flag.Bool("json", false, "emit JSON only")
	flag.Parse()
	if *requests <= 0 || *concurrency <= 0 || *customers <= 0 || *inputTokens < 0 || *outputTokens < 0 || *inputCNYPer1K < 0 || *outputCNYPer1K < 0 || *requestTimeout <= 0 || *simulatedWork < 0 {
		fmt.Fprintln(os.Stderr, "requests, concurrency, customers and request-timeout must be positive; token, price and simulated-work values cannot be negative")
		os.Exit(2)
	}
	if *concurrency > *requests {
		*concurrency = *requests
	}

	// Match server.DefaultRateLimiter: 1 request/s and burst 5 per customer;
	// process-wide 100 request/s and burst 200.
	rl, err := server.NewLayeredRateLimiter(rate.Every(time.Second), 5, 10_000, rate.Limit(100), 200)
	if err != nil {
		panic(err)
	}
	latencies := make([]int64, *requests)
	var next, allowed, globalRejected, customerRejected, timedOut atomic.Int64
	start := time.Now()
	var wg sync.WaitGroup
	for worker := 0; worker < *concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := next.Add(1) - 1
				if i >= int64(*requests) {
					return
				}
				before := time.Now()
				requestCtx, cancel := context.WithTimeout(context.Background(), *requestTimeout)
				decision, err := runRequest(requestCtx, rl, fmt.Sprintf("shop-demo:customer-%d", i%int64(*customers)), *simulatedWork)
				cancel()
				latencies[i] = time.Since(before).Microseconds()
				if err != nil {
					if err == context.DeadlineExceeded {
						timedOut.Add(1)
					}
					continue
				}
				if decision.Allowed {
					allowed.Add(1)
				} else if decision.Reason == server.RateLimitReasonGlobal {
					globalRejected.Add(1)
				} else {
					customerRejected.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	getPercentile := func(p float64) int64 {
		index := int(math.Ceil(float64(len(latencies))*p)) - 1
		if index < 0 {
			index = 0
		}
		return latencies[index]
	}
	baselineInputTokens := int64(*requests) * *inputTokens
	baselineOutputTokens := int64(*requests) * *outputTokens
	optimizedInputTokens := allowed.Load() * *inputTokens
	optimizedOutputTokens := allowed.Load() * *outputTokens
	baseTokens := baselineInputTokens + baselineOutputTokens
	optimizedTokens := optimizedInputTokens + optimizedOutputTokens
	savedTokens := baseTokens - optimizedTokens
	rep := report{
		Requests: int64(*requests), Concurrency: *concurrency, Customers: *customers,
		Allowed: allowed.Load(), GlobalRejected: globalRejected.Load(), CustomerRejected: customerRejected.Load(), TimedOut: timedOut.Load(), RequestTimeoutMS: requestTimeout.Milliseconds(),
		ElapsedMS: float64(elapsed.Microseconds()) / 1000, RPS: float64(*requests) / elapsed.Seconds(),
		P50US: getPercentile(.50), P95US: getPercentile(.95), P99US: getPercentile(.99),
		BaselineInputTokens: baselineInputTokens, BaselineOutputTokens: baselineOutputTokens,
		OptimizedInputTokens: optimizedInputTokens, OptimizedOutputTokens: optimizedOutputTokens,
		BaselineTokens: baseTokens, OptimizedTokens: optimizedTokens, SavedTokens: savedTokens,
		BaselineCost:  tokenCost(baselineInputTokens, baselineOutputTokens, *inputCNYPer1K, *outputCNYPer1K),
		OptimizedCost: tokenCost(optimizedInputTokens, optimizedOutputTokens, *inputCNYPer1K, *outputCNYPer1K),
	}
	rep.Throttled = rep.GlobalRejected + rep.CustomerRejected
	rep.SavedCost = rep.BaselineCost - rep.OptimizedCost
	if baseTokens > 0 {
		rep.SavedPercent = float64(savedTokens) / float64(baseTokens) * 100
	}
	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(rep)
		return
	}
	fmt.Printf("OpenBook 本地并发流量模拟（不调用 LLM、不写数据库）\n")
	fmt.Printf("请求=%d，并发=%d，顾客=%d，耗时=%.2fms，限流判断吞吐=%.0f req/s\n", rep.Requests, rep.Concurrency, rep.Customers, rep.ElapsedMS, rep.RPS)
	fmt.Printf("放行=%d，拒绝=%d（全局=%d，顾客=%d），超时=%d（单请求上限=%dms）；请求耗时 P50/P95/P99=%d/%d/%d µs\n", rep.Allowed, rep.Throttled, rep.GlobalRejected, rep.CustomerRejected, rep.TimedOut, rep.RequestTimeoutMS, rep.P50US, rep.P95US, rep.P99US)
	fmt.Printf("Token：基线=%d（输入=%d，输出=%d），优化后=%d（输入=%d，输出=%d），节省=%d（%.2f%%）\n", rep.BaselineTokens, rep.BaselineInputTokens, rep.BaselineOutputTokens, rep.OptimizedTokens, rep.OptimizedInputTokens, rep.OptimizedOutputTokens, rep.SavedTokens, rep.SavedPercent)
	fmt.Printf("按输入 ¥%.6f/1K、输出 ¥%.6f/1K Token 估算：基线=¥%.6f，优化后=¥%.6f，节省=¥%.6f\n", *inputCNYPer1K, *outputCNYPer1K, rep.BaselineCost, rep.OptimizedCost, rep.SavedCost)
}

func tokenCost(inputTokens, outputTokens int64, inputCNYPer1K, outputCNYPer1K float64) float64 {
	return float64(inputTokens)/1000*inputCNYPer1K + float64(outputTokens)/1000*outputCNYPer1K
}

// runRequest is deliberately context-aware: if the work becomes slow, its
// worker returns at the deadline instead of remaining occupied indefinitely.
// The limiter itself is non-blocking; simulatedWork only exists to exercise
// the same cancellation path locally before an HTTP target is introduced.
func runRequest(ctx context.Context, rl *server.RateLimiter, customerKey string, simulatedWork time.Duration) (server.RateLimitDecision, error) {
	if simulatedWork > 0 {
		timer := time.NewTimer(simulatedWork)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return server.RateLimitDecision{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return server.RateLimitDecision{}, err
	}
	return rl.AllowDecision(customerKey), nil
}
