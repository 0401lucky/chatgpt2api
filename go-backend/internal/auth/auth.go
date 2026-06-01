package auth

import (
	"crypto/sha256"
	"crypto/subtle"
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
