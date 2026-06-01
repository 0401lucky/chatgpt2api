package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"chatgpt2api-go-backend/internal/account"
	"chatgpt2api-go-backend/internal/auth"
	"chatgpt2api-go-backend/internal/config"
	"chatgpt2api-go-backend/internal/imagetask"
	"chatgpt2api-go-backend/internal/protocol"
)

type App struct {
	config   *config.Config
	accounts *account.Service
	auth     *auth.Service
	models   ModelLister
	chat     ConversationStreamer
	image    ImageGenerator
	tasks    *imagetask.Service
	mux      *http.ServeMux
	started  time.Time
}

type ModelLister interface {
	ListModels(ctx context.Context) (map[string]any, error)
}

type ConversationStreamer interface {
	StreamConversation(ctx context.Context, accessToken string, messages []map[string]any, model, prompt string) (<-chan string, <-chan error)
}

type ImageGenerator interface {
	GenerateImage(ctx context.Context, accessToken, prompt, model, size, responseFormat string) ([]map[string]any, error)
}

func New(cfg *config.Config, accounts *account.Service, authService *auth.Service, models ModelLister) *App {
	app := &App{
		config:   cfg,
		accounts: accounts,
		auth:     authService,
		models:   models,
		mux:      http.NewServeMux(),
		started:  time.Now(),
	}
	if chat, ok := models.(ConversationStreamer); ok {
		app.chat = chat
	}
	if imageGenerator, ok := models.(ImageGenerator); ok {
		app.image = imageGenerator
		if tasks, err := imagetask.NewService(
			filepath.Join(cfg.DataDir, "image_tasks.json"),
			accounts,
			imageGenerator,
			cfg.ImageRetentionDays,
			time.Duration(cfg.ImagePollTimeoutSecs+60)*time.Second,
		); err == nil {
			app.tasks = tasks
		}
	}
	app.routes()
	return app
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

func (a *App) routes() {
	a.mux.HandleFunc("/health", a.handleHealth)
	a.mux.HandleFunc("/version", a.handleVersion)
	a.mux.HandleFunc("/auth/login", a.handleLogin)
	a.mux.HandleFunc("/api/accounts", a.handleAccounts)
	a.mux.HandleFunc("/api/accounts/refresh", a.handleAccountsRefresh)
	a.mux.HandleFunc("/api/accounts/update", a.handleAccountsUpdate)
	a.mux.HandleFunc("/api/image-tasks", a.handleImageTasks)
	a.mux.HandleFunc("/api/image-tasks/generations", a.handleImageTaskGenerations)
	a.mux.HandleFunc("/api/image-tasks/edits", a.handleImageTaskEdits)
	a.mux.HandleFunc("/api/creation-tasks", a.handleImageTasks)
	a.mux.HandleFunc("/api/creation-tasks/image-generations", a.handleImageTaskGenerations)
	a.mux.HandleFunc("/api/creation-tasks/image-edits", a.handleImageTaskEdits)
	a.mux.HandleFunc("/v1/models", a.handleModels)
	a.mux.HandleFunc("/v1/chat/completions", a.handleChatCompletions)
	a.mux.HandleFunc("/v1/images/generations", a.handleImagesGenerations)
	a.mux.HandleFunc("/v1/images/edits", a.handleImagesEdits)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"backend":    "go",
		"version":    a.config.Version,
		"uptime_sec": int(time.Since(a.started).Seconds()),
	})
}

func (a *App) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": a.config.Version})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	identity, ok := a.requireIdentity(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"version":    a.config.Version,
		"role":       identity.Role,
		"subject_id": identity.ID,
		"name":       identity.Name,
	})
}

