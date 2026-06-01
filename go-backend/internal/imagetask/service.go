package imagetask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"chatgpt2api-go-backend/internal/auth"
	"chatgpt2api-go-backend/internal/protocol"
)

const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusError   = "error"
)

type AccountPool interface {
	AcquireImageToken(ctx context.Context, allow func(map[string]any) bool) (string, func(), error)
	MarkImageResult(accessToken string, success bool) map[string]any
}

type Generator interface {
	GenerateImage(ctx context.Context, accessToken, prompt, model, size, responseFormat string) ([]map[string]any, error)
	EditImage(ctx context.Context, accessToken, prompt, model, size, responseFormat string, images []protocol.ImageInput) ([]map[string]any, error)
}

type HistoryRecorder interface {
	SaveHistoryRecord(sourceEndpoint, mode, model, prompt string, data []map[string]any, usage map[string]any)
}

type Service struct {
	mu            sync.RWMutex
	path          string
	accounts      AccountPool
	generator     Generator
	recorder      HistoryRecorder
	retentionDays int
	taskTimeout   time.Duration
	tasks         map[string]*Task
}

type Task struct {
	ID        string           `json:"id"`
	OwnerID   string           `json:"owner_id"`
	Status    string           `json:"status"`
	Mode      string           `json:"mode"`
	Model     string           `json:"model"`
	Size      string           `json:"size,omitempty"`
	CreatedAt string           `json:"created_at"`
	UpdatedAt string           `json:"updated_at"`
	Data      []map[string]any `json:"data,omitempty"`
	Error     string           `json:"error,omitempty"`

	Prompt string `json:"-"`
}

type SubmitGenerationRequest struct {
	ClientTaskID string
	Prompt       string
	Model        string
	Size         string
}

type SubmitEditRequest struct {
	ClientTaskID string
	Prompt       string
	Model        string
	Size         string
	Images       []protocol.ImageInput
}

func NewService(path string, accounts AccountPool, generator Generator, retentionDays int, taskTimeout time.Duration) (*Service, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("image task path is required")
	}
	if retentionDays < 1 {
		retentionDays = 30
	}
	if taskTimeout <= 0 {
		taskTimeout = 5 * time.Minute
	}
	service := &Service{
		path:          path,
		accounts:      accounts,
		generator:     generator,
		retentionDays: retentionDays,
		taskTimeout:   taskTimeout,
		tasks:         map[string]*Task{},
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := service.load(); err != nil {
		return nil, err
	}
	service.recoverUnfinishedLocked()
	service.cleanupLocked()
	if err := service.saveLocked(); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *Service) SetHistoryRecorder(recorder HistoryRecorder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recorder = recorder
}

