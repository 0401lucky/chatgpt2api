package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chatgpt2api-go-backend/internal/account"
	"chatgpt2api-go-backend/internal/auth"
	"chatgpt2api-go-backend/internal/config"
	"chatgpt2api-go-backend/internal/storage"
)

func TestAccountsRequireAdminAndHideToken(t *testing.T) {
	app, accounts := newTestApp(t)
	accounts.AddAccounts([]string{"token-alpha-1234567890"})

	unauthorized := httptest.NewRecorder()
	app.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/accounts", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	authorized := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer admin-key")
	app.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d body=%s", authorized.Code, authorized.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(authorized.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	items := body["items"].([]any)
	first := items[0].(map[string]any)
	if _, ok := first["access_token"]; ok {
		t.Fatalf("account leaked access_token: %#v", first)
	}
}

func TestUpdateAndDeleteByAccountID(t *testing.T) {
	app, accounts := newTestApp(t)
	accounts.AddAccounts([]string{"token-alpha-1234567890"})
	id := accounts.ListAccounts()[0]["id"].(string)

	updateBody := []byte(`{"account_id":"` + id + `","status":"禁用"}`)
	update := httptest.NewRecorder()
	updateReq := httptest.NewRequest(http.MethodPost, "/api/accounts/update", bytes.NewReader(updateBody))
	updateReq.Header.Set("Authorization", "Bearer admin-key")
	app.ServeHTTP(update, updateReq)
	if update.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", update.Code, update.Body.String())
	}

	deleteBody := []byte(`{"account_ids":["` + id + `"]}`)
	deleteResp := httptest.NewRecorder()
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/accounts", bytes.NewReader(deleteBody))
	deleteReq.Header.Set("Authorization", "Bearer admin-key")
	app.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", deleteResp.Code, deleteResp.Body.String())
	}
	if len(accounts.ListAccounts()) != 0 {
		t.Fatalf("accounts not deleted: %#v", accounts.ListAccounts())
	}
}

func TestCreateAccountReportsRefreshNotImplemented(t *testing.T) {
	app, _ := newTestApp(t)
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts", bytes.NewReader([]byte(`{"tokens":["token-alpha-1234567890"]}`)))
	req.Header.Set("Authorization", "Bearer admin-key")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["refreshed"].(float64) != 0 {
		t.Fatalf("refreshed = %#v", body["refreshed"])
	}
	if len(body["errors"].([]any)) != 1 {
		t.Fatalf("errors = %#v", body["errors"])
	}
}

func TestModelsRequireAuthAndUseLister(t *testing.T) {
	app, _ := newTestAppWithModels(t, fakeModelLister{})
	unauthorized := httptest.NewRecorder()
	app.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	authorized := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer admin-key")
	app.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d body=%s", authorized.Code, authorized.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(authorized.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["object"] != "list" {
		t.Fatalf("body = %#v", body)
	}
}

func newTestApp(t *testing.T) (*App, *account.Service) {
	return newTestAppWithModels(t, nil)
}

func newTestAppWithModels(t *testing.T, models ModelLister) (*App, *account.Service) {
	t.Helper()
	store := storage.NewJSONStore(t.TempDir())
	accounts, err := account.NewService(store, 3)
	if err != nil {
		t.Fatal(err)
	}
	accounts.AddAccounts([]string{"token-alpha-1234567890"})
	if item := accounts.UpdateAccount("token-alpha-1234567890", map[string]any{"quota": 5, "type": "Plus", "status": "正常"}); item == nil {
		t.Fatal("failed to seed test account")
	}
	cfg := &config.Config{
		AuthKey:                 "admin-key",
		Version:                 "test-version",
		DataDir:                 filepath.Dir(store.AccountsPath),
		ImageAccountConcurrency: 3,
		ImageRetentionDays:      3,
		ImagePollTimeoutSecs:    1,
	}
	return New(cfg, accounts, auth.NewService(store, cfg.AuthKey), models), accounts
}

type fakeModelLister struct{}

func (fakeModelLister) ListModels(context.Context) (map[string]any, error) {
	return map[string]any{"object": "list", "data": []any{}}, nil
}

