package register

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	mathrand "math/rand"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	mailProviderMu  sync.Mutex
	mailProviderSeq int
	mailDomainMu    sync.Mutex
	mailDomainSeq   int

	mailCodePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)background-color:\s*#F3F3F3[^>]*>[\s\S]*?(\d{6})[\s\S]*?</p>`),
		regexp.MustCompile(`(?i)(?:Verification code|code is|代码为|验证码)[:\s]*(\d{6})`),
		regexp.MustCompile(`(?is)>\s*(\d{6})\s*<`),
		regexp.MustCompile(`\b(\d{6})\b`),
	}
)

var yydsDefaultDomains = []string{
	"now-sohusports.com", "10011.hzeg.eu.org", "10086.hzeg.eu.org", "dx.jesys.net",
	"mail.sunshine8.site", "mail.wuwang1028.bond", "xiejiang.site", "ai.2026157.xyz",
	"mail.1m1.dpdns.org", "tokenizer.qwen3-30b-a3b.xyz", "15768.xyz", "israeloil.abrdns.com",
	"mmail.wuwang1028.bond", "rs.sdfe.app", "tm.spkun.org", "wyattcloud.vip",
	"xiaolajiao.dedyn.io", "znhyo.dpdns.org", "20220108.xyz", "666162.xyz",
	"a0.engineer", "a0.jesys.net", "app.longlivethepeople.dpdns.org", "dsqcyy.com",
	"email.fibcbxa.shop", "lingeriesceinturesfemme.com", "lsa1230.dpdns.org", "xuicf1r.site",
	"yyds.tadeo.bond", "chatgpt.qwen3-30b-a3b.xyz", "emoij.indevs.in", "579199.xyz",
	"chen-hai.sryze.cc", "lvcaodibeer.com", "mail.0m0.email", "mail.tadeo.bond",
	"mailx.04.mom", "microsoftazureamazonawsibmapplenvidiaoracleciscoadobe.com", "tynxbzz.com",
	"vnn.indevs.in", "xiaolajiao.tech", "yyds.wessvan.com", "blueshieldpharma.com",
	"flowmail.site", "luguangtech.com", "mm.rc0101.site", "r4.sdfe.app", "sn6fk3.nsmjj.tech",
	"tm.488448.xyz", "0m0.app", "21sad.xyz", "908209381.shop", "9l.sdfe.app",
	"api.qwen3-30b-a3b.xyz", "d.729406.xyz", "dschat.asia", "edumail.zenithsr.pro",
	"em.rc0101.site", "hgu717.ninja", "letv377.nsmjj.tech", "misty.indevs.in",
	"ncyc7b.2026157.xyz", "work.2026157.xyz", "xddroot.eu.org", "det.indevs.in", "l8.jesys.net",
}

type mailboxProvider interface {
	CreateMailbox(ctx context.Context, username string) (map[string]any, error)
	FetchLatestMessage(ctx context.Context, mailbox map[string]any) (map[string]any, error)
	Close()
}

type mailSettings struct {
	RequestTimeout time.Duration
	WaitTimeout    time.Duration
	WaitInterval   time.Duration
	UserAgent      string
	Proxy          string
}

type mailProviderFactory struct {
	Reputation *domainReputationStore
}

type baseMailProvider struct {
	client *http.Client
	conf   mailSettings
	entry  map[string]any
}

type gptMailProvider struct {
	baseMailProvider
}

type yydsMailProvider struct {
	baseMailProvider
	reputation *domainReputationStore
}

