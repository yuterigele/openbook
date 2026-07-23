package server

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cloudwego/eino/adk"
	"github.com/yuterigele/openbook/msgops"
)

// AgentMetrics tracks completed Agent tasks and tool outcomes for platform
// operations. It intentionally contains no shop or customer identifier.
// Platform operators need global health signals, while tenant activity remains
// isolated from this in-process observability view.
type AgentMetrics struct {
	TasksStarted   atomic.Int64
	TasksSucceeded atomic.Int64
	TasksFailed    atomic.Int64

	mu    sync.Mutex
	tools map[string]*toolMetricCounter
}

type toolMetricCounter struct {
	Calls     int64
	Succeeded int64
	Failed    int64
}

// ToolMetric is one tool's aggregate result, safe to expose to a platform UI.
type ToolMetric struct {
	Name        string  `json:"name"`
	Calls       int64   `json:"calls"`
	Succeeded   int64   `json:"succeeded"`
	Failed      int64   `json:"failed"`
	SuccessRate float64 `json:"success_rate"`
}

// AgentMetricsSnapshot is the platform-only Agent observability response.
type AgentMetricsSnapshot struct {
	TasksStarted    int64        `json:"tasks_started"`
	TasksSucceeded  int64        `json:"tasks_succeeded"`
	TasksFailed     int64        `json:"tasks_failed"`
	TaskSuccessRate float64      `json:"task_success_rate"`
	Tools           []ToolMetric `json:"tools"`
}

// DefaultAgentMetrics is process-local by design, like the LLM usage tracker.
// Counters reset after a process restart and are intended for live operations.
var DefaultAgentMetrics = &AgentMetrics{tools: make(map[string]*toolMetricCounter)}

func (m *AgentMetrics) RecordTaskStarted() { m.TasksStarted.Add(1) }

func (m *AgentMetrics) RecordTaskFinished(success bool) {
	if success {
		m.TasksSucceeded.Add(1)
		return
	}
	m.TasksFailed.Add(1)
}

// RecordToolResult treats SafeToolMiddleware's [tool error] envelope as a
// failed execution. Business results returned normally by tools remain
// successful executions even when their user-facing message says unavailable.
func (m *AgentMetrics) RecordToolResult(name, content string) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "unknown"
	}
	failed := strings.HasPrefix(strings.TrimSpace(content), "[tool error]")
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.tools[name]
	if c == nil {
		c = &toolMetricCounter{}
		m.tools[name] = c
	}
	c.Calls++
	if failed {
		c.Failed++
	} else {
		c.Succeeded++
	}
}

// RecordToolResultMessages extracts complete tool-result messages from an
// Agent event or persisted intermediate list.
func RecordToolResultMessages[M adk.MessageType](messages []M) {
	for _, message := range messages {
		for _, result := range msgops.ToolResults(message) {
			DefaultAgentMetrics.RecordToolResult(result.Name, result.Content)
		}
	}
}

func (m *AgentMetrics) Snapshot() AgentMetricsSnapshot {
	s := AgentMetricsSnapshot{
		TasksStarted:   m.TasksStarted.Load(),
		TasksSucceeded: m.TasksSucceeded.Load(),
		TasksFailed:    m.TasksFailed.Load(),
	}
	completed := s.TasksSucceeded + s.TasksFailed
	if completed > 0 {
		s.TaskSuccessRate = float64(s.TasksSucceeded) / float64(completed)
	}

	m.mu.Lock()
	s.Tools = make([]ToolMetric, 0, len(m.tools))
	for name, c := range m.tools {
		metric := ToolMetric{Name: name, Calls: c.Calls, Succeeded: c.Succeeded, Failed: c.Failed}
		if c.Calls > 0 {
			metric.SuccessRate = float64(c.Succeeded) / float64(c.Calls)
		}
		s.Tools = append(s.Tools, metric)
	}
	m.mu.Unlock()
	sort.Slice(s.Tools, func(i, j int) bool { return s.Tools[i].Name < s.Tools[j].Name })
	return s
}

func (m *AgentMetrics) Reset() {
	m.TasksStarted.Store(0)
	m.TasksSucceeded.Store(0)
	m.TasksFailed.Store(0)
	m.mu.Lock()
	m.tools = make(map[string]*toolMetricCounter)
	m.mu.Unlock()
}
