package chatmodel

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	degradationWindow     = 5 * time.Minute
	degradationMinCalls   = 20
	degradationThreshold  = 0.05
	degradationAlertDelay = 15 * time.Minute
)

// DegradationMonitor measures runtime provider switches over model calls. It
// deliberately sends only operational aggregates, never prompts, replies or
// customer identifiers.
type DegradationMonitor struct {
	mu            sync.Mutex
	attempts      []time.Time
	fallbacks     []time.Time
	active        bool
	lastAlertSent time.Time
	client        *http.Client
}

var DefaultDegradationMonitor = &DegradationMonitor{client: &http.Client{Timeout: 5 * time.Second}}

var runtimeFallbackAttempts atomic.Int64

// SendFeishuDegradationTestAlert sends one connectivity-check notification
// when explicitly enabled. It never changes alert counters or state.
func SendFeishuDegradationTestAlert() {
	if os.Getenv("FEISHU_ALERT_TEST") != "1" {
		log.Printf("[alert] Feishu degradation connectivity test disabled (set FEISHU_ALERT_TEST=1 to enable)")
		return
	}
	log.Printf("[alert] sending Feishu degradation connectivity test")
	go DefaultDegradationMonitor.send("连通性测试", 0, 0, 0)
}

func (m *DegradationMonitor) RecordAttempt() { m.record(false) }
func (m *DegradationMonitor) RecordFallback() {
	runtimeFallbackAttempts.Add(1)
	m.record(true)
}

// RuntimeFallbackAttempts returns process-lifetime LLM provider switches.
func RuntimeFallbackAttempts() int64 { return runtimeFallbackAttempts.Load() }

func (m *DegradationMonitor) record(fallback bool) {
	now := time.Now()
	m.mu.Lock()
	m.attempts = pruneTimes(m.attempts, now.Add(-degradationWindow))
	m.fallbacks = pruneTimes(m.fallbacks, now.Add(-degradationWindow))
	m.attempts = append(m.attempts, now)
	if fallback {
		m.fallbacks = append(m.fallbacks, now)
	}
	calls, falls := len(m.attempts), len(m.fallbacks)
	rate := 0.0
	if calls > 0 {
		rate = float64(falls) / float64(calls)
	}
	shouldAlert := calls >= degradationMinCalls && rate > degradationThreshold && (!m.active || now.Sub(m.lastAlertSent) >= degradationAlertDelay)
	shouldRecover := m.active && (calls < degradationMinCalls || rate <= degradationThreshold)
	if shouldAlert {
		m.active = true
		m.lastAlertSent = now
	}
	if shouldRecover {
		m.active = false
	}
	m.mu.Unlock()
	if shouldAlert {
		go m.send("告警", calls, falls, rate)
	}
	if shouldRecover {
		go m.send("恢复", calls, falls, rate)
	}
}

func pruneTimes(events []time.Time, cutoff time.Time) []time.Time {
	first := 0
	for first < len(events) && events[first].Before(cutoff) {
		first++
	}
	return append([]time.Time(nil), events[first:]...)
}

func (m *DegradationMonitor) send(kind string, calls, fallbacks int, rate float64) {
	url := strings.TrimSpace(os.Getenv("FEISHU_ALERT_WEBHOOK_URL"))
	if url == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"msg_type": "text", "content": map[string]string{"text": "[OpenBook 模型降级" + kind + "] 5 分钟模型调用=" + itoa(calls) + "，运行时降级=" + itoa(fallbacks) + "，降级率=" + formatRate(rate)}})
	resp, err := m.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[alert] Feishu degradation notification failed: %v", err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[alert] Feishu degradation notification returned HTTP %d", resp.StatusCode)
	}
}

func itoa(v int) string           { return strconv.Itoa(v) }
func formatRate(v float64) string { return strconv.FormatFloat(v*100, 'f', 2, 64) + "%" }
