package smoketest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const smokeKey = "go-smoke-key"

func TestLocalGoProcessSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skip process smoke test in short mode")
	}
	root := repoRoot(t)
	dataDir := t.TempDir()
	port := freePort(t)
	configPath := filepath.Join(dataDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"auth-key":"placeholder","backup":{"enabled":true,"provider":"cloudflare_r2","account_id":"a","access_key_id":"b","secret_access_key":"c","bucket":"d","prefix":"backups","interval_minutes":1440,"rotation_keep":10,"encrypt":false,"passphrase":"","include":{"config":true,"register":true,"cpa":true,"sub2api":true,"logs":true,"image_tasks":true,"accounts_snapshot":true,"auth_keys_snapshot":true,"images":false}},"image_poll_initial_wait_secs":0,"image_poll_interval_secs":1,"image_poll_timeout_secs":5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1:" + port
	mockBase, mockClose := startMockUpstream(t)
	defer mockClose()
	started := startGoProcess(t, root, dataDir, port, mockBase)
	defer started.close(t)
	waitForHealth(t, base)

	assertStatus(t, requestJSON(t, http.MethodGet, base+"/health", "", nil), http.StatusOK)
	assertStatus(t, requestJSON(t, http.MethodPost, base+"/auth/login", smokeKey, map[string]any{}), http.StatusOK)
	assertStatus(t, requestJSON(t, http.MethodGet, base+"/api/accounts", smokeKey, nil), http.StatusOK)

	add := decodeMap(t, requestJSON(t, http.MethodPost, base+"/api/accounts", smokeKey, map[string]any{"tokens": []string{"smoke-token-1234567890"}}))
	if intValue(add["added"]) != 1 {
		t.Fatalf("add account result = %#v", add)
	}

	accounts := decodeMap(t, requestJSON(t, http.MethodGet, base+"/api/accounts", smokeKey, nil))
	items := anySlice(accounts["items"])
	if len(items) != 1 {
		t.Fatalf("accounts = %#v", accounts)
	}
	first, _ := items[0].(map[string]any)
	if _, ok := first["access_token"]; ok {
		t.Fatalf("account leaked access_token: %#v", first)
	}

	refresh := decodeMap(t, requestJSON(t, http.MethodPost, base+"/api/accounts/refresh", smokeKey, map[string]any{}))
	if intValue(refresh["refreshed"]) != 1 {
		t.Fatalf("refresh should succeed against mock upstream: %#v", refresh)
	}
	if !logContains(t, filepath.Join(dataDir, "data", "logs.jsonl"), "刷新账号") {
		t.Fatalf("log file missing refresh record")
	}
	logs := decodeMap(t, requestJSON(t, http.MethodGet, base+"/api/logs?type=account", smokeKey, nil))
	if len(anySlice(logs["items"])) == 0 {
		t.Fatalf("account logs missing: %#v", logs)
	}

	assertStatus(t, requestJSON(t, http.MethodGet, base+"/api/settings", smokeKey, nil), http.StatusOK)
	storage := decodeMap(t, requestJSON(t, http.MethodGet, base+"/api/storage/info", smokeKey, nil))
	backend := storage["backend"].(map[string]any)
	if backend["type"] != "json" {
		t.Fatalf("storage info = %#v", storage)
	}
	assertStatus(t, requestJSON(t, http.MethodGet, base+"/api/images", smokeKey, nil), http.StatusOK)
	assertStatus(t, requestJSON(t, http.MethodGet, base+"/api/image-history", smokeKey, nil), http.StatusOK)
	assertStatus(t, requestJSON(t, http.MethodGet, base+"/api/register", smokeKey, nil), http.StatusOK)
	assertStatus(t, requestJSON(t, http.MethodGet, base+"/api/cpa/pools", smokeKey, nil), http.StatusOK)
	assertStatus(t, requestJSON(t, http.MethodGet, base+"/api/sub2api/servers", smokeKey, nil), http.StatusOK)

	backupRun := decodeMap(t, requestJSON(t, http.MethodPost, base+"/api/backups/run", smokeKey, map[string]any{}))
	result := backupRun["result"].(map[string]any)
	key := strings.TrimSpace(fmt.Sprint(result["key"]))
	if key == "" {
		t.Fatalf("backup result = %#v", backupRun)
	}
	backups := decodeMap(t, requestJSON(t, http.MethodGet, base+"/api/backups", smokeKey, nil))
	if len(anySlice(backups["items"])) != 1 {
		t.Fatalf("backup list = %#v", backups)
	}
	assertStatus(t, requestJSON(t, http.MethodGet, base+"/api/backups/detail?key="+key, smokeKey, nil), http.StatusOK)

	models := decodeMap(t, requestJSON(t, http.MethodGet, base+"/v1/models", smokeKey, nil))
	if models["object"] != "list" || len(anySlice(models["data"])) == 0 {
		t.Fatalf("models = %#v", models)
	}

	chatResp := requestJSON(t, http.MethodPost, base+"/v1/chat/completions", smokeKey, map[string]any{
		"model":    "auto",
		"messages": []map[string]any{{"role": "user", "content": "ping"}},
	})
	assertStatus(t, chatResp, http.StatusOK)
	imageResp := requestJSON(t, http.MethodPost, base+"/v1/images/generations", smokeKey, map[string]any{
		"model":           "gpt-image-2",
		"prompt":          "smoke image",
		"response_format": "b64_json",
	})
	assertStatus(t, imageResp, http.StatusOK)
	editResp := requestMultipartEdit(t, base+"/v1/images/edits", smokeKey)
	assertStatus(t, editResp, http.StatusOK)
	respBody := decodeMap(t, requestJSON(t, http.MethodPost, base+"/v1/responses", smokeKey, map[string]any{
		"model": "gpt-image-2",
		"input": "生成一张白底红点",
		"tools": []map[string]any{{"type": "image_generation"}},
	}))
	if respBody["status"] != "completed" {
		t.Fatalf("response api = %#v", respBody)
	}
	messageBody := decodeMap(t, requestJSON(t, http.MethodPost, base+"/v1/messages", smokeKey, map[string]any{
		"model":    "auto",
		"messages": []map[string]any{{"role": "user", "content": "hello"}},
	}))
	if messageBody["role"] != "assistant" {
		t.Fatalf("messages api = %#v", messageBody)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return strings.TrimPrefix(listener.Addr().String(), "127.0.0.1:")
}

func waitForHealth(t *testing.T, base string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("go process did not become healthy")
}

func requestJSON(t *testing.T, method, url, key string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func requestMultipartEdit(t *testing.T, url, key string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("model", "gpt-image-2")
	_ = writer.WriteField("prompt", "smoke edit")
	part, err := writer.CreateFormFile("image", "input.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte{137, 80, 78, 71, 13, 10, 26, 10})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func assertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, want, body)
	}
}

func decodeMap(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func logContains(t *testing.T, path, needle string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), needle)
}

type goProcess struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

func (p *goProcess) close(t *testing.T) {
	t.Helper()
	if p.cancel != nil {
		p.cancel()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		done := make(chan struct{}, 1)
		go func() {
			_ = p.cmd.Wait()
			done <- struct{}{}
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("smoke process did not exit within timeout after kill")
		}
	}
}

func startGoProcess(t *testing.T, root, dataDir, port, mockBase string) *goProcess {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "chatgpt2api-smoke.exe")
	build := exec.Command("go", "build", "-o", binary, "./cmd/chatgpt2api")
	build.Dir = root
	build.Env = os.Environ()
	if cache := os.Getenv("GOCACHE"); cache != "" {
		build.Env = append(build.Env, "GOCACHE="+cache)
	}
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build smoke binary: %v\n%s", err, out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binary)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"CHATGPT2API_AUTH_KEY="+smokeKey,
		"CHATGPT2API_CONFIG_FILE="+filepath.Join(dataDir, "config.json"),
		"CHATGPT2API_DATA_DIR="+filepath.Join(dataDir, "data"),
		"CHATGPT2API_GO_PORT="+port,
		"CHATGPT2API_BASE_URL="+mockBase,
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("go output:\n%s", output.String())
		}
	})
	return &goProcess{cmd: cmd, cancel: cancel}
}