func (s *Service) SubmitGeneration(identity *auth.Identity, req SubmitGenerationRequest) (map[string]any, error) {
	taskID := clean(req.ClientTaskID)
	if taskID == "" {
		return nil, errors.New("client_task_id is required")
	}
	prompt := clean(req.Prompt)
	if prompt == "" {
		return nil, errors.New("prompt is required")
	}
	model := clean(req.Model)
	if model == "" || model == "auto" {
		model = "gpt-image-2"
	}
	owner := ownerID(identity)
	key := taskKey(owner, taskID)
	now := nowString()

	s.mu.Lock()
	if s.cleanupLocked() {
		_ = s.saveLocked()
	}
	if task := s.tasks[key]; task != nil {
		public := publicTask(task)
		s.mu.Unlock()
		return public, nil
	}
	task := &Task{
		ID:        taskID,
		OwnerID:   owner,
		Status:    StatusQueued,
		Mode:      "generate",
		Model:     model,
		Size:      clean(req.Size),
		Prompt:    prompt,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.tasks[key] = task
	if err := s.saveLocked(); err != nil {
		delete(s.tasks, key)
		s.mu.Unlock()
		return nil, err
	}
	public := publicTask(task)
	s.mu.Unlock()

	go s.runGeneration(key, prompt, model, task.Size)
	return public, nil
}

func (s *Service) SubmitEdit(identity *auth.Identity, req SubmitEditRequest) (map[string]any, error) {
	taskID := clean(req.ClientTaskID)
	if taskID == "" {
		return nil, errors.New("client_task_id is required")
	}
	prompt := clean(req.Prompt)
	if prompt == "" {
		return nil, errors.New("prompt is required")
	}
	if len(req.Images) == 0 {
		return nil, errors.New("image file is required")
	}
	model := clean(req.Model)
	if model == "" || model == "auto" {
		model = "gpt-image-2"
	}
	owner := ownerID(identity)
	key := taskKey(owner, taskID)
	now := nowString()

	s.mu.Lock()
	if s.cleanupLocked() {
		_ = s.saveLocked()
	}
	if task := s.tasks[key]; task != nil {
		public := publicTask(task)
		s.mu.Unlock()
		return public, nil
	}
	task := &Task{
		ID:        taskID,
		OwnerID:   owner,
		Status:    StatusQueued,
		Mode:      "edit",
		Model:     model,
		Size:      clean(req.Size),
		Prompt:    prompt,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.tasks[key] = task
	if err := s.saveLocked(); err != nil {
		delete(s.tasks, key)
		s.mu.Unlock()
		return nil, err
	}
	public := publicTask(task)
	s.mu.Unlock()

	images := append([]protocol.ImageInput(nil), req.Images...)
	go s.runEdit(key, prompt, model, task.Size, images)
	return public, nil
}

func (s *Service) ListTasks(identity *auth.Identity, ids []string) map[string]any {
	owner := ownerID(identity)
	requested := cleanList(ids)

	s.mu.Lock()
	if s.cleanupLocked() {
		_ = s.saveLocked()
	}
	if len(requested) == 0 {
		items := make([]map[string]any, 0)
		for _, task := range s.tasks {
			if task.OwnerID == owner {
				items = append(items, publicTask(task))
			}
		}
		sort.Slice(items, func(i, j int) bool {
			return clean(items[i]["updated_at"]) > clean(items[j]["updated_at"])
		})
		s.mu.Unlock()
		return map[string]any{"items": items, "missing_ids": []string{}}
	}

	items := make([]map[string]any, 0, len(requested))
	missing := make([]string, 0)
	for _, id := range requested {
		task := s.tasks[taskKey(owner, id)]
		if task == nil {
			missing = append(missing, id)
			continue
		}
		items = append(items, publicTask(task))
	}
	s.mu.Unlock()
	return map[string]any{"items": items, "missing_ids": missing}
}

func (s *Service) runGeneration(key, prompt, model, size string) {
	s.updateTask(key, map[string]any{"status": StatusRunning, "error": ""})

	if s.accounts == nil || s.generator == nil {
		s.updateTask(key, map[string]any{
			"status": StatusError,
			"error":  "Go 后端图片生成上游尚未配置",
			"data":   []map[string]any{},
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.taskTimeout)
	defer cancel()
	token, release, err := s.accounts.AcquireImageToken(ctx, nil)
	if err != nil {
		s.updateTask(key, map[string]any{
			"status": StatusError,
			"error":  err.Error(),
			"data":   []map[string]any{},
		})
		return
	}
	defer release()

	data, err := s.generator.GenerateImage(ctx, token, prompt, model, size, "b64_json")
	if err != nil {
		s.accounts.MarkImageResult(token, false)
		s.updateTask(key, map[string]any{
			"status": StatusError,
			"error":  err.Error(),
			"data":   []map[string]any{},
		})
		return
	}
	if len(data) == 0 {
		s.accounts.MarkImageResult(token, false)
		s.updateTask(key, map[string]any{
			"status": StatusError,
			"error":  "上游没有返回图片，请检查账号额度或稍后重试",
			"data":   []map[string]any{},
		})
		return
	}
	s.accounts.MarkImageResult(token, true)
	if s.recorder != nil {
		s.recorder.SaveHistoryRecord("/api/image-tasks/generations", "generate", model, prompt, data, nil)
	}
	s.updateTask(key, map[string]any{"status": StatusSuccess, "data": data, "error": ""})
}

func (s *Service) runEdit(key, prompt, model, size string, images []protocol.ImageInput) {
	s.updateTask(key, map[string]any{"status": StatusRunning, "error": ""})

	if s.accounts == nil || s.generator == nil {
		s.updateTask(key, map[string]any{
			"status": StatusError,
			"error":  "Go 后端图片编辑上游尚未配置",
			"data":   []map[string]any{},
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.taskTimeout)
	defer cancel()
	token, release, err := s.accounts.AcquireImageToken(ctx, nil)
	if err != nil {
		s.updateTask(key, map[string]any{
			"status": StatusError,
			"error":  err.Error(),
			"data":   []map[string]any{},
		})
		return
	}
	defer release()

	data, err := s.generator.EditImage(ctx, token, prompt, model, size, "b64_json", images)
	if err != nil {
		s.accounts.MarkImageResult(token, false)
		s.updateTask(key, map[string]any{
			"status": StatusError,
			"error":  err.Error(),
			"data":   []map[string]any{},
		})
		return
	}
	if len(data) == 0 {
		s.accounts.MarkImageResult(token, false)
		s.updateTask(key, map[string]any{
			"status": StatusError,
			"error":  "上游没有返回图片，请检查账号额度或稍后重试",
			"data":   []map[string]any{},
		})
		return
	}
	s.accounts.MarkImageResult(token, true)
	if s.recorder != nil {
		s.recorder.SaveHistoryRecord("/api/image-tasks/edits", "edit", model, prompt, data, nil)
	}
	s.updateTask(key, map[string]any{"status": StatusSuccess, "data": data, "error": ""})
}

func (s *Service) updateTask(key string, updates map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.tasks[key]
	if task == nil {
		return
	}
	for field, value := range updates {
		switch field {
		case "status":
			task.Status = clean(value)
		case "error":
			task.Error = clean(value)
		case "data":
			if data, ok := value.([]map[string]any); ok {
				task.Data = data
			}
		}
	}
	task.UpdatedAt = nowString()
	_ = s.saveLocked()
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	items := rawTasks(raw)
	for _, item := range items {
		task := normalizeTask(item)
		if task == nil {
			continue
		}
		s.tasks[taskKey(task.OwnerID, task.ID)] = task
	}
	return nil
}

func (s *Service) saveLocked() error {
	items := make([]*Task, 0, len(s.tasks))
	for _, task := range s.tasks {
		items = append(items, task)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	data, err := json.MarshalIndent(map[string]any{"tasks": items}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".image-tasks-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err == nil {
		return nil
	}
	return os.WriteFile(s.path, data, 0o644)
}

func (s *Service) recoverUnfinishedLocked() {
	for _, task := range s.tasks {
		if task.Status == StatusQueued || task.Status == StatusRunning {
			task.Status = StatusError
			task.Error = "服务已重启，未完成的图片任务已中断"
			task.UpdatedAt = nowString()
		}
	}
}

func (s *Service) cleanupLocked() bool {
	cutoff := time.Now().Add(-time.Duration(s.retentionDays) * 24 * time.Hour)
	changed := false
	for key, task := range s.tasks {
		if task.Status != StatusSuccess && task.Status != StatusError {
			continue
		}
		updatedAt, ok := parseTaskTime(task.UpdatedAt)
		if ok && updatedAt.Before(cutoff) {
			delete(s.tasks, key)
			changed = true
		}
	}
	return changed
}

func rawTasks(raw any) []any {
	if object, ok := raw.(map[string]any); ok {
		if list, ok := object["tasks"].([]any); ok {
			return list
		}
	}
	if list, ok := raw.([]any); ok {
		return list
	}
	return nil
}

func normalizeTask(raw any) *Task {
	item, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	id := clean(item["id"])
	owner := clean(item["owner_id"])
	if id == "" || owner == "" {
		return nil
	}
	status := clean(item["status"])
	if status != StatusQueued && status != StatusRunning && status != StatusSuccess && status != StatusError {
		status = StatusError
	}
	task := &Task{
		ID:        id,
		OwnerID:   owner,
		Status:    status,
		Mode:      firstNonEmpty(clean(item["mode"]), "generate"),
		Model:     firstNonEmpty(clean(item["model"]), "gpt-image-2"),
		Size:      clean(item["size"]),
		CreatedAt: firstNonEmpty(clean(item["created_at"]), nowString()),
		UpdatedAt: firstNonEmpty(clean(item["updated_at"]), clean(item["created_at"]), nowString()),
		Error:     clean(item["error"]),
	}
	for _, rawData := range anyList(item["data"]) {
		if dataItem, ok := rawData.(map[string]any); ok {
			task.Data = append(task.Data, dataItem)
		}
	}
	return task
}

func publicTask(task *Task) map[string]any {
	item := map[string]any{
		"id":         task.ID,
		"status":     task.Status,
		"mode":       task.Mode,
		"model":      task.Model,
		"size":       task.Size,
		"created_at": task.CreatedAt,
		"updated_at": task.UpdatedAt,
	}
	if task.Data != nil {
		item["data"] = task.Data
	}
	if task.Error != "" {
		item["error"] = task.Error
	}
	return item
}

func ownerID(identity *auth.Identity) string {
	if identity == nil || strings.TrimSpace(identity.ID) == "" {
		return "anonymous"
	}
	return strings.TrimSpace(identity.ID)
}

func taskKey(owner, id string) string {
	return owner + ":" + id
}

func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = clean(value)
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

func anyList(value any) []any {
	switch list := value.(type) {
	case []any:
		return list
	case []map[string]any:
		out := make([]any, 0, len(list))
		for _, item := range list {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func parseTaskTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func nowString() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

func clean(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
