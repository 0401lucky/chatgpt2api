package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

type KeyStore interface {
	LoadAuthKeys() ([]map[string]any, error)
	SaveAuthKeys([]map[string]any) error
}

type Identity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type Service struct {
	mu       sync.Mutex
	store    KeyStore
	adminKey string
}

func NewService(store KeyStore, adminKey string) *Service {
	return &Service{store: store, adminKey: strings.TrimSpace(adminKey)}
}

func ExtractBearerToken(authorization string) string {
	scheme, value, ok := strings.Cut(strings.TrimSpace(authorization), " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}

func (s *Service) AuthenticateBearer(authorization string) *Identity {
	return s.Authenticate(ExtractBearerToken(authorization))
}

func (s *Service) Authenticate(rawKey string) *Identity {
	rawKey = strings.TrimSpace(rawKey)
	if rawKey == "" {
		return nil
	}
	if s.adminKey != "" && constantEqual(rawKey, s.adminKey) {
		return &Identity{ID: "admin", Name: "管理员", Role: "admin"}
	}
	candidateHash := hashKey(rawKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.store.LoadAuthKeys()
	if err != nil {
		return nil
	}
	for index, item := range items {
		if !boolValue(item["enabled"], true) {
			continue
		}
		storedHash := clean(item["key_hash"])
		if storedHash == "" || !constantEqual(candidateHash, storedHash) {
			continue
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		items[index]["last_used_at"] = now
		_ = s.store.SaveAuthKeys(items)
		return &Identity{
			ID:   firstNonEmpty(clean(item["id"]), "user"),
			Name: firstNonEmpty(clean(item["name"]), "普通用户"),
			Role: normalizedRole(item["role"]),
		}
	}
	return nil
}

func (s *Service) ListKeys(role string) []map[string]any {
	role = normalizedRole(role)
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.store.LoadAuthKeys()
	if err != nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if normalizedRole(item["role"]) != role {
			continue
		}
		out = append(out, publicKeyItem(item))
	}
	return out
}

func (s *Service) CreateKey(role, name string) (map[string]any, string, error) {
	role = normalizedRole(role)
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.store.LoadAuthKeys()
	if err != nil {
		return nil, "", err
	}
	name, err = s.buildKeyNameLocked(items, role, name, "")
	if err != nil {
		return nil, "", err
	}
	var rawKey string
	var keyHash string
	for {
		rawKey = "sk-" + randomToken(24)
		keyHash, err = s.buildKeyHashLocked(items, rawKey, "")
		if err == nil {
			break
		}
	}
	item := map[string]any{
		"id":           randomHex(12),
		"name":         name,
		"role":         role,
		"enabled":      true,
		"key_hash":     keyHash,
		"created_at":   time.Now().UTC().Format(time.RFC3339Nano),
		"last_used_at": nil,
	}
	items = append(items, item)
	if err := s.store.SaveAuthKeys(items); err != nil {
		return nil, "", err
	}
	return publicKeyItem(item), rawKey, nil
}

func (s *Service) UpdateKey(id string, updates map[string]any, role string) (map[string]any, error) {
	id = clean(id)
	role = normalizedRole(role)
	if id == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.store.LoadAuthKeys()
	if err != nil {
		return nil, err
	}
	for index, item := range items {
		if clean(item["id"]) != id || normalizedRole(item["role"]) != role {
			continue
		}
		next := copyMap(item)
		if rawName, ok := updates["name"]; ok && rawName != nil {
			name, err := s.buildKeyNameLocked(items, role, clean(rawName), id)
			if err != nil {
				return nil, err
			}
			next["name"] = name
		}
		if rawEnabled, ok := updates["enabled"]; ok && rawEnabled != nil {
			next["enabled"] = boolValue(rawEnabled, true)
		}
		if rawKey, ok := updates["key"]; ok && rawKey != nil {
			keyHash, err := s.buildKeyHashLocked(items, clean(rawKey), id)
			if err != nil {
				return nil, err
			}
			next["key_hash"] = keyHash
		}
		items[index] = next
		if err := s.store.SaveAuthKeys(items); err != nil {
			return nil, err
		}
		return publicKeyItem(next), nil
	}
	return nil, nil
}

func (s *Service) DeleteKey(id string, role string) (bool, error) {
	id = clean(id)
	role = normalizedRole(role)
	if id == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.store.LoadAuthKeys()
	if err != nil {
		return false, err
	}
	next := items[:0]
	removed := false
	for _, item := range items {
		if clean(item["id"]) == id && normalizedRole(item["role"]) == role {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if !removed {
		return false, nil
	}
	return true, s.store.SaveAuthKeys(next)
}

func (s *Service) buildKeyHashLocked(items []map[string]any, rawKey, excludeID string) (string, error) {
	rawKey = clean(rawKey)
	if rawKey == "" {
		return "", fmt.Errorf("请输入新的专用密钥")
	}
	if s.adminKey != "" && constantEqual(rawKey, s.adminKey) {
		return "", fmt.Errorf("这个密钥和管理员密钥冲突了，请换一个新的密钥")
	}
	keyHash := hashKey(rawKey)
	for _, item := range items {
		if excludeID != "" && clean(item["id"]) == excludeID {
			continue
		}
		if storedHash := clean(item["key_hash"]); storedHash != "" && constantEqual(storedHash, keyHash) {
			return "", fmt.Errorf("这个专用密钥已经存在，请换一个新的密钥")
		}
	}
	return keyHash, nil
}

func (s *Service) buildKeyNameLocked(items []map[string]any, role, name, excludeID string) (string, error) {
	role = normalizedRole(role)
	name = clean(name)
	if name == "" {
		name = defaultKeyName(role)
		if !hasKeyName(items, role, name, excludeID) {
			return name, nil
		}
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s %d", name, i)
			if !hasKeyName(items, role, candidate, excludeID) {
				return candidate, nil
			}
		}
	}
	if hasKeyName(items, role, name, excludeID) {
		return "", fmt.Errorf("这个名称已经在使用中了，换一个更容易区分的名称吧")
	}
	return name, nil
}

func hashKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func constantEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func normalizedRole(value any) string {
	if strings.EqualFold(clean(value), "admin") {
		return "admin"
	}
	return "user"
}

func publicKeyItem(item map[string]any) map[string]any {
	return map[string]any{
		"id":           firstNonEmpty(clean(item["id"]), randomHex(12)),
		"name":         firstNonEmpty(clean(item["name"]), defaultKeyName(normalizedRole(item["role"]))),
		"role":         normalizedRole(item["role"]),
		"enabled":      boolValue(item["enabled"], true),
		"created_at":   nullableString(item["created_at"]),
		"last_used_at": nullableString(item["last_used_at"]),
	}
}

func defaultKeyName(role string) string {
	if normalizedRole(role) == "admin" {
		return "管理员密钥"
	}
	return "普通用户"
}

func hasKeyName(items []map[string]any, role, name, excludeID string) bool {
	name = clean(name)
	for _, item := range items {
		if excludeID != "" && clean(item["id"]) == excludeID {
			continue
		}
		if normalizedRole(item["role"]) == normalizedRole(role) && clean(item["name"]) == name {
			return true
		}
	}
	return false
}

func nullableString(value any) any {
	if text := clean(value); text != "" {
		return text
	}
	return nil
}

func randomHex(size int) string {
	if size < 1 {
		size = 12
	}
	raw := make([]byte, (size+1)/2)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())[:size]
	}
	return hex.EncodeToString(raw)[:size]
}

func randomToken(size int) string {
	if size < 16 {
		size = 16
	}
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func clean(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		if value == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func copyMap(item map[string]any) map[string]any {
	out := make(map[string]any, len(item))
	for key, value := range item {
		out[key] = value
	}
	return out
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
