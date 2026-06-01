package upstream

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"chatgpt2api-go-backend/internal/proxy"
)

func TestLiveTurnstileDiagnostics(t *testing.T) {
	accessToken := os.Getenv("CHATGPT2API_LIVE_ACCESS_TOKEN")
	if accessToken == "" {
		t.Skip("set CHATGPT2API_LIVE_ACCESS_TOKEN to run live upstream diagnostics")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	service := NewService(nil, proxy.NewService(os.Getenv("CHATGPT2API_PROXY")))
	client := service.NewClient(accessToken)
	if err := client.bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	sourceP := buildLegacyRequirementsToken(client.userAgent, client.powSources, client.powDataBuild)
	body, _ := json.Marshal(map[string]any{"p": sourceP})
	path := "/backend-api/sentinel/chat-requirements"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, client.BaseURL+path, bytes.NewReader(body))
	for key, value := range client.headers(path, map[string]string{"Content-Type": "application/json"}) {
		req.Header.Set(key, value)
	}
	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("chat requirements: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("chat requirements status=%d body=%s", resp.StatusCode, summarizeBody(data))
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	turnstile := mapField(payload["turnstile"])
	dx := cleanString(turnstile["dx"])
	for _, item := range []struct {
		name string
		p    string
	}{
		{name: "empty", p: ""},
		{name: "source", p: sourceP},
	} {
		token, status := solveTurnstileTokenWithStatus(dx, item.p)
		t.Logf("turnstile %s status=%s token_len=%d", item.name, status, len(token))
		if os.Getenv("CHATGPT2API_LIVE_VERBOSE") == "1" && item.name == "source" {
			decoded, err := base64.StdEncoding.DecodeString(dx)
			if err != nil {
				t.Fatalf("decode dx: %v", err)
			}
			var tokenList []any
			if err := json.Unmarshal([]byte(xorString(string(decoded), item.p)), &tokenList); err != nil {
				t.Fatalf("decode token list: %v", err)
			}
			t.Logf("turnstile aliases=%s", compactJSON(turnstileAliasSummary(tokenList)))
			logTurnstileSamples(t, tokenList)
			if os.Getenv("CHATGPT2API_LIVE_DUMP") == "1" {
				logTurnstileProgram(t, tokenList)
			}
		}
	}
}

func turnstileAliasSummary(tokenList []any) map[string]string {
	aliases := map[string]string{}
	for _, key := range []string{"1", "2", "3", "5", "6", "7", "8", "14", "15", "17", "18", "19", "20", "21", "23", "24"} {
		aliases[key] = key
	}
	for _, rawToken := range tokenList {
		token, ok := rawToken.([]any)
		if !ok || len(token) < 3 {
			continue
		}
		op := turnstileKey(token[0])
		if aliases[op] != "8" {
			continue
		}
		source := aliases[turnstileKey(token[2])]
		if source == "" {
			source = turnstileKey(token[2])
		}
		aliases[turnstileKey(token[1])] = source
	}
	out := map[string]string{}
	for key, value := range aliases {
		if key != value {
			out[key] = value
		}
	}
	return out
}

func logTurnstileSamples(t *testing.T, tokenList []any) {
	t.Helper()
	seen := map[string]int{}
	for _, rawToken := range tokenList {
		token, ok := rawToken.([]any)
		if !ok || len(token) == 0 {
			continue
		}
		op := turnstileKey(token[0])
		if seen[op] >= 4 {
			continue
		}
		seen[op]++
		t.Logf("turnstile sample op=%s token=%s", op, compactJSON(token))
	}
}

func logTurnstileProgram(t *testing.T, tokenList []any) {
	t.Helper()
	aliases := turnstileAliasSummary(tokenList)
	for index, rawToken := range tokenList {
		token, ok := rawToken.([]any)
		if !ok || len(token) == 0 {
			continue
		}
		op := turnstileKey(token[0])
		baseOp := aliases[op]
		if baseOp == "" {
			baseOp = op
		}
		t.Logf("turnstile program %03d op=%s base=%s token=%s", index, op, baseOp, compactJSON(token))
	}
}

func compactJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
