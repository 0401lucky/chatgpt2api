package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	if !seen["gpt-5"] || !seen["gpt-5-5"] || !seen["gpt-image-2"] || !seen["codex-gpt-image-2"] {
		t.Fatalf("models missing expected ids: %#v", data)
	}
}

func TestDefaultFingerprintUsesChromeProfile(t *testing.T) {
	client := NewTestClient("https://example.test", "", nil, http.DefaultClient)
	if client.fp["impersonate"] != defaultProfile {
		t.Fatalf("impersonate = %q", client.fp["impersonate"])
	}
	if client.fp["user-agent"] != defaultUserAgent {
		t.Fatalf("user-agent = %q", client.fp["user-agent"])
	}
	if client.fp["sec-ch-ua"] != defaultSecCHUA {
		t.Fatalf("sec-ch-ua = %q", client.fp["sec-ch-ua"])
	}
	if !strings.Contains(client.fp["sec-ch-ua-full-version-list"], "Google Chrome") {
		t.Fatalf("sec-ch-ua-full-version-list = %q", client.fp["sec-ch-ua-full-version-list"])
	}
}

func TestEdgeFingerprintIsNormalizedForGoClient(t *testing.T) {
	lookup := fakeAccountLookup{account: map[string]any{
		"fp": map[string]any{
			"impersonate": "edge101",
			"user-agent":  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36 Edg/143.0.0.0",
			"sec-ch-ua":   `"Microsoft Edge";v="143", "Chromium";v="143", "Not A(Brand";v="24"`,
		},
	}}
	client := NewTestClient("https://example.test", "token", lookup, http.DefaultClient)
	if client.fp["impersonate"] != defaultProfile {
		t.Fatalf("impersonate = %q", client.fp["impersonate"])
	}
	if client.fp["user-agent"] != defaultUserAgent {
		t.Fatalf("user-agent = %q", client.fp["user-agent"])
	}
	if client.fp["sec-ch-ua"] != defaultSecCHUA {
		t.Fatalf("sec-ch-ua = %q", client.fp["sec-ch-ua"])
	}
}

func TestBootstrapCloudflareChallengeFallsBackToDefaultPOW(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<!doctype html><html><body>Cloudflare cf_chl challenge-platform</body></html>`))
		case "/backend-api/sentinel/chat-requirements":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["p"] == "" {
				t.Fatalf("missing fallback p body: %#v", body)
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
	if len(client.powSources) != 1 || client.powSources[0] != defaultPOWScript {
		t.Fatalf("powSources = %#v", client.powSources)
	}
}

func TestChatRequirementsCloudflareChallengeRetriesWithFreshSession(t *testing.T) {
	var requirementsHits atomic.Int32
	var factoryHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html></html>"))
		case "/backend-api/sentinel/chat-requirements":
			hit := requirementsHits.Add(1)
			if hit == 1 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`<!doctype html><html><body>Cloudflare cf_chl challenge-platform</body></html>`))
				return
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
	firstDeviceID := client.deviceID
	client.NewHTTPClient = func(profile string, timeout time.Duration) *http.Client {
		if profile != defaultProfile {
			t.Fatalf("profile = %q", profile)
		}
		factoryHits.Add(1)
		return server.Client()
	}

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
	if requirementsHits.Load() != 2 {
		t.Fatalf("requirements hits = %d", requirementsHits.Load())
	}
	if factoryHits.Load() != 1 {
		t.Fatalf("factory hits = %d", factoryHits.Load())
	}
	if client.deviceID == firstDeviceID {
		t.Fatal("device id was not refreshed")
	}
}

func TestFetchRemoteInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			t.Fatalf("refresh account should not bootstrap homepage")
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

func TestRefreshAccessTokenPostsOAuthRefreshForm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("path = %s", r.URL.String())
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Fatalf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "refresh-old" {
			t.Fatalf("refresh_token = %q", r.Form.Get("refresh_token"))
		}
		if r.Form.Get("client_id") != platformOAuthClientID {
			t.Fatalf("client_id = %q", r.Form.Get("client_id"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-new"})
	}))
	defer server.Close()

	service := &Service{OAuthTokenURL: server.URL + "/oauth/token"}
	tokens, err := service.RefreshAccessToken(context.Background(), "refresh-old")
	if err != nil {
		t.Fatal(err)
	}
	if tokens["access_token"] != "access-new" {
		t.Fatalf("access_token = %#v", tokens["access_token"])
	}
	if tokens["refresh_token"] != "refresh-old" {
		t.Fatalf("refresh_token fallback = %#v", tokens["refresh_token"])
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

func TestAPIMessagesToConversationMessagesUploadsImageInput(t *testing.T) {
	var uploaded atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/files":
			if r.Method != http.MethodPost {
				t.Fatalf("upload metadata method = %s", r.Method)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"upload_url": "http://" + r.Host + "/upload-target",
				"file_id":    "file-vision-1",
			})
		case "/upload-target":
			if r.Method != http.MethodPut {
				t.Fatalf("upload bytes method = %s", r.Method)
			}
			uploaded.Store(true)
			w.WriteHeader(http.StatusOK)
		case "/backend-api/files/file-vision-1/uploaded":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewTestClient(server.URL, "test-token", nil, server.Client())
	messages, err := client.apiMessagesToConversationMessages(context.Background(), []map[string]any{{
		"role": "user",
		"content": []any{
			map[string]any{"type": "input_text", "text": "describe this"},
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64,iVBORw0KGgo="},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !uploaded.Load() {
		t.Fatal("image bytes were not uploaded")
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	content := messages[0]["content"].(map[string]any)
	if content["content_type"] != "multimodal_text" {
		t.Fatalf("content = %#v", content)
	}
	parts := content["parts"].([]any)
	first := parts[0].(map[string]any)
	if first["asset_pointer"] != "file-service://file-vision-1" {
		t.Fatalf("parts = %#v", parts)
	}
}

type fakeAccountLookup struct {
	account map[string]any
}

func (f fakeAccountLookup) GetAccount(string) map[string]any {
	return f.account
}
