package cron

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// TaskMetrics records only operational timestamps and error counts; it never
// records customer, appointment, or message data.
type TaskMetrics struct {
	mu    sync.RWMutex
	tasks map[string]taskMetric
}

type taskMetric struct {
	lastSuccess time.Time
	failures    uint64
}

var DefaultTaskMetrics = &TaskMetrics{tasks: make(map[string]taskMetric)}

func (m *TaskMetrics) RecordSuccess(task string) {
	m.mu.Lock()
	entry := m.tasks[task]
	entry.lastSuccess = time.Now()
	m.tasks[task] = entry
	m.mu.Unlock()
}

func (m *TaskMetrics) RecordFailure(task string) {
	m.mu.Lock()
	entry := m.tasks[task]
	entry.failures++
	m.tasks[task] = entry
	m.mu.Unlock()
}

// PrometheusText renders the registered cron task heartbeat metrics.
func (m *TaskMetrics) PrometheusText() string {
	m.mu.RLock()
	names := make([]string, 0, len(m.tasks))
	for name := range m.tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# HELP openbook_cron_last_success_timestamp_seconds Unix time of the last successful cron task run.\n# TYPE openbook_cron_last_success_timestamp_seconds gauge\n")
	b.WriteString("# HELP openbook_cron_failures_total Total failed cron task runs.\n# TYPE openbook_cron_failures_total counter\n")
	for _, name := range names {
		entry := m.tasks[name]
		fmt.Fprintf(&b, "openbook_cron_last_success_timestamp_seconds{task=%q} %d\n", name, entry.lastSuccess.Unix())
		fmt.Fprintf(&b, "openbook_cron_failures_total{task=%q} %d\n", name, entry.failures)
	}
	m.mu.RUnlock()
	return b.String()
}
