package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSearchUsesPreparedSearchConversationAndPollsResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html data-build="test-build"><script src="/backend-api/sentinel/sdk.js"></script></html>`))
		case "/backend-api/f/conversation/prepare":
			if r.Header.Get("X-Conduit-Token") != "no-token" {
				t.Fatalf("prepare conduit header = %q", r.Header.Get("X-Conduit-Token"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != searchModel {
				t.Fatalf("prepare model = %#v", body["model"])
			}
			hints := body["system_hints"].([]any)
			if len(hints) != 1 || hints[0] != "search" {
				t.Fatalf("prepare hints = %#v", body["system_hints"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"conduit_token": "conduit-token"})
		case "/backend-api/sentinel/chat-requirements":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "requirements-token"})
		case "/backend-api/f/conversation":
			if r.Header.Get("X-Conduit-Token") != "conduit-token" {
				t.Fatalf("run conduit header = %q", r.Header.Get("X-Conduit-Token"))
			}
			if r.Header.Get("OpenAI-Sentinel-Chat-Requirements-Token") != "requirements-token" {
				t.Fatalf("requirements header = %q", r.Header.Get("OpenAI-Sentinel-Chat-Requirements-Token"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["force_use_search"] != true {
				t.Fatalf("force_use_search = %#v", body["force_use_search"])
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"conversation_id":"conv-search"}` + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		case "/backend-api/conversation/conv-search":
			if r.Header.Get("X-OpenAI-Target-Route") != "/backend-api/conversation/{conversation_id}" {
				t.Fatalf("target route = %q", r.Header.Get("X-OpenAI-Target-Route"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"mapping": map[string]any{
				"msg-1": map[string]any{"message": map[string]any{
					"id":          "assistant-1",
					"create_time": 123.0,
					"author":      map[string]any{"role": "assistant"},
					"metadata":    map[string]any{"finish_details": map[string]any{"type": "finished_successfully"}},
					"content": map[string]any{"parts": []any{
						"搜索答案 https://example.com/a",
						map[string]any{"title": "Example", "url": "https://example.com/a", "snippet": "摘录", "type": "webpage"},
					}},
				}},
			}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.String())
		}
	}))
	defer server.Close()

	client := NewTestClient(server.URL, "test-token", nil, server.Client())
	result, err := client.Search(context.Background(), "查一下今天的消息", "", 5*time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if result["conversation_id"] != "conv-search" || result["status"] != "finished_successfully" {
		t.Fatalf("result = %#v", result)
	}
	if result["answer"] != "搜索答案 https://example.com/a" {
		t.Fatalf("answer = %#v", result["answer"])
	}
	sources := result["sources"].([]map[string]string)
	if len(sources) != 1 || sources[0]["url"] != "https://example.com/a" {
		t.Fatalf("sources = %#v", sources)
	}
}
