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
	if s.ToolCalls != 2 || s.ToolSuccessRate != 0.5 || !s.ToolSLOMet {
		t.Fatalf("aggregate tool SLO = %+v, want 2 calls / 50%% / neutral before evaluation", s)
	}
	if s.ToolSLOEvaluated {
		t.Fatalf("SLO should not be evaluated below the minimum sample size: %+v", s)
	}
}

func TestAgentMetrics_ToolSLOAtTarget(t *testing.T) {
	m := &AgentMetrics{tools: make(map[string]*toolMetricCounter)}
	for i := 0; i < 20; i++ {
		result := "ok"
		if i == 0 {
			result = "[tool error] temporary failure"
		}
		m.RecordToolResult("query_schedule", result)
	}
	s := m.Snapshot()
	if !s.ToolSLOEvaluated || !s.ToolSLOMet || s.ToolSuccessRate != ToolSuccessTarget {
		t.Fatalf("SLO = %+v, want evaluated and met at %.2f", s, ToolSuccessTarget)
	}
}
