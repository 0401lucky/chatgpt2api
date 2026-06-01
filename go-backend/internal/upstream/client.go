package upstream

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"chatgpt2api-go-backend/internal/proxy"
)

const (
	defaultBaseURL           = "https://chatgpt.com"
	defaultClientVersion     = "prod-be885abbfcfe7b1f511e88b3003d9ee44757fbad"
	defaultClientBuildNumber = "5955942"
	defaultUserAgent         = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36 Edg/143.0.0.0"
	defaultSecCHUA           = `"Microsoft Edge";v="143", "Chromium";v="143", "Not A(Brand";v="24"`
	defaultProfile           = "edge101"
)

type AccountLookup interface {
	GetAccount(accessToken string) map[string]any
}

type Service struct {
	BaseURL              string
	Lookup               AccountLookup
	Proxy                *proxy.Service
	ImagePollTimeout     time.Duration
	ImagePollInitialWait time.Duration
	ImagePollInterval    time.Duration
}

func NewService(lookup AccountLookup, proxyService *proxy.Service) *Service {
	return &Service{BaseURL: defaultBaseURL, Lookup: lookup, Proxy: proxyService}
}

func (s *Service) NewClient(accessToken string) *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	client := &Client{
		BaseURL:           baseURL,
		ClientVersion:     defaultClientVersion,
		ClientBuildNumber: defaultClientBuildNumber,
		AccessToken:       strings.TrimSpace(accessToken),
		Lookup:            s.Lookup,
	}
	client.fp = client.buildFingerprint()
	client.applyBrowserFingerprint()
	client.userAgent = client.fp["user-agent"]
	client.deviceID = client.fp["oai-device-id"]
	client.sessionID = client.fp["oai-session-id"]
	if s.Proxy != nil {
		client.HTTPClient = s.Proxy.BrowserHTTPClientWithProfile(client.fp["impersonate"], 300*time.Second)
	}
	if client.HTTPClient == nil {
		client.HTTPClient = (&proxy.Service{}).BrowserHTTPClientWithProfile(client.fp["impersonate"], 300*time.Second)
	}
	return client
}

func (s *Service) FetchRemoteInfo(ctx context.Context, accessToken string) (map[string]any, error) {
	return s.NewClient(accessToken).FetchRemoteInfo(ctx)
}

func (s *Service) ListModels(ctx context.Context) (map[string]any, error) {
	return s.NewClient("").ListModels(ctx)
}

type Client struct {
	BaseURL           string
	ClientVersion     string
	ClientBuildNumber string
	AccessToken       string
	Lookup            AccountLookup
	HTTPClient        *http.Client

	fp           map[string]string
	userAgent    string
	deviceID     string
	sessionID    string
	powSources   []string
	powDataBuild string
}

func NewTestClient(baseURL, accessToken string, lookup AccountLookup, httpClient *http.Client) *Client {
	client := &Client{
		BaseURL:           strings.TrimRight(baseURL, "/"),
		ClientVersion:     defaultClientVersion,
		ClientBuildNumber: defaultClientBuildNumber,
		AccessToken:       strings.TrimSpace(accessToken),
		Lookup:            lookup,
		HTTPClient:        httpClient,
	}
	client.fp = client.buildFingerprint()
	client.applyBrowserFingerprint()
	client.userAgent = client.fp["user-agent"]
	client.deviceID = client.fp["oai-device-id"]
	client.sessionID = client.fp["oai-session-id"]
	return client
}

func (c *Client) FetchRemoteInfo(ctx context.Context) (map[string]any, error) {
	if c.AccessToken == "" {
		return nil, fmt.Errorf("access_token is required")
	}
	mePayload, err := c.getJSON(ctx, "/backend-api/me", "/backend-api/me")
	if err != nil {
		return nil, err
	}
	initPayload, err := c.postJSONPayload(ctx, "/backend-api/conversation/init", map[string]any{
		"gizmo_id": nil, "requested_default_model": nil, "conversation_id": nil, "timezone_offset_min": -480,
	}, "/backend-api/conversation/init")
	if err != nil {
		return nil, err
	}
	limits := anyList(initPayload["limits_progress"])
	quota, restoreAt, unknown := extractQuotaAndRestoreAt(limits)
	accountType := detectAccountType(c.AccessToken, mePayload, initPayload)
	status := "正常"
	if !unknown && quota == 0 {
		status = "限流"
	}
	return map[string]any{
		"email":               mePayload["email"],
		"user_id":             mePayload["id"],
		"chatgpt_account_id":  firstNonEmpty(chatGPTAccountIDFromPayload(decodeAccessTokenPayload(c.AccessToken)), cleanString(mePayload["chatgpt_account_id"]), cleanString(mePayload["account_id"]), cleanString(mePayload["id"])),
		"type":                accountType,
		"quota":               quota,
		"image_quota_unknown": unknown,
		"limits_progress":     limits,
		"default_model_slug":  initPayload["default_model_slug"],
		"restore_at":          restoreAt,
		"status":              status,
	}, nil
}

