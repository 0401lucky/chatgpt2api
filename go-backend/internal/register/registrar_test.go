package register

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestReplaceRegisterSessionUsesIndependentDeviceCookie(t *testing.T) {
	originalDeviceID := "original-device-id"
	originalClient, err := registerHTTPClient("", 0, originalDeviceID)
	if err != nil {
		t.Fatalf("registerHTTPClient original error: %v", err)
	}
	worker := &registerWorker{
		config:   map[string]any{"proxy": ""},
		client:   originalClient,
		deviceID: originalDeviceID,
	}

	loginDeviceID := "login-device-id"
	loginClient, err := worker.replaceRegisterSession(loginDeviceID)
	if err != nil {
		t.Fatalf("replaceRegisterSession error: %v", err)
	}
	defer loginClient.CloseIdleConnections()

	if worker.client == originalClient {
		t.Fatalf("worker client was not replaced")
	}
	if worker.client != loginClient {
		t.Fatalf("worker client does not point to login client")
	}
	if worker.deviceID != loginDeviceID {
		t.Fatalf("worker deviceID = %q, want %q", worker.deviceID, loginDeviceID)
	}

	authURL, err := url.Parse(registerAuthBase)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	got := ""
	for _, cookie := range worker.client.Jar.Cookies(authURL) {
		if cookie.Name == "oai-did" {
			got = cookie.Value
			break
		}
	}
	if got != loginDeviceID {
		t.Fatalf("login oai-did cookie = %q, want %q", got, loginDeviceID)
	}
}

func TestRegisterHTTPClientAcceptsSocksProxyConfig(t *testing.T) {
	client, err := registerHTTPClient("socks5://127.0.0.1:7890", 0, "device-id")
	if err != nil {
		t.Fatalf("registerHTTPClient socks proxy error: %v", err)
	}
	defer client.CloseIdleConnections()

	if client.Jar == nil {
		t.Fatalf("registerHTTPClient did not attach cookie jar")
	}
}

func TestRequestDetailedWithFinalURLReturnsRedirectCallbackCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/callback?code=oauth-code&state=state-1", http.StatusFound)
		case "/callback":
			http.Redirect(w, r, "/done", http.StatusFound)
		case "/done":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.String())
		}
	}))
	defer server.Close()

	worker := &registerWorker{client: server.Client()}
	status, payload, _, finalURL, err := worker.requestDetailedWithFinalURL(context.Background(), http.MethodGet, server.URL+"/start", nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d payload=%#v", status, payload)
	}
	if code := oauthCode(finalURL); code != "oauth-code" {
		t.Fatalf("finalURL = %q, code = %q", finalURL, code)
	}
}