func (f *mailProviderFactory) CreateMailbox(ctx context.Context, mailConfig map[string]any, username string) (map[string]any, error) {
	enabled, err := enabledMailEntries(mailConfig)
	if err != nil {
		return nil, err
	}
	tried := map[string]struct{}{}
	lastErr := ""
	for range enabled {
		provider, err := f.createProvider(mailConfig, "", "")
		if err != nil {
			return nil, err
		}
		key := providerKey(provider)
		if _, ok := tried[key]; ok {
			provider.Close()
			continue
		}
		tried[key] = struct{}{}
		mailbox, createErr := provider.CreateMailbox(ctx, username)
		provider.Close()
		if createErr == nil {
			return mailbox, nil
		}
		lastErr = createErr.Error()
		return nil, createErr
	}
	if lastErr == "" {
		lastErr = "所有启用的邮箱提供商均无法创建邮箱"
	}
	return nil, fmt.Errorf("%s", lastErr)
}

func (f *mailProviderFactory) WaitForCode(ctx context.Context, mailConfig map[string]any, mailbox map[string]any) (string, error) {
	provider, err := f.createProvider(mailConfig, clean(mailbox["provider"]), clean(mailbox["provider_ref"]))
	if err != nil {
		return "", err
	}
	defer provider.Close()
	conf := mailSettingsFromConfig(mailConfig)
	deadline := time.NewTimer(conf.WaitTimeout)
	defer deadline.Stop()
	for {
		message, fetchErr := provider.FetchLatestMessage(ctx, mailbox)
		if fetchErr == nil && message != nil {
			if code := extractUnseenMailCode(mailbox, message); code != "" {
				return code, nil
			}
		}
		interval := time.NewTimer(conf.WaitInterval)
		select {
		case <-ctx.Done():
			interval.Stop()
			return "", ctx.Err()
		case <-deadline.C:
			interval.Stop()
			return "", nil
		case <-interval.C:
		}
	}
}

func (f *mailProviderFactory) createProvider(mailConfig map[string]any, providerName, providerRef string) (mailboxProvider, error) {
	entry, err := selectMailEntry(mailConfig, providerName, providerRef)
	if err != nil {
		return nil, err
	}
	conf := mailSettingsFromConfig(mailConfig)
	client, err := httpClientForProxy(conf.Proxy, conf.RequestTimeout)
	if err != nil {
		return nil, err
	}
	base := baseMailProvider{client: client, conf: conf, entry: entry}
	switch clean(entry["type"]) {
	case "gptmail":
		return &gptMailProvider{baseMailProvider: base}, nil
	case "yyds_mail":
		return &yydsMailProvider{baseMailProvider: base, reputation: f.Reputation}, nil
	default:
		return nil, fmt.Errorf("当前 Go 注册机暂未支持邮箱 provider: %s，请先使用 gptmail 或 yyds_mail", clean(entry["type"]))
	}
}

func providerKey(provider mailboxProvider) string {
	switch typed := provider.(type) {
	case *gptMailProvider:
		return "gptmail:" + clean(typed.entry["provider_ref"])
	case *yydsMailProvider:
		return "yyds_mail:" + clean(typed.entry["provider_ref"])
	default:
		return fmt.Sprintf("%T", provider)
	}
}

func mailSettingsFromConfig(mailConfig map[string]any) mailSettings {
	return mailSettings{
		RequestTimeout: time.Duration(maxInt(1, intValue(mailConfig["request_timeout"], 60))) * time.Second,
		WaitTimeout:    time.Duration(maxInt(1, intValue(mailConfig["wait_timeout"], 300))) * time.Second,
		WaitInterval:   time.Duration(maxInt(1, intValue(mailConfig["wait_interval"], 5))) * time.Second,
		UserAgent:      firstNonEmpty(clean(mailConfig["user_agent"]), registerUserAgent, "Mozilla/5.0"),
		Proxy:          clean(mailConfig["proxy"]),
	}
}

func mailEntries(mailConfig map[string]any) []map[string]any {
	providers := asMapSlice(mailConfig["providers"])
	out := make([]map[string]any, 0, len(providers))
	counters := map[string]int{}
	for index, item := range providers {
		entry := copyMap(item)
		t := clean(entry["type"])
		counters[t]++
		entry["provider_ref"] = fmt.Sprintf("%s#%d", t, index+1)
		if t == "ddg_mail" {
			entry["label"] = fmt.Sprintf("DDG-%d", counters[t])
		} else {
			entry["label"] = fmt.Sprintf("%s#%d", t, index+1)
		}
		out = append(out, entry)
	}
	return out
}