func (c *Client) ListModels(ctx context.Context) (map[string]any, error) {
	if err := c.bootstrap(ctx); err != nil {
		return nil, err
	}
	path := "/backend-anon/models?iim=false&is_gizmo=false"
	route := "/backend-anon/models"
	if c.AccessToken != "" {
		path = "/backend-api/models?history_and_training_disabled=false"
		route = "/backend-api/models"
	}
	payload, err := c.getJSON(ctx, path, route)
	if err != nil {
		return nil, err
	}
	models := anyList(payload["models"])
	data := make([]map[string]any, 0, len(models)+len(localModelIDs()))
	seen := map[string]struct{}{}
	for _, raw := range models {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		slug := cleanString(item["slug"])
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		data = append(data, modelItem(slug, intValue(item["created"], 0), firstNonEmpty(cleanString(item["owned_by"]), "chatgpt")))
	}
	for _, id := range localModelIDs() {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		data = append(data, modelItem(id, 0, "chatgpt2api"))
	}
	sort.Slice(data, func(i, j int) bool { return cleanString(data[i]["id"]) < cleanString(data[j]["id"]) })
	return map[string]any{"object": "list", "data": data}, nil
}

func (c *Client) bootstrap(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/", nil)
	for key, value := range c.bootstrapHeaders() {
		req.Header.Set(key, value)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return upstreamHTTPError("bootstrap", resp.StatusCode, body)
	}
	c.powSources, c.powDataBuild = parsePOWResources(string(body))
	if len(c.powSources) == 0 {
		c.powSources = []string{defaultPOWScript}
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, path, route string) (map[string]any, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	for key, value := range c.headers(route, nil) {
		req.Header.Set(key, value)
	}
	return c.doJSON(req, route)
}

func (c *Client) postJSONPayload(ctx context.Context, path string, payload any, route string) (map[string]any, error) {
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	for key, value := range c.headers(route, map[string]string{"Content-Type": "application/json"}) {
		req.Header.Set(key, value)
	}
	return c.doJSON(req, route)
}

func (c *Client) doJSON(req *http.Request, contextName string) (map[string]any, error) {
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w", contextName, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, upstreamHTTPError(contextName, resp.StatusCode, body)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (c *Client) buildFingerprint() map[string]string {
	account := map[string]any{}
	if c.AccessToken != "" && c.Lookup != nil {
		account = c.Lookup.GetAccount(c.AccessToken)
	}
	fp := map[string]string{}
	if raw, ok := account["fp"].(map[string]any); ok {
		for key, value := range raw {
			if text := cleanString(value); text != "" {
				fp[strings.ToLower(key)] = text
			}
		}
	}
	for _, key := range []string{"user-agent", "impersonate", "oai-device-id", "oai-session-id", "sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform"} {
		if value := cleanString(account[key]); value != "" {
			fp[key] = value
		}
	}
	setDefault(fp, "user-agent", defaultUserAgent)
	setDefault(fp, "impersonate", defaultProfile)
	setDefault(fp, "oai-device-id", newUUID())
	setDefault(fp, "oai-session-id", newUUID())
	setDefault(fp, "sec-ch-ua-mobile", "?0")
	setDefault(fp, "sec-ch-ua-platform", `"Windows"`)
	return fp
}

func (c *Client) applyBrowserFingerprint() {
	setDefault(c.fp, "sec-ch-ua", browserMetadataFromUserAgent(c.fp["user-agent"]).secCHUA)
	setDefault(c.fp, "sec-ch-ua-arch", `"x86"`)
	setDefault(c.fp, "sec-ch-ua-bitness", `"64"`)
	setDefault(c.fp, "sec-ch-ua-full-version", `"143.0.3650.96"`)
	setDefault(c.fp, "sec-ch-ua-full-version-list", `"Microsoft Edge";v="143.0.3650.96", "Chromium";v="143.0.7499.147", "Not A(Brand";v="24.0.0.0"`)
	setDefault(c.fp, "sec-ch-ua-platform-version", `"19.0.0"`)
}

func (c *Client) headers(route string, extra map[string]string) map[string]string {
	headers := map[string]string{
		"User-Agent":                  c.userAgent,
		"Origin":                      c.BaseURL,
		"Referer":                     c.BaseURL + "/",
		"Accept-Language":             "zh-CN,zh;q=0.9,en;q=0.8,en-US;q=0.7",
		"Cache-Control":               "no-cache",
		"Pragma":                      "no-cache",
		"Priority":                    "u=1, i",
		"Sec-Ch-Ua":                   c.fp["sec-ch-ua"],
		"Sec-Ch-Ua-Arch":              c.fp["sec-ch-ua-arch"],
		"Sec-Ch-Ua-Bitness":           c.fp["sec-ch-ua-bitness"],
		"Sec-Ch-Ua-Full-Version":      c.fp["sec-ch-ua-full-version"],
		"Sec-Ch-Ua-Full-Version-List": c.fp["sec-ch-ua-full-version-list"],
		"Sec-Ch-Ua-Mobile":            c.fp["sec-ch-ua-mobile"],
		"Sec-Ch-Ua-Model":             `""`,
		"Sec-Ch-Ua-Platform":          c.fp["sec-ch-ua-platform"],
		"Sec-Ch-Ua-Platform-Version":  c.fp["sec-ch-ua-platform-version"],
		"Sec-Fetch-Dest":              "empty",
		"Sec-Fetch-Mode":              "cors",
		"Sec-Fetch-Site":              "same-origin",
		"OAI-Device-Id":               c.deviceID,
		"OAI-Session-Id":              c.sessionID,
		"OAI-Language":                "zh-CN",
		"OAI-Client-Version":          c.ClientVersion,
		"OAI-Client-Build-Number":     c.ClientBuildNumber,
		"X-OpenAI-Target-Path":        route,
		"X-OpenAI-Target-Route":       route,
	}
	if c.AccessToken != "" {
		headers["Authorization"] = "Bearer " + c.AccessToken
	}
	for key, value := range extra {
		headers[key] = value
	}
	return headers
}

func (c *Client) bootstrapHeaders() map[string]string {
	return map[string]string{
		"User-Agent":                c.userAgent,
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Accept-Language":           "zh-CN,zh;q=0.9,en;q=0.8",
		"Sec-Ch-Ua":                 c.fp["sec-ch-ua"],
		"Sec-Ch-Ua-Mobile":          c.fp["sec-ch-ua-mobile"],
		"Sec-Ch-Ua-Platform":        c.fp["sec-ch-ua-platform"],
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
		"Upgrade-Insecure-Requests": "1",
	}
}

type browserHeaderMetadata struct {
	secCHUA string
}

func browserMetadataFromUserAgent(userAgent string) browserHeaderMetadata {
	chromeVersion := regexpVersion(userAgent, `Chrome/([0-9]+(?:\.[0-9]+){0,3})`)
	edgeVersion := regexpVersion(userAgent, `Edg[A-Z]*/([0-9]+(?:\.[0-9]+){0,3})`)
	if edgeVersion != "" {
		edgeMajor := majorVersion(edgeVersion)
		chromiumMajor := majorVersion(firstNonEmpty(chromeVersion, edgeVersion))
		return browserHeaderMetadata{secCHUA: fmt.Sprintf(`"Microsoft Edge";v="%s", "Chromium";v="%s", "Not A(Brand";v="24"`, edgeMajor, chromiumMajor)}
	}
	if chromeVersion != "" {
		major := majorVersion(chromeVersion)
		return browserHeaderMetadata{secCHUA: fmt.Sprintf(`"Not:A-Brand";v="99", "Google Chrome";v="%s", "Chromium";v="%s"`, major, major)}
	}
	return browserHeaderMetadata{secCHUA: defaultSecCHUA}
}

func detectAccountType(accessToken string, payloads ...map[string]any) string {
	if authPayload, ok := decodeAccessTokenPayload(accessToken)["https://api.openai.com/auth"].(map[string]any); ok {
		if matched := normalizeAccountType(authPayload["chatgpt_plan_type"]); matched != "" {
			return matched
		}
	}
	for _, payload := range payloads {
		if matched := searchAccountType(payload); matched != "" {
			return matched
		}
	}
	return "Free"
}

func searchAccountType(value any) string {
	switch x := value.(type) {
	case map[string]any:
		for key, item := range x {
			lower := strings.ToLower(strings.TrimSpace(key))
			if strings.Contains(lower, "plan") || strings.Contains(lower, "type") || strings.Contains(lower, "subscription") || strings.Contains(lower, "workspace") || strings.Contains(lower, "tier") {
				if matched := normalizeAccountType(item); matched != "" {
					return matched
				}
				if matched := searchAccountType(item); matched != "" {
					return matched
				}
			}
		}
	case []any:
		for _, item := range x {
			if matched := searchAccountType(item); matched != "" {
				return matched
			}
		}
	}
	return ""
}

func normalizeAccountType(value any) string {
	switch strings.ToLower(strings.TrimSpace(cleanString(value))) {
	case "free":
		return "Free"
	case "plus", "personal":
		return "Plus"
	case "prolite", "pro_lite":
		return "ProLite"
	case "team", "business", "enterprise":
		return "Team"
	case "pro":
		return "Pro"
	default:
		return ""
	}
}

func decodeAccessTokenPayload(accessToken string) map[string]any {
	parts := strings.Split(strings.TrimSpace(accessToken), ".")
	if len(parts) < 2 {
		return map[string]any{}
	}
	payload := parts[1]
	payload += strings.Repeat("=", (4-len(payload)%4)%4)
	data, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		return map[string]any{}
	}
	return out
}