func (a *App) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"items": a.accounts.ListAccounts()})
	case http.MethodPost:
		var body struct {
			Tokens []string `json:"tokens"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		tokens := cleanStrings(body.Tokens)
		if len(tokens) == 0 {
			writeDetailError(w, http.StatusBadRequest, "tokens is required")
			return
		}
		result := a.accounts.AddAccounts(tokens)
		refreshResult := a.accounts.RefreshAccounts(r.Context(), tokens)
		result["refreshed"] = refreshResult["refreshed"]
		result["errors"] = refreshResult["errors"]
		if items, ok := refreshResult["items"]; ok {
			result["items"] = items
		}
		writeJSON(w, http.StatusOK, result)
	case http.MethodDelete:
		var body struct {
			Tokens     []string `json:"tokens"`
			AccountIDs []string `json:"account_ids"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		tokens := cleanStrings(body.Tokens)
		accountIDs := cleanStrings(body.AccountIDs)
		if len(tokens) > 0 {
			writeJSON(w, http.StatusOK, a.accounts.DeleteAccounts(tokens))
			return
		}
		if len(accountIDs) == 0 {
			writeDetailError(w, http.StatusBadRequest, "tokens or account_ids is required")
			return
		}
		if len(a.accounts.ListTokensByIDs(accountIDs)) == 0 {
			writeDetailError(w, http.StatusNotFound, "accounts not found")
			return
		}
		writeJSON(w, http.StatusOK, a.accounts.DeleteAccountsByIDs(accountIDs))
	default:
		writeMethodNotAllowed(w)
	}
}

func (a *App) handleAccountsRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var body struct {
		AccessTokens []string `json:"access_tokens"`
		AccountIDs   []string `json:"account_ids"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	tokens := cleanStrings(body.AccessTokens)
	if len(tokens) == 0 && len(body.AccountIDs) > 0 {
		tokens = a.accounts.ListTokensByIDs(body.AccountIDs)
	}
	if len(tokens) == 0 {
		tokens = a.accounts.ListTokens()
	}
	if len(tokens) == 0 {
		writeDetailError(w, http.StatusBadRequest, "access_tokens or account_ids is required")
		return
	}
	writeJSON(w, http.StatusOK, a.accounts.RefreshAccounts(r.Context(), tokens))
}

func (a *App) handleAccountsUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var body struct {
		AccessToken string  `json:"access_token"`
		AccountID   string  `json:"account_id"`
		Type        *string `json:"type"`
		Status      *string `json:"status"`
		Quota       *int    `json:"quota"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	accessToken := strings.TrimSpace(body.AccessToken)
	accountID := strings.TrimSpace(body.AccountID)
	if accessToken == "" && accountID == "" {
		writeDetailError(w, http.StatusBadRequest, "access_token or account_id is required")
		return
	}
	updates := map[string]any{}
	if body.Type != nil {
		updates["type"] = *body.Type
	}
	if body.Status != nil {
		updates["status"] = *body.Status
	}
	if body.Quota != nil {
		updates["quota"] = *body.Quota
	}
	if len(updates) == 0 {
		writeDetailError(w, http.StatusBadRequest, "还没有检测到改动，请修改后再保存")
		return
	}
	var item map[string]any
	if accessToken != "" {
		item = a.accounts.UpdateAccount(accessToken, updates)
	} else {
		item = a.accounts.UpdateAccountByID(accountID, updates)
	}
	if item == nil {
		writeDetailError(w, http.StatusNotFound, "account not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item, "items": a.accounts.ListAccounts()})
}

func (a *App) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireIdentity(w, r); !ok {
		return
	}
	if a.models == nil {
		writeDetailError(w, http.StatusNotImplemented, "models upstream is not configured")
		return
	}
	result, err := a.models.ListModels(r.Context())
	if err != nil {
		writeDetailError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireIdentity(w, r); !ok {
		return
	}
	if a.chat == nil {
		writeOpenAIError(w, http.StatusNotImplemented, "server_error", "chat completions upstream is not configured")
		return
	}
	var body map[string]any
	if !decodeJSONBody(w, r, &body) {
		return
	}
	token, err := a.accounts.GetAvailableAccessTokenFor(r.Context(), nil)
	if err != nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "no_available_account", err.Error())
		return
	}
	if protocol.IsStream(body) {
		a.streamChatCompletion(w, r, token, body)
		return
	}
	result, err := protocol.ChatCompletion(r.Context(), a.chat, token, body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handleImageTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	identity, ok := a.requireIdentity(w, r)
	if !ok {
		return
	}
	if a.tasks == nil {
		writeDetailError(w, http.StatusNotImplemented, "image task upstream is not configured")
		return
	}
	writeJSON(w, http.StatusOK, a.tasks.ListTasks(identity, splitComma(r.URL.Query().Get("ids"))))
}

func (a *App) handleImageTaskGenerations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	identity, ok := a.requireIdentity(w, r)
	if !ok {
		return
	}
	if a.tasks == nil {
		writeDetailError(w, http.StatusNotImplemented, "image task upstream is not configured")
		return
	}
	var body struct {
		ClientTaskID string `json:"client_task_id"`
		Prompt       string `json:"prompt"`
		Model        string `json:"model"`
		Size         string `json:"size"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	task, err := a.tasks.SubmitGeneration(identity, imagetask.SubmitGenerationRequest{
		ClientTaskID: body.ClientTaskID,
		Prompt:       body.Prompt,
		Model:        body.Model,
		Size:         body.Size,
	})
	if err != nil {
		writeDetailError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (a *App) handleImageTaskEdits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireIdentity(w, r); !ok {
		return
	}
	writeDetailError(w, http.StatusNotImplemented, "Go 后端本阶段先支持文生图任务，图生图任务将在下一阶段迁移")
}

func (a *App) handleImagesGenerations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireIdentity(w, r); !ok {
		return
	}
	if a.image == nil {
		writeOpenAIError(w, http.StatusNotImplemented, "server_error", "image generation upstream is not configured")
		return
	}
	var body struct {
		Prompt         string `json:"prompt"`
		Model          string `json:"model"`
		Size           string `json:"size"`
		N              int    `json:"n"`
		ResponseFormat string `json:"response_format"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "prompt is required")
		return
	}
	if body.N == 0 {
		body.N = 1
	}
	if body.N < 1 || body.N > 4 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "n must be between 1 and 4")
		return
	}
	data := make([]map[string]any, 0, body.N)
	for i := 0; i < body.N; i++ {
		token, release, err := a.accounts.AcquireImageToken(r.Context(), nil)
		if err != nil {
			writeOpenAIError(w, http.StatusServiceUnavailable, "no_available_account", err.Error())
			return
		}
		items, err := a.image.GenerateImage(r.Context(), token, body.Prompt, body.Model, body.Size, body.ResponseFormat)
		release()
		if err != nil {
			a.accounts.MarkImageResult(token, false)
			writeOpenAIError(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		a.accounts.MarkImageResult(token, true)
		data = append(data, items...)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": time.Now().Unix(),
		"data":    data,
	})
}

