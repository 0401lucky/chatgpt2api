package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"chatgpt2api-go-backend/internal/storage"
)

func TestAuthenticateLegacyAdmin(t *testing.T) {
	store := storage.NewJSONStore(t.TempDir())
	service := NewService(store, "admin-key")
	identity := service.AuthenticateBearer("Bearer admin-key")
	if identity == nil || identity.Role != "admin" || identity.ID != "admin" {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestAuthenticateStoredUserKey(t *testing.T) {
	store := storage.NewJSONStore(t.TempDir())
	if err := store.SaveAuthKeys([]map[string]any{{
		"id":       "user-1",
		"name":     "测试用户",
		"role":     "user",
		"enabled":  true,
		"key_hash": testHash("user-key"),
	}}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, "admin-key")
	identity := service.AuthenticateBearer("Bearer user-key")
	if identity == nil || identity.Role != "user" || identity.ID != "user-1" {
		t.Fatalf("identity = %#v", identity)
	}
	items, err := store.LoadAuthKeys()
	if err != nil {
		t.Fatal(err)
	}
	if items[0]["last_used_at"] == nil || items[0]["last_used_at"] == "" {
		t.Fatalf("last_used_at was not updated: %#v", items[0])
	}
}

func TestDisabledKeyIsRejected(t *testing.T) {
	store := storage.NewJSONStore(t.TempDir())
	if err := store.SaveAuthKeys([]map[string]any{{
		"id":       "user-1",
		"role":     "user",
		"enabled":  false,
		"key_hash": testHash("user-key"),
	}}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, "admin-key")
	if identity := service.AuthenticateBearer("Bearer user-key"); identity != nil {
		t.Fatalf("disabled identity = %#v", identity)
	}
}

func testHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