func TestChatCompletionsUsesAccountPool(t *testing.T) {
	app, accounts := newTestAppWithModels(t, fakeChatBackend{})
	accounts.AddAccounts([]string{"token-alpha-1234567890"})

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{
		"model": "auto",
		"messages": [{"role": "user", "content": "ping"}]
	}`)))
	req.Header.Set("Authorization", "Bearer admin-key")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	choices := body["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != "你好，世界" {
		t.Fatalf("body = %#v", body)
	}
}

type fakeChatBackend struct {
	fakeModelLister
}

func (fakeChatBackend) StreamConversation(ctx context.Context, accessToken string, messages []map[string]any, model, prompt string) (<-chan string, <-chan error) {
	out := make(chan string, 3)
	errCh := make(chan error, 1)
	out <- `{"message":{"author":{"role":"assistant"},"content":{"parts":["你好"]}}}`
	out <- `{"p":"/message/content/parts/0","o":"append","v":"，世界"}`
	out <- "[DONE]"
	close(out)
	errCh <- nil
	close(errCh)
	return out, errCh
}

func TestImageTaskGenerationSubmitsAndPolls(t *testing.T) {
	app, _ := newTestAppWithModels(t, fakeImageBackend{})

	submit := httptest.NewRecorder()
	submitReq := httptest.NewRequest(http.MethodPost, "/api/image-tasks/generations", bytes.NewReader([]byte(`{
		"client_task_id": "task-1",
		"prompt": "一只小猫",
		"model": "gpt-image-2",
		"size": "1:1"
	}`)))
	submitReq.Header.Set("Authorization", "Bearer admin-key")
	app.ServeHTTP(submit, submitReq)
	if submit.Code != http.StatusOK {
		t.Fatalf("submit status = %d body=%s", submit.Code, submit.Body.String())
	}

	var listed map[string]any
	for i := 0; i < 20; i++ {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/image-tasks?ids=task-1,missing", nil)
		req.Header.Set("Authorization", "Bearer admin-key")
		app.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("list status = %d body=%s", resp.Code, resp.Body.String())
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &listed); err != nil {
			t.Fatal(err)
		}
		items := listed["items"].([]any)
		if len(items) == 1 && items[0].(map[string]any)["status"] == "success" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	items := listed["items"].([]any)
	task := items[0].(map[string]any)
	if task["status"] != "success" {
		t.Fatalf("task did not finish: %#v", task)
	}
	missing := listed["missing_ids"].([]any)
	if len(missing) != 1 || missing[0] != "missing" {
		t.Fatalf("missing_ids = %#v", missing)
	}
	data := task["data"].([]any)
	if data[0].(map[string]any)["b64_json"] != "Y2F0" {
		t.Fatalf("data = %#v", data)
	}
}

func TestDirectImageGenerationSupportsB64JSON(t *testing.T) {
	app, _ := newTestAppWithModels(t, fakeImageBackend{})

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader([]byte(`{
		"prompt": "一只小猫",
		"model": "gpt-image-2",
		"response_format": "b64_json"
	}`)))
	req.Header.Set("Authorization", "Bearer admin-key")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	items := body["data"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["b64_json"] != "Y2F0" {
		t.Fatalf("data = %#v", body["data"])
	}
}

func TestImageEditEndpointsReturnNotImplemented(t *testing.T) {
	app, _ := newTestAppWithModels(t, fakeImageBackend{})

	taskResp := httptest.NewRecorder()
	taskReq := httptest.NewRequest(http.MethodPost, "/api/image-tasks/edits", bytes.NewReader([]byte("not multipart")))
	taskReq.Header.Set("Authorization", "Bearer admin-key")
	app.ServeHTTP(taskResp, taskReq)
	if taskResp.Code != http.StatusNotImplemented {
		t.Fatalf("task edit status = %d body=%s", taskResp.Code, taskResp.Body.String())
	}

	openAIResp := httptest.NewRecorder()
	openAIReq := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader([]byte("not multipart")))
	openAIReq.Header.Set("Authorization", "Bearer admin-key")
	app.ServeHTTP(openAIResp, openAIReq)
	if openAIResp.Code != http.StatusNotImplemented {
		t.Fatalf("openai edit status = %d body=%s", openAIResp.Code, openAIResp.Body.String())
	}
}

func TestCreationTaskAliasesWork(t *testing.T) {
	app, _ := newTestAppWithModels(t, fakeImageBackend{})

	submit := httptest.NewRecorder()
	submitReq := httptest.NewRequest(http.MethodPost, "/api/creation-tasks/image-generations", bytes.NewReader([]byte(`{
		"client_task_id": "task-creation-1",
		"prompt": "一只小猫",
		"model": "gpt-image-2",
		"size": "1:1"
	}`)))
	submitReq.Header.Set("Authorization", "Bearer admin-key")
	app.ServeHTTP(submit, submitReq)
	if submit.Code != http.StatusOK {
		t.Fatalf("submit status = %d body=%s", submit.Code, submit.Body.String())
	}

	list := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/creation-tasks?ids=task-creation-1", nil)
	listReq.Header.Set("Authorization", "Bearer admin-key")
	var listed map[string]any
	for i := 0; i < 20; i++ {
		list = httptest.NewRecorder()
		app.ServeHTTP(list, listReq)
		if list.Code != http.StatusOK {
			t.Fatalf("list status = %d body=%s", list.Code, list.Body.String())
		}
		if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
			t.Fatal(err)
		}
		items := listed["items"].([]any)
		if len(items) == 1 && items[0].(map[string]any)["status"] == "success" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("creation task alias did not finish: %#v", listed)
}

type fakeImageBackend struct {
	fakeModelLister
}

func (fakeImageBackend) GenerateImage(ctx context.Context, accessToken, prompt, model, size, responseFormat string) ([]map[string]any, error) {
	item := map[string]any{"url": "https://example.test/cat.png", "revised_prompt": prompt}
	if strings.EqualFold(strings.TrimSpace(responseFormat), "b64_json") {
		item["b64_json"] = "Y2F0"
	}
	return []map[string]any{item}, nil
}
