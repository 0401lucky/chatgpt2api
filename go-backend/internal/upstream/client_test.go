package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html></html>"))
		case "/backend-anon/models":
			if r.Header.Get("X-OpenAI-Target-Path") != "/backend-anon/models" {
				t.Fatalf("target path header = %q", r.Header.Get("X-OpenAI-Target-Path"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{
					{"slug": "gpt-5", "created": 123, "owned_by": "openai"},
					{"slug": "gpt-5", "created": 123, "owned_by": "openai"},
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.String())
		}
	}))
	defer server.Close()

	client := NewTestClient(server.URL, "", nil, server.Client())
	result, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data := result["data"].([]map[string]any)
	seen := map[string]bool{}
	for _, item := range data {
		seen[item["id"].(string)] = true
	}
	if !seen["gpt-5"] || !seen["gpt-image-2"] || !seen["codex-gpt-image-2"] {
		t.Fatalf("models missing expected ids: %#v", data)
	}
}

func TestFetchRemoteInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html></html>"))
		case "/backend-api/me":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "test@example.com", "id": "user-1"})
		case "/backend-api/conversation/init":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"default_model_slug": "gpt-5",
				"limits_progress": []map[string]any{{
					"feature_name": "image_gen",
					"remaining":    2,
					"reset_after":  "2026-06-01T00:00:00Z",
				}},
				"workspace": map[string]any{"plan_type": "plus"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.String())
		}
	}))
	defer server.Close()

	client := NewTestClient(server.URL, "test-token", nil, server.Client())
	info, err := client.FetchRemoteInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info["email"] != "test@example.com" {
		t.Fatalf("email = %#v", info["email"])
	}
	if info["quota"] != 2 || info["status"] != "正常" || info["image_quota_unknown"] != false {
		t.Fatalf("info = %#v", info)
	}
	if info["type"] != "Plus" {
		t.Fatalf("type = %#v", info["type"])
	}
}

func TestStreamConversation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html data-build="test-build"><script src="/backend-api/sentinel/sdk.js"></script></html>`))
		case "/backend-api/sentinel/chat-requirements":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["p"] == "" {
				t.Fatalf("missing p body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "requirements-token"})
		case "/backend-api/conversation":
			if r.Header.Get("OpenAI-Sentinel-Chat-Requirements-Token") != "requirements-token" {
				t.Fatalf("requirements header = %q", r.Header.Get("OpenAI-Sentinel-Chat-Requirements-Token"))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"message\":{\"author\":{\"role\":\"assistant\"},\"content\":{\"parts\":[\"hello\"]}}}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.String())
		}
	}))
	defer server.Close()

	client := NewTestClient(server.URL, "test-token", nil, server.Client())
	payloads, errCh := client.StreamConversation(context.Background(), []map[string]any{{"role": "user", "content": "ping"}}, "auto", "")
	var got []string
	for payload := range payloads {
		got = append(got, payload)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != "[DONE]" {
		t.Fatalf("payloads = %#v", got)
	}
}

func TestDownloadImageBytesUsesChatGPTHeadersForBackendURL(t *testing.T) {
	var hit atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/files/download/file-1" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("OAI-Device-Id") == "" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("missing chatgpt headers: %#v", r.Header)
		}
		hit.Store(true)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("png-bytes"))
	}))
	defer server.Close()

	client := NewTestClient(server.URL, "test-token", nil, server.Client())
	data, err := client.downloadImageBytes(context.Background(), server.URL+"/backend-api/files/download/file-1?conversation_id=conv-1&inline=false")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "png-bytes" {
		t.Fatalf("data = %q", string(data))
	}
	if !hit.Load() {
		t.Fatal("server not hit")
	}
}
