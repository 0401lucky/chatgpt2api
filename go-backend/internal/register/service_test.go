package register

import (
	"context"
	"testing"
)

func TestServiceSnapshotMergesLiveAccountPoolMetrics(t *testing.T) {
	accounts := &fakeAccountProvider{items: []map[string]any{
		{"status": "正常", "quota": 10},
		{"status": "正常", "quota": 5},
		{"status": "正常", "quota": 99, "image_quota_unknown": true},
		{"status": "限流", "quota": 100},
	}}
	service := NewService(t.TempDir()+"/register.json", t.TempDir()+"/mail_domain_reputation.json", accounts)

	stats := asMap(service.Get()["stats"])
	if got := intValue(stats["current_available"], 0); got != 3 {
		t.Fatalf("current_available = %d", got)
	}
	if got := intValue(stats["current_quota"], 0); got != 15 {
		t.Fatalf("current_quota = %d", got)
	}

	accounts.items = append(accounts.items, map[string]any{"status": "正常", "quota": 7})
	stats = asMap(service.Get()["stats"])
	if got := intValue(stats["current_available"], 0); got != 4 {
		t.Fatalf("updated current_available = %d", got)
	}
	if got := intValue(stats["current_quota"], 0); got != 22 {
		t.Fatalf("updated current_quota = %d", got)
	}
}

type fakeAccountProvider struct {
	items []map[string]any
}

func (f *fakeAccountProvider) ListAccounts() []map[string]any {
	out := make([]map[string]any, len(f.items))
	for i, item := range f.items {
		out[i] = copyMap(item)
	}
	return out
}

func (f *fakeAccountProvider) AddAccounts(tokens []string) map[string]any {
	return map[string]any{"added": len(tokens)}
}

func (f *fakeAccountProvider) RefreshAccounts(ctx context.Context, tokens []string) map[string]any {
	return map[string]any{"refreshed": len(tokens)}
}
