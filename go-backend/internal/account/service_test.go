package account

import (
	"context"
	"strings"
	"testing"

	"chatgpt2api-go-backend/internal/storage"
)

func TestListAccountsHidesAccessTokenAndDeleteByID(t *testing.T) {
	service := newTestService(t, 3)
	service.AddAccounts([]string{"token-alpha-1234567890", "token-beta-1234567890"})
	items := service.ListAccounts()
	if len(items) != 2 {
		t.Fatalf("len(items) = %d", len(items))
	}
	if _, ok := items[0]["access_token"]; ok {
		t.Fatalf("public account leaked access_token: %#v", items[0])
	}
	if items[0]["token_preview"] == "" || items[0]["id"] == "" {
		t.Fatalf("public account missing id/preview: %#v", items[0])
	}
	service.DeleteAccountsByIDs([]string{items[0]["id"].(string)})
	if got := len(service.ListAccounts()); got != 1 {
		t.Fatalf("remaining accounts = %d", got)
	}
}

func TestAcquireImageTokenRespectsQuotaAndRelease(t *testing.T) {
	service := newTestService(t, 2)
	service.AddAccounts([]string{"token-alpha-1234567890", "token-beta-1234567890"})
	service.UpdateAccount("token-alpha-1234567890", map[string]any{"quota": 1, "status": "正常"})
	service.UpdateAccount("token-beta-1234567890", map[string]any{"quota": 0, "status": "正常"})
	service.mu.Lock()
	service.items[1]["image_quota_unknown"] = true
	_ = service.saveLocked()
	service.mu.Unlock()

	token1, release1, err := service.AcquireImageToken(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if token1 != "token-alpha-1234567890" {
		t.Fatalf("token1 = %q", token1)
	}
	token2, release2, err := service.AcquireImageToken(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if token2 != "token-beta-1234567890" {
		t.Fatalf("token2 = %q", token2)
	}
	release1()
	release2()

	updated := service.MarkImageResult(token1, true)
	if updated["quota"].(int) != 0 || updated["status"].(string) != "限流" {
		t.Fatalf("updated account = %#v", updated)
	}
}

func TestRefreshWithoutRefresherReturnsSafeErrors(t *testing.T) {
	service := newTestService(t, 3)
	const token = "secret-token-alpha-1234567890"
	service.AddAccounts([]string{token})
	result := service.RefreshAccounts(context.Background(), []string{token})
	errorsOut := result["errors"].([]map[string]string)
	if len(errorsOut) != 1 {
		t.Fatalf("errors = %#v", errorsOut)
	}
	if _, ok := errorsOut[0]["access_token"]; ok {
		t.Fatalf("refresh error leaked access_token key: %#v", errorsOut[0])
	}
	if strings.Contains(errorsOut[0]["error"], token) {
		t.Fatalf("refresh error leaked token value: %#v", errorsOut[0])
	}
}

func newTestService(t *testing.T, imageConcurrency int) *Service {
	t.Helper()
	service, err := NewService(storage.NewJSONStore(t.TempDir()), imageConcurrency)
	if err != nil {
		t.Fatal(err)
	}
	return service
}
