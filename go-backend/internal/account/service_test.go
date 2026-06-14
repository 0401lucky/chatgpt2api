package account

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestListAccountsIncludesImageInflight(t *testing.T) {
	service := newTestService(t, 2)
	service.AddAccounts([]string{"token-alpha-1234567890"})
	service.UpdateAccount("token-alpha-1234567890", map[string]any{"quota": 2, "status": "正常"})

	_, release, err := service.AcquireImageToken(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	items := service.ListAccounts()
	if got := items[0]["image_inflight"]; got != 1 {
		t.Fatalf("image_inflight = %#v", got)
	}
	if got := items[0]["imageInflight"]; got != 1 {
		t.Fatalf("imageInflight = %#v", got)
	}

	release()
	items = service.ListAccounts()
	if got := items[0]["image_inflight"]; got != 0 {
		t.Fatalf("image_inflight after release = %#v", got)
	}
}

func TestListNormalTokensOnlyReturnsNormalAccounts(t *testing.T) {
	service := newTestService(t, 2)
	service.AddAccounts([]string{"token-alpha-1234567890", "token-beta-1234567890", "token-gamma-1234567890"})
	service.UpdateAccount("token-alpha-1234567890", map[string]any{"status": "正常"})
	service.UpdateAccount("token-beta-1234567890", map[string]any{"status": "限流"})
	service.UpdateAccount("token-gamma-1234567890", map[string]any{"status": "异常"})

	tokens := service.ListNormalTokens()
	if len(tokens) != 1 || tokens[0] != "token-alpha-1234567890" {
		t.Fatalf("normal tokens = %#v", tokens)
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

func TestRefreshAccountsMarksInvalidTokenAbnormal(t *testing.T) {
	service := newTestService(t, 3)
	const token = "secret-token-alpha-1234567890"
	service.AddAccounts([]string{token})
	service.SetRemoteRefresher(&fakeRemoteRefresher{
		err: fmt.Errorf("/backend-api/me failed: HTTP 401, body={\"error\":{\"code\":\"token_invalidated\"}}"),
	})

	result := service.RefreshAccounts(context.Background(), []string{token})
	if result["refreshed"].(int) != 0 {
		t.Fatalf("refreshed = %#v", result["refreshed"])
	}
	errorsOut := result["errors"].([]map[string]string)
	if len(errorsOut) != 1 {
		t.Fatalf("errors = %#v", errorsOut)
	}

	items := result["items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0]["status"] != "异常" || items[0]["quota"] != 0 {
		t.Fatalf("invalid token should be marked abnormal, item = %#v", items[0])
	}
}

func TestMarkInvalidTokenMarksAbnormal(t *testing.T) {
	service := newTestService(t, 3)
	const token = "secret-token-alpha-1234567890"
	service.AddAccounts([]string{token})
	service.UpdateAccount(token, map[string]any{"quota": 5, "status": "正常", "type": "Plus"})

	item := service.MarkInvalidToken(token)
	if item == nil {
		t.Fatal("MarkInvalidToken returned nil")
	}
	if item["status"] != "异常" || item["quota"] != 0 {
		t.Fatalf("invalid token should be abnormal, item = %#v", item)
	}
	if _, _, err := service.AcquireImageToken(context.Background(), nil); err == nil {
		t.Fatal("abnormal account should be skipped by image pool")
	}
	if _, err := service.GetAvailableAccessTokenFor(context.Background(), nil); err == nil {
		t.Fatal("abnormal account should be skipped by text pool")
	}
}

func TestMarkInvalidTokenAutoRemovesAccount(t *testing.T) {
	service := newTestService(t, 3)
	const token = "secret-token-alpha-1234567890"
	service.AddAccounts([]string{token})
	service.UpdateAccount(token, map[string]any{"quota": 5, "status": "正常", "type": "Plus"})
	service.SetAutoRemoveOptions(true, false)

	item := service.MarkInvalidToken(token)
	if item == nil || item["removed"] != true {
		t.Fatalf("invalid token should be removed, item = %#v", item)
	}
	if got := len(service.ListAccounts()); got != 0 {
		t.Fatalf("remaining accounts = %d", got)
	}
	if _, err := service.GetAvailableAccessTokenFor(context.Background(), nil); err == nil {
		t.Fatal("removed account should not be available")
	}
}

func TestMarkImageResultAutoRemovesRateLimitedAccount(t *testing.T) {
	service := newTestService(t, 3)
	const token = "secret-token-alpha-1234567890"
	service.AddAccounts([]string{token})
	service.UpdateAccount(token, map[string]any{"quota": 1, "status": "正常", "type": "Plus"})
	service.SetAutoRemoveOptions(false, true)

	item := service.MarkImageResult(token, true)
	if item == nil || item["removed"] != true {
		t.Fatalf("rate limited account should be removed, item = %#v", item)
	}
	if got := len(service.ListAccounts()); got != 0 {
		t.Fatalf("remaining accounts = %d", got)
	}
	if _, _, err := service.AcquireImageToken(context.Background(), nil); err == nil {
		t.Fatal("removed account should not be available for images")
	}
}

func TestIsInvalidTokenErrorAcceptsRuntime401(t *testing.T) {
	cases := []error{
		fmt.Errorf("/backend-api/conversation failed: HTTP 401, body={\"error\":{\"code\":\"token_invalidated\"}}"),
		fmt.Errorf("auth_chat_requirements failed: HTTP 401"),
		fmt.Errorf("conversation failed: status=401"),
		fmt.Errorf("authentication token has been invalidated"),
		fmt.Errorf("invalid access token"),
	}
	for _, err := range cases {
		if !IsInvalidTokenError(err) {
			t.Fatalf("IsInvalidTokenError(%q) = false", err)
		}
	}
	if IsInvalidTokenError(fmt.Errorf("bootstrap failed: HTTP 403")) {
		t.Fatal("403 should not be treated as invalid token")
	}
}

func TestRefreshAccountsRotatesTokenBeforeMarkingInvalid(t *testing.T) {
	service := newTestService(t, 3)
	const oldToken = "secret-token-alpha-1234567890"
	const newToken = "secret-token-beta-1234567890"
	service.AddAccounts([]string{oldToken})
	service.mu.Lock()
	service.items[0]["refresh_token"] = "refresh-old"
	_ = service.saveLocked()
	service.mu.Unlock()

	refresher := &fakeRemoteRefresher{
		errByToken: map[string]error{
			oldToken: fmt.Errorf("/backend-api/me failed: HTTP 401, body={\"error\":{\"code\":\"token_invalidated\"}}"),
		},
		newTokens: map[string]any{"access_token": newToken, "refresh_token": "refresh-new"},
		infoByToken: map[string]map[string]any{
			newToken: {"quota": 7, "status": "正常", "type": "Plus", "email": "ok@example.com"},
		},
	}
	service.SetRemoteRefresher(refresher)

	result := service.RefreshAccounts(context.Background(), []string{oldToken})
	if result["refreshed"].(int) != 1 {
		t.Fatalf("refreshed = %#v", result["refreshed"])
	}
	if errorsOut := result["errors"].([]map[string]string); len(errorsOut) != 0 {
		t.Fatalf("errors = %#v", errorsOut)
	}
	if len(refresher.refreshCalls) != 1 || refresher.refreshCalls[0] != "refresh-old" {
		t.Fatalf("refresh calls = %#v", refresher.refreshCalls)
	}

	items := result["items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0]["id"] != AccountID(newToken) || items[0]["quota"] != 7 || items[0]["status"] != "正常" {
		t.Fatalf("rotated account = %#v", items[0])
	}
	stored := service.GetAccount(newToken)
	if clean(stored["refresh_token"]) != "refresh-new" {
		t.Fatalf("stored refresh_token = %#v", stored["refresh_token"])
	}
}

func TestRefreshAccountsRunsWithBoundedConcurrency(t *testing.T) {
	service := newTestService(t, 3)
	tokens := make([]string, 20)
	for i := range tokens {
		tokens[i] = fmt.Sprintf("secret-token-%02d-1234567890", i)
	}
	service.AddAccounts(tokens)

	unblock := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(unblock) })
	}
	defer release()
	refresher := &boundedConcurrencyRefresher{
		started: make(chan struct{}, maxRefreshWorkers),
		unblock: unblock,
	}
	service.SetRemoteRefresher(refresher)

	resultCh := make(chan map[string]any, 1)
	go func() {
		resultCh <- service.RefreshAccounts(context.Background(), tokens)
	}()

	for i := 0; i < maxRefreshWorkers; i++ {
		select {
		case <-refresher.started:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for refresh worker %d", i+1)
		}
	}
	if maxActive := refresher.maxActive(); maxActive != maxRefreshWorkers {
		t.Fatalf("max active workers before release = %d, want %d", maxActive, maxRefreshWorkers)
	}

	release()
	select {
	case result := <-resultCh:
		if result["refreshed"].(int) != len(tokens) {
			t.Fatalf("refreshed = %#v", result["refreshed"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for refresh result")
	}
	if maxActive := refresher.maxActive(); maxActive > maxRefreshWorkers {
		t.Fatalf("max active workers = %d, want <= %d", maxActive, maxRefreshWorkers)
	}
}

func TestRefreshAccountsMarksInvalidWhenOAuthRefreshFails(t *testing.T) {
	service := newTestService(t, 3)
	const token = "secret-token-alpha-1234567890"
	service.AddAccounts([]string{token})
	service.mu.Lock()
	service.items[0]["refresh_token"] = "refresh-old"
	_ = service.saveLocked()
	service.mu.Unlock()

	refresher := &fakeRemoteRefresher{
		err:        fmt.Errorf("/backend-api/me failed: HTTP 401, body={\"error\":{\"code\":\"token_invalidated\"}}"),
		refreshErr: fmt.Errorf("oauth_refresh failed: invalid_grant"),
	}
	service.SetRemoteRefresher(refresher)

	result := service.RefreshAccounts(context.Background(), []string{token})
	if result["refreshed"].(int) != 0 {
		t.Fatalf("refreshed = %#v", result["refreshed"])
	}
	if len(refresher.refreshCalls) != 1 || refresher.refreshCalls[0] != "refresh-old" {
		t.Fatalf("refresh calls = %#v", refresher.refreshCalls)
	}
	items := result["items"].([]map[string]any)
	if items[0]["status"] != "异常" || items[0]["quota"] != 0 {
		t.Fatalf("failed oauth refresh should mark invalid account abnormal, item = %#v", items[0])
	}
}

func TestRefreshAccountsDoesNotMarkTransientErrorAbnormal(t *testing.T) {
	service := newTestService(t, 3)
	const token = "secret-token-alpha-1234567890"
	service.AddAccounts([]string{token})
	service.SetRemoteRefresher(&fakeRemoteRefresher{
		err: fmt.Errorf("bootstrap failed: HTTP 403, upstream returned Cloudflare challenge page"),
	})

	result := service.RefreshAccounts(context.Background(), []string{token})
	if result["refreshed"].(int) != 0 {
		t.Fatalf("refreshed = %#v", result["refreshed"])
	}
	items := result["items"].([]map[string]any)
	if items[0]["status"] != "正常" {
		t.Fatalf("transient error should not mark abnormal, item = %#v", items[0])
	}
}

func TestStartRefreshJobReturnsProgressAndCompletes(t *testing.T) {
	service := newTestService(t, 3)
	const token = "secret-token-alpha-1234567890"
	service.AddAccounts([]string{token})
	service.SetRemoteRefresher(&fakeRemoteRefresher{
		info: map[string]any{"quota": 9, "status": "正常", "type": "Team", "email": "job@example.test"},
	})

	job, err := service.StartRefreshJob([]string{token})
	if err != nil {
		t.Fatal(err)
	}
	id := job["id"].(string)
	if id == "" || job["requested"].(int) != 1 {
		t.Fatalf("job = %#v", job)
	}

	var current map[string]any
	for i := 0; i < 20; i++ {
		current = service.GetRefreshJob(id)
		if current != nil && current["status"] == RefreshJobSuccess {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if current == nil || current["status"] != RefreshJobSuccess {
		t.Fatalf("job did not finish: %#v", current)
	}
	if current["completed"].(int) != 1 || current["refreshed"].(int) != 1 || current["failed"].(int) != 0 {
		t.Fatalf("job counters = %#v", current)
	}
	items := service.ListAccounts()
	if items[0]["quota"] != 9 || items[0]["type"] != "Team" || items[0]["email"] != "job@example.test" {
		t.Fatalf("account was not refreshed: %#v", items[0])
	}
}

func TestReloginAccountsRotatesTokenAndKeepsPasswordHidden(t *testing.T) {
	service := newTestService(t, 3)
	const oldToken = "secret-token-alpha-1234567890"
	const newToken = "secret-token-beta-1234567890"
	service.AddAccountItems([]map[string]any{{
		"access_token": oldToken,
		"email":        "user@example.test",
		"password":     "secret-password",
		"status":       "异常",
		"quota":        0,
	}})
	items := service.ListAccounts()
	if items[0]["has_password"] != true {
		t.Fatalf("public account should expose has_password only: %#v", items[0])
	}
	if _, ok := items[0]["password"]; ok {
		t.Fatalf("public account leaked password: %#v", items[0])
	}

	provider := &fakePasswordReloginProvider{
		tokens: map[string]any{
			"access_token":  newToken,
			"refresh_token": "refresh-new",
			"id_token":      "id-new",
		},
	}
	refresher := &fakeRemoteRefresher{
		infoByToken: map[string]map[string]any{
			newToken: {"quota": 7, "status": "正常", "type": "Plus"},
		},
	}
	refreshed, errorsOut := service.reloginCleanedAccounts(context.Background(), []string{oldToken}, provider, refresher, nil)
	if refreshed != 1 || len(errorsOut) != 0 {
		t.Fatalf("relogin result refreshed=%d errors=%#v", refreshed, errorsOut)
	}
	if len(provider.calls) != 1 || provider.calls[0] != "user@example.test" {
		t.Fatalf("provider calls = %#v", provider.calls)
	}
	items = service.ListAccounts()
	if items[0]["id"] != AccountID(newToken) || items[0]["status"] != "正常" || items[0]["quota"] != 7 {
		t.Fatalf("rotated public account = %#v", items[0])
	}
	stored := service.GetAccount(newToken)
	if clean(stored["password"]) != "secret-password" || clean(stored["refresh_token"]) != "refresh-new" || clean(stored["id_token"]) != "id-new" {
		t.Fatalf("stored account = %#v", stored)
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

type fakePasswordReloginProvider struct {
	tokens map[string]any
	err    error
	calls  []string
}

func (f *fakePasswordReloginProvider) LoginWithPassword(ctx context.Context, email, password string, mailbox map[string]any) (map[string]any, error) {
	f.calls = append(f.calls, email)
	if f.err != nil {
		return nil, f.err
	}
	return copyMap(f.tokens), nil
}

type fakeRemoteRefresher struct {
	mu           sync.Mutex
	info         map[string]any
	err          error
	infoByToken  map[string]map[string]any
	errByToken   map[string]error
	newTokens    map[string]any
	refreshErr   error
	fetchCalls   []string
	refreshCalls []string
}

func (f *fakeRemoteRefresher) FetchRemoteInfo(_ context.Context, token string) (map[string]any, error) {
	f.mu.Lock()
	f.fetchCalls = append(f.fetchCalls, token)
	err := f.errByToken[token]
	info := f.infoByToken[token]
	defaultInfo := f.info
	defaultErr := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if info != nil {
		return info, nil
	}
	return defaultInfo, defaultErr
}

func (f *fakeRemoteRefresher) RefreshAccessToken(_ context.Context, refreshToken string) (map[string]any, error) {
	f.mu.Lock()
	f.refreshCalls = append(f.refreshCalls, refreshToken)
	refreshErr := f.refreshErr
	newTokens := f.newTokens
	f.mu.Unlock()
	if refreshErr != nil {
		return nil, refreshErr
	}
	return newTokens, nil
}

type boundedConcurrencyRefresher struct {
	mu      sync.Mutex
	active  int
	max     int
	calls   int
	started chan struct{}
	unblock <-chan struct{}
}

func (f *boundedConcurrencyRefresher) FetchRemoteInfo(ctx context.Context, token string) (map[string]any, error) {
	f.mu.Lock()
	f.active++
	f.calls++
	if f.active > f.max {
		f.max = f.active
	}
	callNumber := f.calls
	f.mu.Unlock()
	if callNumber <= maxRefreshWorkers {
		f.started <- struct{}{}
	}
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	select {
	case <-f.unblock:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return map[string]any{"quota": 1, "status": "正常", "type": "Free", "email": token + "@example.test"}, nil
}

func (f *boundedConcurrencyRefresher) maxActive() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.max
}