func (a *App) handleImagesEdits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireIdentity(w, r); !ok {
		return
	}
	writeOpenAIError(w, http.StatusNotImplemented, "server_error", "Go 后端本阶段先支持文生图，图生图将在下一阶段迁移")
}

func (a *App) streamChatCompletion(w http.ResponseWriter, r *http.Request, accessToken string, body map[string]any) {
	chunks, errCh, err := protocol.StreamChatCompletion(r.Context(), a.chat, accessToken, body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for chunk := range chunks {
		writeSSEJSON(w, chunk)
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err := <-errCh; err != nil {
		writeSSEJSON(w, openAIErrorPayload("upstream_error", err.Error()))
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (a *App) requireIdentity(w http.ResponseWriter, r *http.Request) (*auth.Identity, bool) {
	identity := a.auth.AuthenticateBearer(r.Header.Get("Authorization"))
	if identity == nil {
		writeDetailError(w, http.StatusUnauthorized, "密钥无效或已失效，请重新登录")
		return nil, false
	}
	return identity, true
}

func (a *App) requireAdmin(w http.ResponseWriter, r *http.Request) (*auth.Identity, bool) {
	identity, ok := a.requireIdentity(w, r)
	if !ok {
		return nil, false
	}
	if identity.Role != "admin" {
		writeDetailError(w, http.StatusForbidden, "需要管理员权限才能执行这个操作")
		return nil, false
	}
	return identity, true
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err := decoder.Decode(target); err != nil {
		writeDetailError(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeDetailError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"detail": map[string]any{"error": message}})
}

func writeOpenAIError(w http.ResponseWriter, status int, errorType, message string) {
	writeJSON(w, status, openAIErrorPayload(errorType, message))
}

func openAIErrorPayload(errorType, message string) map[string]any {
	if strings.TrimSpace(errorType) == "" {
		errorType = "server_error"
	}
	return map[string]any{"error": map[string]any{"message": message, "type": errorType, "param": nil, "code": errorType}}
}

func writeSSEJSON(w http.ResponseWriter, payload any) {
	data, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	writeDetailError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func splitComma(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return cleanStrings(strings.Split(value, ","))
}

var ErrNotImplemented = errors.New("not implemented")
