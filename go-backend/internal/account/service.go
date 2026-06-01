package account

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Store interface {
	LoadAccounts() ([]map[string]any, error)
	SaveAccounts([]map[string]any) error
}

type RemoteRefresher interface {
	FetchRemoteInfo(ctx context.Context, accessToken string) (map[string]any, error)
}

type Service struct {
	mu                      sync.Mutex
	store                   Store
	refresher               RemoteRefresher
	items                   []map[string]any
	index                   int
	imageReservations       map[string]int
	imageAccountConcurrency int
}

func NewService(store Store, imageAccountConcurrency int) (*Service, error) {
	if imageAccountConcurrency < 1 {
		imageAccountConcurrency = 1
	}
	items, err := store.LoadAccounts()
	if err != nil {
		return nil, err
	}
	normalized := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if next := normalizeAccount(item); next != nil {
			normalized = append(normalized, next)
		}
	}
	return &Service{
		store:                   store,
		items:                   normalized,
		imageReservations:       map[string]int{},
		imageAccountConcurrency: imageAccountConcurrency,
	}, nil
}

func (s *Service) SetRemoteRefresher(refresher RemoteRefresher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresher = refresher
}

func (s *Service) ListAccounts() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return publicAccounts(s.items)
}

func (s *Service) GetAccount(accessToken string) map[string]any {
	accessToken = clean(accessToken)
	if accessToken == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.findIndexLocked(accessToken)
	if index < 0 {
		return nil
	}
	return copyMap(s.items[index])
}

func (s *Service) ListTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.items))
	for _, item := range s.items {
		if token := clean(item["access_token"]); token != "" {
			out = append(out, token)
		}
	}
	return out
}