func chatGPTAccountIDFromPayload(payload map[string]any) string {
	if authPayload, ok := payload["https://api.openai.com/auth"].(map[string]any); ok {
		return firstNonEmpty(cleanString(authPayload["chatgpt_account_id"]), cleanString(authPayload["account_id"]))
	}
	return ""
}

func extractQuotaAndRestoreAt(limits []any) (int, any, bool) {
	for _, raw := range limits {
		item, ok := raw.(map[string]any)
		if !ok || cleanString(item["feature_name"]) != "image_gen" {
			continue
		}
		var restore any
		if value := cleanString(item["reset_after"]); value != "" {
			restore = value
		}
		return intValue(item["remaining"], 0), restore, false
	}
	return 0, nil, true
}

func upstreamHTTPError(contextName string, status int, body []byte) error {
	detail := summarizeBody(body)
	if detail == "" {
		return fmt.Errorf("%s failed: HTTP %d", contextName, status)
	}
	return fmt.Errorf("%s failed: HTTP %d, %s", contextName, status, detail)
}

func summarizeBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "cf_chl") || strings.Contains(lower, "challenge-platform") || strings.Contains(lower, "cloudflare") {
		return "upstream returned Cloudflare challenge page; refresh browser fingerprint/session or change proxy"
	}
	if strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype html") || strings.Contains(lower, "<body") {
		return "upstream returned HTML error page"
	}
	const maxBodyDetail = 1024
	if len(text) > maxBodyDetail {
		text = text[:maxBodyDetail] + "...(truncated)"
	}
	return "body=" + text
}

