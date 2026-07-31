package wecom

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestSendKfTextMessageAutoTakeoverOn95018(t *testing.T) {
	client := NewClient("corp", "secret", 1)
	client.accessToken = "cached-token"
	client.accessTokenExp = time.Now().Add(time.Hour)
	client.SetKfAutoTakeover(true)

	sendCalls := 0
	transferCalls := 0
	client.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/cgi-bin/kf/send_msg":
			sendCalls++
			if sendCalls == 1 {
				return jsonResponse(`{"errcode":95018,"errmsg":"send msg session status invalid"}`), nil
			}
			return jsonResponse(`{"errcode":0,"errmsg":"ok","msgid":"msg-1"}`), nil
		case "/cgi-bin/kf/service_state/trans":
			transferCalls++
			var body map[string]interface{}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode transfer body: %v", err)
			}
			if body["open_kfid"] != "wk-test" || body["external_userid"] != "wm-test" {
				t.Fatalf("unexpected transfer identity: %#v", body)
			}
			if body["service_state"] != float64(1) {
				t.Fatalf("service_state = %#v, want 1", body["service_state"])
			}
			return jsonResponse(`{"errcode":0,"errmsg":"ok"}`), nil
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})}

	if err := client.SendKfTextMessage(context.Background(), "wm-test", "wk-test", "hello"); err != nil {
		t.Fatalf("SendKfTextMessage: %v", err)
	}
	if sendCalls != 2 || transferCalls != 1 {
		t.Fatalf("calls = send:%d transfer:%d, want 2/1", sendCalls, transferCalls)
	}
}

func TestSendKfTextMessageDoesNotTakeOverByDefault(t *testing.T) {
	client := NewClient("corp", "secret", 1)
	client.accessToken = "cached-token"
	client.accessTokenExp = time.Now().Add(time.Hour)

	sendCalls := 0
	client.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		sendCalls++
		return jsonResponse(`{"errcode":95018,"errmsg":"send msg session status invalid"}`), nil
	})}

	err := client.SendKfTextMessage(context.Background(), "wm-test", "wk-test", "hello")
	if err == nil || !strings.Contains(err.Error(), "95018") {
		t.Fatalf("error = %v, want 95018", err)
	}
	if sendCalls != 1 {
		t.Fatalf("send calls = %d, want 1 without takeover", sendCalls)
	}
}
