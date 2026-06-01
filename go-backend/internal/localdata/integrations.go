package localdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *CPAService) ListPools() []map[string]any {
	return sanitizeCPAPools(s.rawPools())
}

func (s *CPAService) GetPool(id string) map[string]any {
	for _, pool := range s.rawPools() {
		if clean(pool["id"]) == clean(id) {
			return pool
		}
	}
	return nil
}

func (s *CPAService) AddPool(input map[string]any) (map[string]any, error) {
	if clean(input["base_url"]) == "" {
		return nil, errors.New("base_url is required")
	}
	if clean(input["secret_key"]) == "" {
		return nil, errors.New("secret_key is required")
	}
	pools := s.rawPools()
	pool := normalizeCPAPool(mergeMap(input, map[string]any{"id": newID()}), false)
	pools = append(pools, pool)
	_ = saveJSON(s.Path, pools)
	return sanitizeCPAPool(pool), nil
}

func (s *CPAService) UpdatePool(id string, updates map[string]any) (map[string]any, error) {
	pools := s.rawPools()
	for index, pool := range pools {
		if clean(pool["id"]) != clean(id) {
			continue
		}
		next := mergeMap(pool, updates)
		next["id"] = pool["id"]
		pools[index] = normalizeCPAPool(next, false)
		_ = saveJSON(s.Path, pools)
		return sanitizeCPAPool(pools[index]), nil
	}
	return nil, nil
}

func (s *CPAService) DeletePool(id string) bool {
	pools := s.rawPools()
	next := pools[:0]
	removed := false
	for _, pool := range pools {
		if clean(pool["id"]) == clean(id) {
			removed = true
			continue
		}
		next = append(next, pool)
	}
	if removed {
		_ = saveJSON(s.Path, next)
	}
	return removed
}

func (s *CPAService) ImportJob(id string) map[string]any {
	pool := s.GetPool(id)
	if pool == nil {
		return nil
	}
	return normalizeImportJob(pool["import_job"], true)
}

func (s *CPAService) SetImportJob(id string, job map[string]any) map[string]any {
	updated, _ := s.UpdatePool(id, map[string]any{"import_job": normalizeImportJob(job, false)})
	if updated == nil {
		return nil
	}
	return asMap(updated["import_job"])
}