func (s *Service) ListTokensByIDs(ids []string) []string {
	targets := map[string]struct{}{}
	for _, id := range ids {
		if id = clean(id); id != "" {
			targets[id] = struct{}{}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, item := range s.items {
		token := clean(item["access_token"])
		if token == "" {
			continue
		}
		if _, ok := targets[AccountID(token)]; ok {
			out = append(out, token)
		}
	}
	return out
}

func (s *Service) AddAccounts(tokens []string) map[string]any {
	cleaned := cleanTokens(tokens)
	if len(cleaned) == 0 {
		return map[string]any{"added": 0, "skipped": 0, "items": s.ListAccounts()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	indexed := map[string]map[string]any{}
	order := make([]string, 0, len(s.items)+len(cleaned))
	for _, item := range s.items {
		token := clean(item["access_token"])
		if token == "" {
			continue
		}
		indexed[token] = copyMap(item)
		order = append(order, token)
	}
	added, skipped := 0, 0
	for _, token := range cleaned {
		current, ok := indexed[token]
		if ok {
			skipped++
		} else {
			added++
			current = map[string]any{}
			order = append(order, token)
		}
		next := copyMap(current)
		next["access_token"] = token
		if clean(next["type"]) == "" {
			next["type"] = "Free"
		}
		if normalized := normalizeAccount(next); normalized != nil {
			indexed[token] = normalized
		}
	}
	nextItems := make([]map[string]any, 0, len(order))
	seen := map[string]struct{}{}
	for _, token := range order {
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		if item := indexed[token]; item != nil {
			nextItems = append(nextItems, item)
		}
	}
	s.items = nextItems
	_ = s.saveLocked()
	return map[string]any{"added": added, "skipped": skipped, "items": publicAccounts(s.items)}
}

func (s *Service) DeleteAccounts(tokens []string) map[string]any {
	targets := map[string]struct{}{}
	for _, token := range cleanTokens(tokens) {
		targets[token] = struct{}{}
	}
	if len(targets) == 0 {
		return map[string]any{"removed": 0, "items": s.ListAccounts()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.items[:0]
	removed := 0
	for _, item := range s.items {
		token := clean(item["access_token"])
		if _, ok := targets[token]; ok {
			removed++
			delete(s.imageReservations, token)
			continue
		}
		next = append(next, item)
	}
	s.items = next
	if len(s.items) == 0 {
		s.index = 0
	} else {
		s.index %= len(s.items)
	}
	if removed > 0 {
		_ = s.saveLocked()
	}
	return map[string]any{"removed": removed, "items": publicAccounts(s.items)}
}

func (s *Service) DeleteAccountsByIDs(ids []string) map[string]any {
	return s.DeleteAccounts(s.ListTokensByIDs(ids))
}

func (s *Service) UpdateAccount(accessToken string, updates map[string]any) map[string]any {
	return s.updateAccountFields(accessToken, updates, true)
}

func (s *Service) updateAccountFields(accessToken string, updates map[string]any, manualUpdate bool) map[string]any {
	accessToken = clean(accessToken)
	if accessToken == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.findIndexLocked(accessToken)
	if index < 0 {
		return nil
	}
	next := copyMap(s.items[index])
	for key, value := range updates {
		if manualUpdate {
			switch key {
			case "type", "status", "quota":
				next[key] = value
			}
			continue
		}
		next[key] = value
	}
	normalized := normalizeAccount(next)
	if normalized == nil {
		return nil
	}
	s.items[index] = normalized
	_ = s.saveLocked()
	return publicAccount(normalized)
}

func (s *Service) UpdateAccountByID(id string, updates map[string]any) map[string]any {
	tokens := s.ListTokensByIDs([]string{id})
	if len(tokens) == 0 {
		return nil
	}
	return s.UpdateAccount(tokens[0], updates)
}

func (s *Service) RefreshAccounts(ctx context.Context, tokens []string) map[string]any {
	cleaned := cleanTokens(tokens)
	if len(cleaned) == 0 {
		return map[string]any{"refreshed": 0, "errors": []map[string]string{}, "items": s.ListAccounts()}
	}
	s.mu.Lock()
	refresher := s.refresher
	s.mu.Unlock()
	refreshed := 0
	errorsOut := []map[string]string{}
	if refresher == nil {
		for _, token := range cleaned {
			errorsOut = append(errorsOut, publicError(token, "Go 后端账号远程刷新尚未实现，本阶段仅提供本地账号池能力"))
		}
		return map[string]any{"refreshed": refreshed, "errors": errorsOut, "items": s.ListAccounts()}
	}
	for _, token := range cleaned {
		remoteInfo, err := refresher.FetchRemoteInfo(ctx, token)
		if err != nil {
			errorsOut = append(errorsOut, publicError(token, safeError(token, err.Error())))
			continue
		}
		if item := s.updateAccountFields(token, remoteInfo, false); item != nil {
			refreshed++
		}
	}
	return map[string]any{"refreshed": refreshed, "errors": errorsOut, "items": s.ListAccounts()}
}

func (s *Service) GetAvailableAccessTokenFor(ctx context.Context, allow func(map[string]any) bool) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return "", errors.New("no available account")
	}
	for attempts := 0; attempts < len(s.items); attempts++ {
		index := s.index % len(s.items)
		s.index++
		item := s.items[index]
		if !isTextAccountAvailable(item) {
			continue
		}
		if allow != nil && !allow(copyMap(item)) {
			continue
		}
		return clean(item["access_token"]), nil
	}
	return "", errors.New("no available account")
}

func (s *Service) AcquireImageToken(ctx context.Context, allow func(map[string]any) bool) (string, func(), error) {
	select {
	case <-ctx.Done():
		return "", nil, ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return "", nil, errors.New("no available image quota")
	}
	for attempts := 0; attempts < len(s.items); attempts++ {
		index := s.index % len(s.items)
		s.index++
		item := s.items[index]
		token := clean(item["access_token"])
		if token == "" || !isImageAccountAvailable(item) {
			continue
		}
		if allow != nil && !allow(copyMap(item)) {
			continue
		}
		capacity := s.imageAccountCapacity(item)
		if capacity <= 0 || s.imageReservations[token] >= capacity {
			continue
		}
		s.imageReservations[token]++
		var once sync.Once
		release := func() {
			once.Do(func() {
				s.releaseImageToken(token)
			})
		}
		return token, release, nil
	}
	return "", nil, errors.New("no available image quota")
}

func (s *Service) MarkImageResult(accessToken string, success bool) map[string]any {
	accessToken = clean(accessToken)
	if accessToken == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.findIndexLocked(accessToken)
	if index < 0 {
		return nil
	}
	next := copyMap(s.items[index])
	next["last_used_at"] = time.Now().Format("2006-01-02 15:04:05")
	if success {
		next["success"] = intValue(next["success"], 0) + 1
		if !boolValue(next["image_quota_unknown"], false) {
			next["quota"] = max(0, intValue(next["quota"], 0)-1)
		}
		if !boolValue(next["image_quota_unknown"], false) && intValue(next["quota"], 0) == 0 {
			next["status"] = "限流"
		} else if clean(next["status"]) == "限流" {
			next["status"] = "正常"
		}
	} else {
		next["fail"] = intValue(next["fail"], 0) + 1
	}
	normalized := normalizeAccount(next)
	if normalized == nil {
		return nil
	}
	s.items[index] = normalized
	_ = s.saveLocked()
	return publicAccount(normalized)
}

func (s *Service) releaseImageToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := s.imageReservations[token]
	if count <= 1 {
		delete(s.imageReservations, token)
		return
	}
	s.imageReservations[token] = count - 1
}

func (s *Service) imageAccountCapacity(item map[string]any) int {
	if boolValue(item["image_quota_unknown"], false) {
		return s.imageAccountConcurrency
	}
	return intValue(item["quota"], 0)
}

func (s *Service) findIndexLocked(accessToken string) int {
	for index, item := range s.items {
		if clean(item["access_token"]) == accessToken {
			return index
		}
	}
	return -1
}

func (s *Service) saveLocked() error {
	return s.store.SaveAccounts(s.items)
}

func normalizeAccount(item map[string]any) map[string]any {
	if item == nil {
		return nil
	}
	token := clean(item["access_token"])
	if token == "" {
		return nil
	}
	next := copyMap(item)
	next["access_token"] = token
	next["type"] = firstNonEmpty(clean(next["type"]), "Free")
	next["status"] = firstNonEmpty(clean(next["status"]), "正常")
	next["quota"] = max(0, intValue(next["quota"], 0))
	next["image_quota_unknown"] = boolValue(next["image_quota_unknown"], false)
	next["email"] = optionalString(next["email"])
	next["user_id"] = optionalString(next["user_id"])
	if _, ok := next["limits_progress"].([]any); !ok {
		next["limits_progress"] = []any{}
	}
	next["default_model_slug"] = optionalString(next["default_model_slug"])
	next["restore_at"] = optionalString(next["restore_at"])
	next["success"] = intValue(next["success"], 0)
	next["fail"] = intValue(next["fail"], 0)
	if clean(next["refresh_token"]) == "" {
		next["refresh_token"] = nil
	}
	return next
}

func publicAccounts(items []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if public := publicAccount(item); public != nil {
			out = append(out, public)
		}
	}
	return out
}

func publicAccount(item map[string]any) map[string]any {
	token := clean(item["access_token"])
	if token == "" {
		return nil
	}
	return map[string]any{
		"id":                 AccountID(token),
		"token_preview":      TokenPreview(token),
		"type":               firstNonEmpty(clean(item["type"]), "Free"),
		"status":             firstNonEmpty(clean(item["status"]), "正常"),
		"quota":              intValue(item["quota"], 0),
		"image_quota_unknown": boolValue(item["image_quota_unknown"], false),
		"imageQuotaUnknown":  boolValue(item["image_quota_unknown"], false),
		"email":              nullable(item["email"]),
		"user_id":            nullable(item["user_id"]),
		"limits_progress":    listValue(item["limits_progress"]),
		"default_model_slug": nullable(item["default_model_slug"]),
		"restore_at":         nullable(item["restore_at"]),
		"restoreAt":          nullable(item["restore_at"]),
		"success":            intValue(item["success"], 0),
		"fail":               intValue(item["fail"], 0),
		"last_used_at":       nullable(item["last_used_at"]),
		"lastUsedAt":         nullable(item["last_used_at"]),
	}
}

func publicError(token, message string) map[string]string {
	return map[string]string{
		"account_id":    AccountID(token),
		"token_preview": TokenPreview(token),
		"error":         message,
	}
}

func AccountID(accessToken string) string {
	sum := sha1.Sum([]byte(accessToken))
	return hex.EncodeToString(sum[:])[:16]
}

func TokenPreview(accessToken string) string {
	if len(accessToken) <= 18 {
		return accessToken
	}
	return accessToken[:16] + "..." + accessToken[len(accessToken)-8:]
}

func cleanTokens(tokens []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, token := range tokens {
		token = clean(token)
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func isTextAccountAvailable(item map[string]any) bool {
	switch clean(item["status"]) {
	case "禁用", "异常":
		return false
	default:
		return clean(item["access_token"]) != ""
	}
}

func isImageAccountAvailable(item map[string]any) bool {
	switch clean(item["status"]) {
	case "禁用", "异常", "限流":
		return false
	}
	if boolValue(item["image_quota_unknown"], false) {
		return clean(item["access_token"]) != ""
	}
	return intValue(item["quota"], 0) > 0 && clean(item["access_token"]) != ""
}

func safeError(token, message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "refresh failed"
	}
	return strings.ReplaceAll(message, token, "[access_token]")
}

func copyMap(item map[string]any) map[string]any {
	out := make(map[string]any, len(item))
	for key, value := range item {
		out[key] = value
	}
	return out
}

func clean(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func optionalString(value any) any {
	if text := clean(value); text != "" {
		return text
	}
	return nil
}

func nullable(value any) any {
	if text := clean(value); text != "" {
		return text
	}
	return nil
}

func listValue(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	return []any{}
}

func intValue(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil {
			return n
		}
	}
	return fallback
}

func boolValue(value any, fallback bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