func modelItem(id string, created int, ownedBy string) map[string]any {
	return map[string]any{"id": id, "object": "model", "created": created, "owned_by": ownedBy, "permission": []any{}, "root": id, "parent": nil}
}

func localModelIDs() []string {
	return []string{"auto", "gpt-5", "gpt-5-1", "gpt-5-2", "gpt-5-3", "gpt-5-3-mini", "gpt-5-mini", "gpt-image-2", "codex-gpt-image-2"}
}

func anyList(value any) []any {
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

func cleanString(value any) string {
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

func setDefault(target map[string]string, key, value string) {
	if strings.TrimSpace(target[key]) == "" {
		target[key] = value
	}
}

func regexpVersion(value, pattern string) string {
	match := regexp.MustCompile(pattern).FindStringSubmatch(value)
	if len(match) > 1 {
		return match[1]
	}
	return ""
}

func majorVersion(version string) string {
	if before, _, ok := strings.Cut(version, "."); ok {
		return before
	}
	return version
}

func newUUID() string {
	// 足够用于 OAI-Device-Id / Session-Id 的随机标识；避免首版引入额外依赖。
	now := time.Now().UnixNano()
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", uint32(now), uint16(now>>32), uint16(now>>16), uint16(now>>48), uint64(now)&0xffffffffffff)
}
