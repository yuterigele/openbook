// feishu-alert-bridge converts Grafana's generic webhook payload into the
// message schema required by Feishu group bots.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const maxRequestBytes = 1 << 20 // 1 MiB

type grafanaWebhook struct {
	Status string `json:"status"`
	Alerts []struct {
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"alerts"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
}

type feishuMessage struct {
	MsgType string `json:"msg_type"`
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
}

func main() {
	webhookURL := strings.TrimSpace(os.Getenv("FEISHU_GRAFANA_WEBHOOK_URL"))
	if webhookURL == "" {
		log.Fatal("FEISHU_GRAFANA_WEBHOOK_URL must be configured")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("POST /grafana", webhookHandler(webhookURL, &http.Client{Timeout: 10 * time.Second}))

	addr := envOrDefault("FEISHU_ALERT_BRIDGE_ADDR", ":8080")
	log.Printf("feishu alert bridge listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func webhookHandler(webhookURL string, client *http.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := http.MaxBytesReader(w, r.Body, maxRequestBytes)
		defer body.Close()
		var payload grafanaWebhook
		if err := json.NewDecoder(body).Decode(&payload); err != nil {
			http.Error(w, "invalid Grafana webhook payload", http.StatusBadRequest)
			return
		}

		message, err := json.Marshal(toFeishuMessage(payload))
		if err != nil {
			log.Printf("encode Feishu alert: %v", err)
			http.Error(w, "encode alert failed", http.StatusInternalServerError)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, webhookURL, bytes.NewReader(message))
		if err != nil {
			log.Printf("create Feishu request: %v", err)
			http.Error(w, "invalid Feishu webhook configuration", http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("send Feishu alert: %v", err)
			http.Error(w, "Feishu notification failed", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			log.Printf("Feishu returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
			http.Error(w, "Feishu notification was rejected", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func toFeishuMessage(payload grafanaWebhook) feishuMessage {
	status := strings.ToUpper(payload.Status)
	if status == "" {
		status = "FIRING"
	}
	var lines []string
	for _, alert := range payload.Alerts {
		name := alert.Labels["alertname"]
		if name == "" {
			name = payload.CommonLabels["alertname"]
		}
		if name == "" {
			name = "OpenBook 告警"
		}
		summary := firstNonEmpty(alert.Annotations["summary"], payload.CommonAnnotations["summary"])
		description := firstNonEmpty(alert.Annotations["description"], payload.CommonAnnotations["description"])
		lines = append(lines, fmt.Sprintf("【%s】%s", strings.ToUpper(firstNonEmpty(alert.Status, status)), name))
		if summary != "" {
			lines = append(lines, "摘要："+summary)
		}
		if description != "" {
			lines = append(lines, "详情："+description)
		}
	}
	if len(lines) == 0 {
		lines = []string{fmt.Sprintf("【%s】OpenBook 告警", status)}
	}
	var result feishuMessage
	result.MsgType = "text"
	result.Content.Text = strings.Join(lines, "\n")
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
