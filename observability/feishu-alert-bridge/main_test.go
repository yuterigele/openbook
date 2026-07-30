package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookHandlerConvertsGrafanaPayloadToFeishu(t *testing.T) {
	var got feishuMessage
	feishu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if contentType := r.Header.Get("Content-Type"); contentType != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", contentType)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode Feishu message: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer feishu.Close()

	bridge := httptest.NewServer(webhookHandler(feishu.URL, feishu.Client()))
	defer bridge.Close()
	payload := `{
		"status":"firing",
		"alerts":[{
			"status":"firing",
			"labels":{"alertname":"OpenBookHighLLMErrorRate"},
			"annotations":{"summary":"LLM 错误率超过 10%","description":"请检查模型服务"}
		}]
	}`
	resp, err := http.Post(bridge.URL, "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("post bridge: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	if got.MsgType != "text" {
		t.Fatalf("msg_type = %q, want text", got.MsgType)
	}
	for _, expected := range []string{"【FIRING】OpenBookHighLLMErrorRate", "摘要：LLM 错误率超过 10%", "详情：请检查模型服务"} {
		if !strings.Contains(got.Content.Text, expected) {
			t.Errorf("message %q does not contain %q", got.Content.Text, expected)
		}
	}
}