func enabledMailEntries(mailConfig map[string]any) ([]map[string]any, error) {
	entries := mailEntries(mailConfig)
	out := []map[string]any{}
	for _, entry := range entries {
		if boolValue(entry["enable"], false) {
			out = append(out, entry)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("mail.providers 没有启用的 provider")
	}
	return out, nil
}

func selectMailEntry(mailConfig map[string]any, providerName, providerRef string) (map[string]any, error) {
	entries := mailEntries(mailConfig)
	enabled, err := enabledMailEntries(mailConfig)
	if err != nil {
		return nil, err
	}
	if providerRef != "" {
		for _, entry := range entries {
			if clean(entry["provider_ref"]) == providerRef {
				return copyMap(entry), nil
			}
		}
	}
	if providerName != "" {
		for _, entry := range enabled {
			if clean(entry["type"]) == providerName {
				return copyMap(entry), nil
			}
		}
	}
	if len(enabled) == 1 {
		return copyMap(enabled[0]), nil
	}
	mailProviderMu.Lock()
	entry := copyMap(enabled[mailProviderSeq%len(enabled)])
	mailProviderSeq = (mailProviderSeq + 1) % len(enabled)
	mailProviderMu.Unlock()
	return entry, nil
}

func (p *baseMailProvider) Close() {
	if p.client != nil {
		p.client.CloseIdleConnections()
	}
}

func (p *gptMailProvider) CreateMailbox(ctx context.Context, username string) (map[string]any, error) {
	payload := map[string]any{}
	if username = strings.TrimSpace(username); username != "" {
		payload["prefix"] = username
	}
	if domain := clean(p.entry["default_domain"]); domain != "" {
		payload["domain"] = domain
	}
	method := http.MethodGet
	var requestBody any
	if len(payload) > 0 {
		method = http.MethodPost
		requestBody = payload
	}
	data, err := mailRequestAny(ctx, p.client, method, "https://mail.chatgpt.org.uk/api/generate-email", map[string]string{
		"X-API-Key":    clean(p.entry["api_key"]),
		"User-Agent":   p.conf.UserAgent,
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}, nil, requestBody, http.StatusOK)
	if err != nil {
		return nil, err
	}
	body := asMap(data)
	payloadMap := asMap(firstNonNil(body["data"], data))
	address := clean(payloadMap["email"])
	if address == "" {
		return nil, fmt.Errorf("gptmail response missing email")
	}
	return map[string]any{"provider": "gptmail", "provider_ref": p.entry["provider_ref"], "address": address}, nil
}

func (p *gptMailProvider) FetchLatestMessage(ctx context.Context, mailbox map[string]any) (map[string]any, error) {
	data, err := mailRequestAny(ctx, p.client, http.MethodGet, "https://mail.chatgpt.org.uk/api/emails", map[string]string{
		"X-API-Key":  clean(p.entry["api_key"]),
		"User-Agent": p.conf.UserAgent,
		"Accept":     "application/json",
	}, map[string]string{"email": clean(mailbox["address"])}, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	body := asMap(data)
	if nested := asMap(body["data"]); len(nested) > 0 {
		body = nested
	}
	items := asMapSlice(firstNonNil(body["emails"], data))
	if len(items) == 0 {
		return nil, nil
	}
	latest := latestMailMessage(items)
	if id := clean(latest["id"]); id != "" {
		detail, detailErr := mailRequestAny(ctx, p.client, http.MethodGet, "https://mail.chatgpt.org.uk/api/email/"+url.PathEscape(id), map[string]string{
			"X-API-Key":  clean(p.entry["api_key"]),
			"User-Agent": p.conf.UserAgent,
			"Accept":     "application/json",
		}, nil, nil, http.StatusOK)
		if detailErr == nil {
			if typed, ok := detail.(map[string]any); ok && typed["data"] != nil {
				latest = asMap(typed["data"])
			} else if typed, ok := detail.(map[string]any); ok {
				latest = typed
			}
		}
	}
	textContent, htmlContent := extractMailContent(latest)
	return map[string]any{
		"provider":     "gptmail",
		"mailbox":      clean(mailbox["address"]),
		"message_id":   clean(latest["id"]),
		"subject":      clean(latest["subject"]),
		"sender":       clean(latest["from_address"]),
		"text_content": textContent,
		"html_content": htmlContent,
		"received_at":  firstNonNil(latest["timestamp"], latest["created_at"]),
		"raw":          latest,
	}, nil
}

func (p *yydsMailProvider) CreateMailbox(ctx context.Context, username string) (map[string]any, error) {
	payload := map[string]any{"localPart": firstNonEmpty(strings.TrimSpace(username), randomMailboxName())}
	if domain := p.selectDomain(); domain != "" {
		payload["domain"] = domain
	}
	if subdomain := clean(p.entry["subdomain"]); subdomain != "" {
		payload["subdomain"] = subdomain
	}
	path := "/accounts"
	if boolValue(p.entry["wildcard"], false) {
		path = "/accounts/wildcard"
	}
	data, err := p.request(ctx, http.MethodPost, path, "", nil, payload, http.StatusOK, http.StatusCreated, http.StatusNoContent)
	if err != nil {
		return nil, err
	}
	body := asMap(data)
	address := firstNonEmpty(clean(body["address"]), clean(body["email"]))
	token := firstNonEmpty(clean(body["token"]), clean(body["temp_token"]), clean(body["tempToken"]), clean(body["access_token"]))
	if address == "" || token == "" {
		return nil, fmt.Errorf("YYDSMail 缺少 address 或 token")
	}
	domain := normalizeDomain(address)
	if domain == "" {
		domain = normalizeDomain(clean(payload["domain"]))
	}
	return map[string]any{
		"provider":     "yyds_mail",
		"provider_ref": p.entry["provider_ref"],
		"address":      address,
		"domain":       domain,
		"token":        token,
		"account_id":   clean(body["id"]),
	}, nil
}

func (p *yydsMailProvider) FetchLatestMessage(ctx context.Context, mailbox map[string]any) (map[string]any, error) {
	token := clean(mailbox["token"])
	if token == "" {
		return nil, fmt.Errorf("YYDSMail 缺少 token")
	}
	data, err := p.request(ctx, http.MethodGet, "/messages", token, map[string]string{"address": clean(mailbox["address"])}, nil, http.StatusOK, http.StatusCreated, http.StatusNoContent)
	if err != nil {
		return nil, err
	}
	items := yydsMailItems(data)
	if len(items) == 0 {
		return nil, nil
	}
	latest := latestMailMessage(items)
	messageID := firstNonEmpty(clean(latest["id"]), clean(latest["message_id"]))
	message := latest
	raw := any(latest)
	if messageID != "" {
		detail, detailErr := p.request(ctx, http.MethodGet, "/messages/"+url.PathEscape(messageID), token, map[string]string{"address": clean(mailbox["address"])}, nil, http.StatusOK, http.StatusCreated, http.StatusNoContent)
		if detailErr != nil {
			return nil, detailErr
		}
		raw = detail
		if detailMap := asMap(detail); len(detailMap) > 0 {
			message = detailMap
		}
	}
	textContent, htmlContent := extractMailContent(message)
	sender := firstNonNil(message["from"], message["sender"])
	if senderMap, ok := sender.(map[string]any); ok {
		sender = firstNonNil(senderMap["address"], senderMap["email"], senderMap["name"])
	}
	return map[string]any{
		"provider":     "yyds_mail",
		"mailbox":      clean(mailbox["address"]),
		"message_id":   messageID,
		"subject":      clean(message["subject"]),
		"sender":       clean(sender),
		"text_content": textContent,
		"html_content": htmlContent,
		"received_at":  firstNonNil(message["createdAt"], message["created_at"], message["receivedAt"], message["date"], message["timestamp"]),
		"raw":          raw,
	}, nil
}

func (p *yydsMailProvider) selectDomain() string {
	domainLearning := boolValue(p.entry["domain_learning"], true)
	exploreRate := floatValue(p.entry["domain_explore_rate"], func() float64 {
		if domainLearning {
			return 0.12
		}
		return 0
	}())
	if domainLearning && mathrand.Float64() < exploreRate {
		return ""
	}
	seed := asStringSlice(p.entry["domain"])
	if len(seed) == 0 {
		seed = append([]string(nil), yydsDefaultDomains...)
	}
	domains := []string{}
	if domainLearning && p.reputation != nil {
		domains = append(domains, p.reputation.GoodDomains("yyds_mail")...)
	}
	domains = append(domains, seed...)
	if p.reputation != nil {
		preferred := p.reputation.PreferredDomains("yyds_mail", domains)
		if len(preferred) == 0 {
			return ""
		}
		return nextDomain(preferred)
	}
	return nextDomain(domains)
}

func (p *yydsMailProvider) request(ctx context.Context, method, path, token string, query map[string]string, payload any, expected ...int) (any, error) {
	apiBase := strings.TrimRight(firstNonEmpty(clean(p.entry["api_base"]), "https://maliapi.215.im/v1"), "/")
	headers := map[string]string{
		"User-Agent":   p.conf.UserAgent,
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	} else {
		headers["X-API-Key"] = clean(p.entry["api_key"])
	}
	var lastErr error
	retryStatuses := map[int]bool{429: true, 500: true, 502: true, 503: true, 504: true}
	for attempt := 0; attempt < 3; attempt++ {
		data, status, err := mailRequestAnyWithStatus(ctx, p.client, method, apiBase+path, headers, query, payload, expected...)
		if err == nil {
			body, ok := data.(map[string]any)
			if !ok {
				return data, nil
			}
			if success, exists := body["success"]; exists && !boolValue(success, true) {
				return nil, fmt.Errorf("YYDSMail 请求失败: %s", firstNonEmpty(clean(body["errorCode"]), clean(body["error"]), clean(body["message"]), "unknown error"))
			}
			if nested, exists := body["data"]; exists {
				switch nested.(type) {
				case map[string]any, []any:
					return nested, nil
				}
			}
			return data, nil
		}
		lastErr = err
		if !retryStatuses[status] || attempt == 2 {
			break
		}
		time.Sleep(time.Duration(500*(attempt+1)) * time.Millisecond)
	}
	return nil, lastErr
}

func yydsMailItems(data any) []map[string]any {
	switch typed := data.(type) {
	case []map[string]any:
		return typed
	case []any:
		return asMapSlice(typed)
	case map[string]any:
		return asMapSlice(firstNonNil(typed["items"], typed["messages"], typed["data"], typed["list"]))
	default:
		return nil
	}
}

func mailRequestAny(ctx context.Context, client *http.Client, method, target string, headers map[string]string, query map[string]string, payload any, expected ...int) (any, error) {
	data, _, err := mailRequestAnyWithStatus(ctx, client, method, target, headers, query, payload, expected...)
	return data, err
}

func mailRequestAnyWithStatus(ctx context.Context, client *http.Client, method, target string, headers map[string]string, query map[string]string, payload any, expected ...int) (any, int, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(data)
	}
	if len(query) > 0 {
		parsed, err := url.Parse(target)
		if err != nil {
			return nil, 0, err
		}
		values := parsed.Query()
		for key, value := range query {
			if strings.TrimSpace(value) != "" {
				values.Set(key, value)
			}
		}
		parsed.RawQuery = values.Encode()
		target = parsed.String()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, 0, err
	}
	for key, value := range headers {
		if strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if !expectedStatus(resp.StatusCode, expected...) {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, resp.StatusCode, fmt.Errorf("mail request failed: %s %s -> HTTP %d body=%s", method, target, resp.StatusCode, string(preview))
	}
	if resp.StatusCode == http.StatusNoContent {
		return map[string]any{}, resp.StatusCode, nil
	}
	var data any
	decoder := json.NewDecoder(resp.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&data); err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func expectedStatus(status int, expected ...int) bool {
	for _, item := range expected {
		if status == item {
			return true
		}
	}
	return false
}

func nextDomain(domains []string) string {
	filtered := normalizeDomains(domains)
	if len(filtered) == 0 {
		return ""
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	mailDomainMu.Lock()
	value := filtered[mailDomainSeq%len(filtered)]
	mailDomainSeq = (mailDomainSeq + 1) % len(filtered)
	mailDomainMu.Unlock()
	return value
}

func extractMailCode(message map[string]any) string {
	textContent, htmlContent := extractMailContent(message)
	content := strings.TrimSpace(strings.Join([]string{clean(message["subject"]), textContent, htmlContent}, "\n"))
	if content == "" {
		return ""
	}
	for _, pattern := range mailCodePatterns {
		match := pattern.FindStringSubmatch(content)
		if len(match) > 1 {
			code := strings.TrimSpace(match[1])
			if code != "" && code != "177010" {
				return code
			}
		}
	}
	return ""
}

func extractUnseenMailCode(mailbox map[string]any, message map[string]any) string {
	ref := mailMessageRef(message)
	seen := seenMailRefs(mailbox["_seen_code_message_refs"])
	if ref != "" {
		if _, ok := seen[ref]; ok {
			return ""
		}
	}
	code := extractMailCode(message)
	if code == "" || ref == "" {
		return code
	}
	existing := seenMailRefList(mailbox["_seen_code_message_refs"])
	mailbox["_seen_code_message_refs"] = append(existing, ref)
	return code
}

func seenMailRefs(value any) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range seenMailRefList(value) {
		out[item] = struct{}{}
	}
	return out
}

func seenMailRefList(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if ref := clean(item); ref != "" {
				out = append(out, ref)
			}
		}
		return out
	default:
		return nil
	}
}

func mailMessageRef(message map[string]any) string {
	provider := clean(message["provider"])
	mailbox := clean(message["mailbox"])
	if id := mailMessageID(message); id != "" {
		return "id:" + provider + ":" + mailbox + ":" + id
	}
	textContent, htmlContent := extractMailContent(message)
	received := clean(message["received_at"])
	content := strings.Join([]string{clean(message["subject"]), textContent, htmlContent}, "\n")
	if strings.TrimSpace(content) == "" {
		return ""
	}
	sum := sha1.Sum([]byte(content))
	return fmt.Sprintf("content:%s:%s:%s:%x", provider, mailbox, received, sum[:8])
}

func extractMailContent(data map[string]any) (string, string) {
	textContent := firstNonEmpty(contentString(data["text_content"]), contentString(data["text"]), contentString(data["body"]), contentString(data["content"]))
	htmlContent := firstNonEmpty(contentString(data["html_content"]), contentString(data["html"]), contentString(data["html_body"]), contentString(data["body_html"]))
	if textContent != "" || htmlContent != "" {
		return textContent, htmlContent
	}
	raw, ok := data["raw"].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return "", ""
	}
	textContent, htmlContent = parseRawMail(raw)
	if textContent == "" && htmlContent == "" {
		return raw, ""
	}
	return textContent, htmlContent
}

