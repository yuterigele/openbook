package server

import "testing"

func TestAgentMetrics_TaskAndToolSuccessRates(t *testing.T) {
	m := &AgentMetrics{tools: make(map[string]*toolMetricCounter)}
	m.RecordTaskStarted()
	m.RecordTaskFinished(true)
	m.RecordTaskStarted()
	m.RecordTaskFinished(false)
	m.RecordToolResult("query_schedule", "available slots")
	m.RecordToolResult("query_schedule", "[tool error] database unavailable")

	s := m.Snapshot()
	if s.TaskSuccessRate != 0.5 {
		t.Fatalf("task success rate = %v, want 0.5", s.TaskSuccessRate)
	}
	if len(s.Tools) != 1 || s.Tools[0].SuccessRate != 0.5 {
		t.Fatalf("tool metrics = %+v, want one 50%% tool", s.Tools)
	}
}
