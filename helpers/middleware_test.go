package helpers

import (
	"errors"
	"testing"

	"github.com/cloudwego/eino/adk"
)

func TestIsRetryableReadTool(t *testing.T) {
	tests := []struct {
		name string
		tool string
		err  error
		want bool
	}{
		{"transient read failure", "query_schedule", errors.New("connection reset by peer"), true},
		{"business rejection", "query_schedule", errors.New("时段已被预约"), false},
		{"write is never retried", "create_appointment", errors.New("i/o timeout"), false},
		{"unknown tool", "unknown", errors.New("EOF"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableReadTool(&adk.ToolContext{Name: tt.tool}, tt.err)
			if got != tt.want {
				t.Fatalf("isRetryableReadTool(%q, %v) = %v, want %v", tt.tool, tt.err, got, tt.want)
			}
		})
	}
}