func contentString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case []string:
		return strings.TrimSpace(strings.Join(typed, ""))
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := contentString(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, ""))
	default:
		return clean(value)
	}
}

func parseRawMail(raw string) (string, string) {
	message, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return raw, ""
	}
	plain, html := parseMIMEBody(message.Header.Get("Content-Type"), message.Header.Get("Content-Transfer-Encoding"), message.Body)
	return strings.TrimSpace(strings.Join(plain, "\n")), strings.TrimSpace(strings.Join(html, "\n"))
}

func parseMIMEBody(contentType, transferEncoding string, body io.Reader) ([]string, []string) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.ToLower(strings.Split(contentType, ";")[0]))
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return nil, nil
		}
		reader := multipart.NewReader(body, boundary)
		var plain []string
		var html []string
		for {
			part, partErr := reader.NextPart()
			if partErr == io.EOF {
				break
			}
			if partErr != nil {
				break
			}
			partPlain, partHTML := parseMIMEBody(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part)
			plain = append(plain, partPlain...)
			html = append(html, partHTML...)
		}
		return plain, html
	}
	payload, err := readMIMEPayload(body, transferEncoding)
	if err != nil || strings.TrimSpace(payload) == "" {
		return nil, nil
	}
	if mediaType == "text/html" {
		return nil, []string{payload}
	}
	if mediaType == "" || strings.HasPrefix(mediaType, "text/") {
		return []string{payload}, nil
	}
	return nil, nil
}