func (s *CPAService) ListRemoteFiles(ctx context.Context, id string) ([]map[string]any, error) {
	pool := s.GetPool(id)
	if pool == nil {
		return nil, errors.New("pool not found")
	}
	baseURL := strings.TrimRight(clean(pool["base_url"]), "/")
	secretKey := clean(pool["secret_key"])
	if baseURL == "" || secretKey == "" {
		return []map[string]any{}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v0/management/auth-files", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+secretKey)
	req.Header.Set("Accept", "application/json")
	payload, err := doJSON(req)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, raw := range anyList(payload["files"]) {
		item := asMap(raw)
		name := clean(item["name"])
		if name == "" {
			continue
		}
		out = append(out, map[string]any{"name": name, "email": firstNonEmpty(clean(item["email"]), clean(item["account"]))})
	}
	return out, nil
}

func (s *CPAService) StartImport(ctx context.Context, id string, names []string, accounts AccountProvider) (map[string]any, error) {
	pool := s.GetPool(id)
	if pool == nil {
		return nil, errors.New("pool not found")
	}
	names = cleanStrings(names)
	if len(names) == 0 {
		return nil, errors.New("selected files is required")
	}
	job := newImportJob(len(names))
	s.SetImportJob(id, job)
	go s.runImport(context.Background(), id, pool, names, accounts)
	return job, nil
}

func (s *CPAService) runImport(ctx context.Context, id string, pool map[string]any, names []string, accounts AccountProvider) {
	job := s.ImportJob(id)
	job["status"] = "running"
	job["updated_at"] = nowISO()
	s.SetImportJob(id, job)
	tokens := []string{}
	errorsOut := []any{}
	for _, name := range names {
		token, err := fetchCPAAccessToken(ctx, pool, name)
		if err != nil {
			errorsOut = append(errorsOut, map[string]any{"name": name, "error": err.Error()})
		} else {
			tokens = append(tokens, token)
		}
		job = s.ImportJob(id)
		job["completed"] = intValue(job["completed"], 0) + 1
		job["failed"] = len(errorsOut)
		job["errors"] = errorsOut
		job["updated_at"] = nowISO()
		s.SetImportJob(id, job)
	}
	job = s.ImportJob(id)
	if len(tokens) == 0 {
		job["status"] = "failed"
		job["failed"] = len(errorsOut)
		job["errors"] = errorsOut
		job["updated_at"] = nowISO()
		s.SetImportJob(id, job)
		return
	}
	add := accounts.AddAccounts(tokens)
	refresh := accounts.RefreshAccounts(ctx, tokens)
	job["status"] = "completed"
	job["added"] = intValue(add["added"], 0)
	job["skipped"] = intValue(add["skipped"], 0)
	job["refreshed"] = intValue(refresh["refreshed"], 0)
	job["failed"] = len(errorsOut)
	job["errors"] = errorsOut
	job["updated_at"] = nowISO()
	s.SetImportJob(id, job)
}

func (s *CPAService) rawPools() []map[string]any {
	var raw any
	_ = loadJSON(s.Path, &raw)
	if object, ok := raw.(map[string]any); ok && clean(object["base_url"]) != "" {
		return []map[string]any{normalizeCPAPool(object, true)}
	}
	items := []map[string]any{}
	for _, rawItem := range anyList(raw) {
		if item := normalizeCPAPool(asMap(rawItem), true); clean(item["base_url"]) != "" {
			items = append(items, item)
		}
	}
	return items
}

func (s *Sub2APIService) ListServers() []map[string]any {
	return sanitizeSub2APIServers(s.rawServers())
}

func (s *Sub2APIService) GetServer(id string) map[string]any {
	for _, server := range s.rawServers() {
		if clean(server["id"]) == clean(id) {
			return server
		}
	}
	return nil
}

func (s *Sub2APIService) AddServer(input map[string]any) (map[string]any, error) {
	if clean(input["base_url"]) == "" {
		return nil, errors.New("base_url is required")
	}
	if clean(input["api_key"]) == "" && (clean(input["email"]) == "" || clean(input["password"]) == "") {
		return nil, errors.New("email+password or api_key is required")
	}
	servers := s.rawServers()
	server := normalizeSub2APIServer(mergeMap(input, map[string]any{"id": newID()}), false)
	servers = append(servers, server)
	_ = saveJSON(s.Path, servers)
	return sanitizeSub2APIServer(server), nil
}

func (s *Sub2APIService) UpdateServer(id string, updates map[string]any) (map[string]any, error) {
	servers := s.rawServers()
	for index, server := range servers {
		if clean(server["id"]) != clean(id) {
			continue
		}
		next := mergeMap(server, updates)
		next["id"] = server["id"]
		servers[index] = normalizeSub2APIServer(next, false)
		_ = saveJSON(s.Path, servers)
		return sanitizeSub2APIServer(servers[index]), nil
	}
	return nil, nil
}

func (s *Sub2APIService) DeleteServer(id string) bool {
	servers := s.rawServers()
	next := servers[:0]
	removed := false
	for _, server := range servers {
		if clean(server["id"]) == clean(id) {
			removed = true
			continue
		}
		next = append(next, server)
	}
	if removed {
		_ = saveJSON(s.Path, next)
	}
	return removed
}

func (s *Sub2APIService) ImportJob(id string) map[string]any {
	server := s.GetServer(id)
	if server == nil {
		return nil
	}
	return normalizeImportJob(server["import_job"], true)
}

func (s *Sub2APIService) SetImportJob(id string, job map[string]any) map[string]any {
	updated, _ := s.UpdateServer(id, map[string]any{"import_job": normalizeImportJob(job, false)})
	if updated == nil {
		return nil
	}
	return asMap(updated["import_job"])
}

func (s *Sub2APIService) ListRemoteGroups(ctx context.Context, id string) ([]map[string]any, error) {
	return s.listRemotePaged(ctx, id, "/api/v1/admin/groups", map[string]string{}, normalizeSub2APIGroup)
}

func (s *Sub2APIService) ListRemoteAccounts(ctx context.Context, id string) ([]map[string]any, error) {
	return s.listRemotePaged(ctx, id, "/api/v1/admin/accounts", map[string]string{"platform": "openai", "type": "oauth"}, normalizeSub2APIAccount)
}

func (s *Sub2APIService) StartImport(ctx context.Context, id string, accountIDs []string, accounts AccountProvider) (map[string]any, error) {
	server := s.GetServer(id)
	if server == nil {
		return nil, errors.New("server not found")
	}
	accountIDs = cleanStrings(accountIDs)
	if len(accountIDs) == 0 {
		return nil, errors.New("account ids is required")
	}
	job := newImportJob(len(accountIDs))
	s.SetImportJob(id, job)
	go s.runImport(context.Background(), id, server, accountIDs, accounts)
	return job, nil
}

func (s *Sub2APIService) runImport(ctx context.Context, id string, server map[string]any, accountIDs []string, accounts AccountProvider) {
	job := s.ImportJob(id)
	job["status"] = "running"
	job["updated_at"] = nowISO()
	s.SetImportJob(id, job)
	tokens := []string{}
	errorsOut := []any{}
	for _, accountID := range accountIDs {
		token, err := fetchSub2APIAccessToken(ctx, server, accountID)
		if err != nil {
			errorsOut = append(errorsOut, map[string]any{"name": accountID, "error": err.Error()})
		} else {
			tokens = append(tokens, token)
		}
		job = s.ImportJob(id)
		job["completed"] = intValue(job["completed"], 0) + 1
		job["failed"] = len(errorsOut)
		job["errors"] = errorsOut
		job["updated_at"] = nowISO()
		s.SetImportJob(id, job)
	}
	job = s.ImportJob(id)
	if len(tokens) == 0 {
		job["status"] = "failed"
		job["failed"] = len(errorsOut)
		job["errors"] = errorsOut
		job["updated_at"] = nowISO()
		s.SetImportJob(id, job)
		return
	}
	add := accounts.AddAccounts(tokens)
	refresh := accounts.RefreshAccounts(ctx, tokens)
	job["status"] = "completed"
	job["added"] = intValue(add["added"], 0)
	job["skipped"] = intValue(add["skipped"], 0)
	job["refreshed"] = intValue(refresh["refreshed"], 0)
	job["failed"] = len(errorsOut)
	job["errors"] = errorsOut
	job["updated_at"] = nowISO()
	s.SetImportJob(id, job)
}

func (s *Sub2APIService) listRemotePaged(ctx context.Context, id, path string, params map[string]string, normalize func(map[string]any) map[string]any) ([]map[string]any, error) {
	server := s.GetServer(id)
	if server == nil {
		return nil, errors.New("server not found")
	}
	baseURL := strings.TrimRight(clean(server["base_url"]), "/")
	headers, err := sub2APIHeaders(ctx, server)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for page := 1; ; page++ {
		query := url.Values{}
		query.Set("page", fmt.Sprint(page))
		query.Set("page_size", "200")
		for key, value := range params {
			query.Set(key, value)
		}
		if groupID := clean(server["group_id"]); groupID != "" && strings.Contains(path, "/accounts") {
			query.Set("group", groupID)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path+"?"+query.Encode(), nil)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		payload, err := doJSON(req)
		if err != nil {
			return nil, err
		}
		items, total := extractPagedItems(payload)
		for _, raw := range items {
			if item := normalize(asMap(raw)); len(item) > 0 {
				out = append(out, item)
			}
		}
		if len(items) == 0 || page*200 >= total || len(items) < 200 {
			break
		}
	}
	return out, nil
}

func (s *Sub2APIService) rawServers() []map[string]any {
	var raw any
	_ = loadJSON(s.Path, &raw)
	items := []map[string]any{}
	for _, rawItem := range anyList(raw) {
		if item := normalizeSub2APIServer(asMap(rawItem), true); clean(item["base_url"]) != "" {
			items = append(items, item)
		}
	}
	return items
}

func normalizeCPAPool(raw map[string]any, failUnfinished bool) map[string]any {
	return map[string]any{
		"id":         firstNonEmpty(clean(raw["id"]), newID()),
		"name":       clean(raw["name"]),
		"base_url":   strings.TrimRight(clean(raw["base_url"]), "/"),
		"secret_key": clean(raw["secret_key"]),
		"import_job": normalizeImportJob(raw["import_job"], failUnfinished),
	}
}

func sanitizeCPAPool(pool map[string]any) map[string]any {
	if pool == nil {
		return nil
	}
	out := copyMap(pool)
	delete(out, "secret_key")
	return out
}

func sanitizeCPAPools(pools []map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, pool := range pools {
		if item := sanitizeCPAPool(pool); item != nil {
			out = append(out, item)
		}
	}
	return out
}

func normalizeSub2APIServer(raw map[string]any, failUnfinished bool) map[string]any {
	return map[string]any{
		"id":         firstNonEmpty(clean(raw["id"]), newID()),
		"name":       clean(raw["name"]),
		"base_url":   strings.TrimRight(clean(raw["base_url"]), "/"),
		"email":      clean(raw["email"]),
		"password":   clean(raw["password"]),
		"api_key":    clean(raw["api_key"]),
		"group_id":   clean(raw["group_id"]),
		"import_job": normalizeImportJob(raw["import_job"], failUnfinished),
	}
}

func sanitizeSub2APIServer(server map[string]any) map[string]any {
	if server == nil {
		return nil
	}
	out := copyMap(server)
	out["has_api_key"] = clean(server["api_key"]) != ""
	delete(out, "password")
	delete(out, "api_key")
	return out
}

func sanitizeSub2APIServers(servers []map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, server := range servers {
		if item := sanitizeSub2APIServer(server); item != nil {
			out = append(out, item)
		}
	}
	return out
}

func normalizeImportJob(raw any, failUnfinished bool) map[string]any {
	item := asMap(raw)
	if len(item) == 0 {
		return nil
	}
	status := firstNonEmpty(clean(item["status"]), "failed")
	if failUnfinished && (status == "pending" || status == "running") {
		status = "failed"
	}
	return map[string]any{
		"job_id":     firstNonEmpty(clean(item["job_id"]), newID()),
		"status":     status,
		"created_at": firstNonEmpty(clean(item["created_at"]), nowISO()),
		"updated_at": firstNonEmpty(clean(item["updated_at"]), clean(item["created_at"]), nowISO()),
		"total":      intValue(item["total"], 0),
		"completed":  intValue(item["completed"], 0),
		"added":      intValue(item["added"], 0),
		"skipped":    intValue(item["skipped"], 0),
		"refreshed":  intValue(item["refreshed"], 0),
		"failed":     intValue(item["failed"], 0),
		"errors":     anyList(item["errors"]),
	}
}

func newImportJob(total int) map[string]any {
	now := nowISO()
	return map[string]any{
		"job_id":     newID(),
		"status":     "pending",
		"created_at": now,
		"updated_at": now,
		"total":      total,
		"completed":  0,
		"added":      0,
		"skipped":    0,
		"refreshed":  0,
		"failed":     0,
		"errors":     []any{},
	}
}

func fetchCPAAccessToken(ctx context.Context, pool map[string]any, name string) (string, error) {
	baseURL := strings.TrimRight(clean(pool["base_url"]), "/")
	secretKey := clean(pool["secret_key"])
	query := url.Values{}
	query.Set("name", name)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v0/management/auth-files/download?"+query.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+secretKey)
	req.Header.Set("Accept", "application/json")
	payload, err := doJSON(req)
	if err != nil {
		return "", err
	}
	token := clean(payload["access_token"])
	if token == "" {
		return "", errors.New("missing access_token")
	}
	return token, nil
}

func sub2APIHeaders(ctx context.Context, server map[string]any) (map[string]string, error) {
	if apiKey := clean(server["api_key"]); apiKey != "" {
		return map[string]string{"x-api-key": apiKey, "Accept": "application/json"}, nil
	}
	baseURL := strings.TrimRight(clean(server["base_url"]), "/")
	email := clean(server["email"])
	password := clean(server["password"])
	if baseURL == "" || email == "" || password == "" {
		return nil, errors.New("sub2api server requires email+password or api_key")
	}
	body, _ := json.Marshal(map[string]any{"email": email, "password": password})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	payload, err := doJSON(req)
	if err != nil {
		return nil, err
	}
	inner := unwrapEnvelope(payload)
	token := clean(inner["access_token"])
	if token == "" {
		return nil, errors.New("sub2api login did not return access_token")
	}
	return map[string]string{"Authorization": "Bearer " + token, "Accept": "application/json"}, nil
}

func fetchSub2APIAccessToken(ctx context.Context, server map[string]any, accountID string) (string, error) {
	baseURL := strings.TrimRight(clean(server["base_url"]), "/")
	headers, err := sub2APIHeaders(ctx, server)
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/admin/accounts/"+url.PathEscape(accountID), nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	payload, err := doJSON(req)
	if err != nil {
		return "", err
	}
	account := unwrapEnvelope(payload)
	credentials := asMap(account["credentials"])
	token := firstNonEmpty(clean(credentials["access_token"]), clean(credentials["accessToken"]), clean(credentials["token"]))
	if token == "" {
		return "", errors.New("missing access_token")
	}
	return token, nil
}

func normalizeSub2APIAccount(item map[string]any) map[string]any {
	credentials := asMap(item["credentials"])
	token := firstNonEmpty(clean(credentials["access_token"]), clean(credentials["accessToken"]), clean(credentials["token"]))
	if token == "" {
		return nil
	}
	id := clean(item["id"])
	return map[string]any{
		"id":                firstNonEmpty(id, clean(credentials["chatgpt_account_id"])),
		"name":              clean(item["name"]),
		"email":             firstNonEmpty(clean(credentials["email"]), clean(item["name"])),
		"plan_type":         clean(credentials["plan_type"]),
		"status":            clean(item["status"]),
		"expires_at":        clean(credentials["expires_at"]),
		"has_refresh_token": clean(credentials["refresh_token"]) != "",
	}
}

func normalizeSub2APIGroup(item map[string]any) map[string]any {
	id := clean(item["id"])
	if id == "" {
		return nil
	}
	return map[string]any{
		"id":                   id,
		"name":                 clean(item["name"]),
		"description":          clean(item["description"]),
		"platform":             clean(item["platform"]),
		"status":               clean(item["status"]),
		"account_count":        intValue(item["account_count"], 0),
		"active_account_count": intValue(item["active_account_count"], 0),
	}
}

func doJSON(req *http.Request) (map[string]any, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, trimText(string(body), 200))
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func unwrapEnvelope(payload map[string]any) map[string]any {
	if inner := asMap(payload["data"]); len(inner) > 0 && payload["code"] != nil {
		return inner
	}
	return payload
}

func extractPagedItems(payload map[string]any) ([]any, int) {
	innerAny := any(unwrapEnvelope(payload))
	if list, ok := innerAny.([]any); ok {
		return list, len(list)
	}
	inner := asMap(innerAny)
	for _, key := range []string{"items", "data", "list"} {
		if list := anyList(inner[key]); len(list) > 0 {
			return list, intValue(inner["total"], len(list))
		}
	}
	return []any{}, 0
}

func trimText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