func startMockUpstream(t *testing.T) (string, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			_, _ = w.Write([]byte(`<html data-build="test-build"><script src="/backend-api/sentinel/sdk.js"></script></html>`))
		case r.URL.Path == "/backend-anon/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{{"slug": "gpt-5", "created": 1, "owned_by": "openai"}},
			})
		case r.URL.Path == "/backend-api/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "test@example.com", "id": "user-1", "chatgpt_plan_type": "plus"})
		case r.URL.Path == "/backend-api/conversation/init":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"default_model_slug": "gpt-5",
				"limits_progress":    []map[string]any{{"feature_name": "image_gen", "remaining": 10, "reset_after": "2026-06-01T00:00:00Z"}},
			})
		case r.URL.Path == "/backend-api/sentinel/chat-requirements/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"prepare_token": "prepare-token", "turnstile": map[string]any{"required": false}, "proofofwork": map[string]any{"required": false}})
		case r.URL.Path == "/backend-api/sentinel/chat-requirements/finalize":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "requirements-token"})
		case r.URL.Path == "/backend-api/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"message":{"author":{"role":"assistant"},"content":{"parts":["hello"]}}}` + "\n\n"))
			_, _ = w.Write([]byte(`data: [DONE]` + "\n\n"))
		case r.URL.Path == "/backend-api/f/conversation/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"conduit_token": "conduit-token"})
		case r.URL.Path == "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"conversation_id":"conv-1","message":{"author":{"role":"tool"},"metadata":{"async_task_type":"image_gen"},"content":{"content_type":"multimodal_text","parts":[{"asset_pointer":"file-service://file-1"}]}}}` + "\n\n"))
			_, _ = w.Write([]byte(`data: [DONE]` + "\n\n"))
		case r.URL.Path == "/backend-api/files":
			_ = json.NewEncoder(w).Encode(map[string]any{"file_id": "file-1", "upload_url": "http://" + r.Host + "/upload/file-1"})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/upload/"):
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/backend-api/files/file-1/uploaded":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.URL.Path == "/backend-api/conversation/conv-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"mapping": map[string]any{
				"msg-1": map[string]any{"message": map[string]any{"author": map[string]any{"role": "tool"}, "metadata": map[string]any{"async_task_type": "image_gen"}, "content": map[string]any{"content_type": "multimodal_text", "parts": []any{map[string]any{"asset_pointer": "file-service://file-1"}}}}, "create_time": 1},
			}})
		case r.URL.Path == "/backend-api/files/file-1/download" || r.URL.Path == "/backend-api/files/download/file-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"download_url": "http://" + r.Host + "/assets/file-1.png"})
		case r.URL.Path == "/backend-api/conversation/conv-1/attachment/sed-1/download":
			_ = json.NewEncoder(w).Encode(map[string]any{"download_url": "http://" + r.Host + "/assets/file-1.png"})
		case r.URL.Path == "/assets/file-1.png":
			_, _ = w.Write([]byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 0})
		default:
			http.NotFound(w, r)
		}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	return "http://" + listener.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
}