func readMIMEPayload(body io.Reader, transferEncoding string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "base64":
		data, err := io.ReadAll(body)
		if err != nil {
			return "", err
		}
		cleaned := strings.NewReplacer("\r", "", "\n", "", " ", "", "\t", "").Replace(string(data))
		decoded, err := base64.StdEncoding.DecodeString(cleaned)
		if err != nil {
			return "", err
		}
		return string(decoded), nil
	case "quoted-printable":
		data, err := io.ReadAll(quotedprintable.NewReader(body))
		if err != nil {
			return "", err
		}
		return string(data), nil
	default:
		data, err := io.ReadAll(body)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}

func latestMailMessage(items []map[string]any) map[string]any {
	if len(items) == 0 {
		return nil
	}
	candidates := append([]map[string]any(nil), items...)
	sort.SliceStable(candidates, func(i, j int) bool {
		left := messageReceivedAt(candidates[i])
		right := messageReceivedAt(candidates[j])
		if !left.IsZero() || !right.IsZero() {
			if !left.Equal(right) {
				return left.After(right)
			}
			return mailMessageID(candidates[i]) > mailMessageID(candidates[j])
		}
		return false
	})
	return candidates[0]
}

func messageReceivedAt(data map[string]any) time.Time {
	for _, key := range []string{"created_at", "createdAt", "received_at", "receivedAt", "date", "timestamp"} {
		if value, ok := data[key]; ok {
			if parsed := parseMailTime(value); !parsed.IsZero() {
				return parsed
			}
		}
	}
	return time.Time{}
}

func mailMessageID(data map[string]any) string {
	return clean(firstNonNil(data["id"], data["message_id"], data["_id"], data["token"], data["@id"]))
}

func parseMailTime(value any) time.Time {
	switch typed := value.(type) {
	case int:
		return time.Unix(int64(typed), 0).UTC()
	case int64:
		return time.Unix(typed, 0).UTC()
	case float64:
		return time.Unix(int64(typed), 0).UTC()
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return time.Unix(integer, 0).UTC()
		}
		if number, err := typed.Float64(); err == nil {
			return time.Unix(int64(number), 0).UTC()
		}
	}
	text := clean(value)
	if text == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC1123Z, text); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC1123, text); err == nil {
		return parsed
	}
	if parsed, err := mail.ParseDate(text); err == nil {
		return parsed
	}
	return time.Time{}
}

func randomMailboxName() string {
	return randomLower(1) + randomAlphaNum(13+mathrand.Intn(5))
}
