package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	searchModel               = "gpt-5-5"
	defaultSearchTimeout      = 300 * time.Second
	defaultSearchPollInterval = 3 * time.Second
)

var (
	searchDoneStatuses     = map[string]struct{}{"finished_successfully": {}, "finished_partial_completion": {}}
	searchConversationIDRe = regexp.MustCompile(`"conversation_id"\s*:\s*"([^"]+)"`)
	searchURLRe            = regexp.MustCompile(`https?://[^\s"'<>）)\]}]+`)
)

func (s *Service) Search(ctx context.Context, accessToken, prompt, model string) (map[string]any, error) {
	return s.NewClient(accessToken).Search(ctx, prompt, model, defaultSearchTimeout, defaultSearchPollInterval)
}

func (c *Client) Search(ctx context.Context, prompt, model string, timeout, pollInterval time.Duration) (map[string]any, error) {
	if c.AccessToken == "" {
		return nil, fmt.Errorf("access_token is required for search")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	model = firstNonEmpty(strings.TrimSpace(model), searchModel)
	if timeout <= 0 {
		timeout = defaultSearchTimeout
	}
	if pollInterval <= 0 {
		pollInterval = defaultSearchPollInterval
	}
	conduitToken, err := c.prepareSearchConversation(ctx, prompt, model)
	if err != nil {
		return nil, err
	}
	if err := c.bootstrap(ctx); err != nil {
		return nil, err
	}
	conversationID, err := c.runSearchConversation(ctx, prompt, conduitToken, model)
	if err != nil {
		return nil, err
	}
	return c.waitSearchResult(ctx, conversationID, timeout, pollInterval)
}

func (c *Client) prepareSearchConversation(ctx context.Context, prompt, model string) (string, error) {
	path := "/backend-api/f/conversation/prepare"
	payload := map[string]any{
		"action":                "next",
		"fork_from_shared_post": false,
		"parent_message_id":     "client-created-root",
		"model":                 model,
		"client_prepare_state":  "success",
		"timezone_offset_min":   -480,
		"timezone":              "Asia/Shanghai",
		"conversation_mode":     map[string]any{"kind": "primary_assistant"},
		"system_hints":          []string{"search"},
		"partial_query": map[string]any{
			"id":      newUUID(),
			"author":  map[string]any{"role": "user"},
			"content": map[string]any{"content_type": "text", "parts": []string{prompt}},
		},
		"supports_buffering":     true,
		"supported_encodings":    []string{"v1"},
		"client_contextual_info": map[string]any{"app_name": "chatgpt.com"},
	}
	result, err := c.postImageJSONPayload(ctx, path, payload, c.headers(path, map[string]string{
		"Accept":          "*/*",
		"Content-Type":    "application/json",
		"X-Conduit-Token": "no-token",
	}))
	if err != nil {
		return "", err
	}
	token := cleanString(result["conduit_token"])
	if token == "" {
		return "", fmt.Errorf("missing conduit_token")
	}
	return token, nil
}

func (c *Client) runSearchConversation(ctx context.Context, prompt, conduitToken, model string) (string, error) {
	requirements, err := c.getChatRequirements(ctx)
	if err != nil {
		return "", err
	}
	path := "/backend-api/f/conversation"
	payload := map[string]any{
		"action": "next",
		"messages": []map[string]any{{
			"id":          newUUID(),
			"author":      map[string]any{"role": "user"},
			"create_time": float64(time.Now().UnixNano()) / 1e9,
			"content":     map[string]any{"content_type": "text", "parts": []string{prompt}},
			"metadata": map[string]any{
				"developer_mode_connector_ids": []any{},
				"selected_github_repos":        []any{},
				"selected_all_github_repos":    false,
				"system_hints":                 []string{"search"},
				"serialization_metadata":       map[string]any{"custom_symbol_offsets": []any{}},
			},
		}},
		"parent_message_id":                    "client-created-root",
		"model":                                model,
		"client_prepare_state":                 "success",
		"timezone_offset_min":                  -480,
		"timezone":                             "Asia/Shanghai",
		"conversation_mode":                    map[string]any{"kind": "primary_assistant"},
		"enable_message_followups":             true,
		"system_hints":                         []string{},
		"supports_buffering":                   true,
		"supported_encodings":                  []string{"v1"},
		"force_use_search":                     true,
		"client_reported_search_source":        "conversation_composer_web_icon",
		"client_contextual_info":               searchClientContextualInfo(),
		"paragen_cot_summary_display_override": "allow",
		"force_parallel_switch":                "auto",
	}
	resp, err := c.postJSONStream(ctx, path, payload, c.imageHeaders(path, requirements, conduitToken, "text/event-stream"))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", upstreamHTTPError(path, resp.StatusCode, body)
	}
	payloads := make(chan string)
	streamErr := make(chan error, 1)
	go func() {
		defer close(payloads)
		streamErr <- iterSSEPayloads(ctx, resp.Body, payloads)
	}()
	conversationID := ""
	for payload := range payloads {
		if payload == "[DONE]" {
			break
		}
		conversationID = firstNonEmpty(conversationID, findSearchValue(payload, "conversation_id"))
	}
	if err := <-streamErr; err != nil {
		return "", err
	}
	if conversationID == "" {
		return "", fmt.Errorf("conversation_id not found in stream")
	}
	return conversationID, nil
}

func searchClientContextualInfo() map[string]any {
	return map[string]any{
		"is_dark_mode":      false,
		"time_since_loaded": 36,
		"page_height":       925,
		"page_width":        886,
		"pixel_ratio":       2,
		"screen_height":     1440,
		"screen_width":      2560,
		"app_name":          "chatgpt.com",
	}
}

func (c *Client) waitSearchResult(ctx context.Context, conversationID string, timeout, pollInterval time.Duration) (map[string]any, error) {
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastResult map[string]any
	lastAnswer := ""
	stableHits := 0
	for pollCtx.Err() == nil {
		conversation, fetchErr := c.getSearchConversation(pollCtx, conversationID)
		result, err := c.extractSearchResult(conversationID, conversation, fetchErr)
		if err != nil {
			if pollCtx.Err() != nil {
				break
			}
			if !isRetryablePollError(err) {
				return nil, err
			}
		} else {
			lastResult = result
			answer := cleanString(result["answer"])
			if answer != "" {
				if _, done := searchDoneStatuses[cleanString(result["status"])]; done {
					return result, nil
				}
				if answer == lastAnswer {
					stableHits++
				} else {
					stableHits = 0
					lastAnswer = answer
				}
				if stableHits >= 2 {
					return result, nil
				}
			}
		}
		if err := sleepContext(pollCtx, pollInterval); err != nil {
			break
		}
	}
	if lastResult != nil {
		return lastResult, nil
	}
	return nil, fmt.Errorf("timed out waiting for search result: %s", conversationID)
}

func (c *Client) getSearchConversation(ctx context.Context, conversationID string) (map[string]any, error) {
	path := "/backend-api/conversation/" + strings.TrimSpace(conversationID)
	headers := c.headers(path, map[string]string{"Accept": "*/*"})
	headers["Referer"] = c.BaseURL + "/c/" + strings.TrimSpace(conversationID)
	headers["X-OpenAI-Target-Route"] = "/backend-api/conversation/{conversation_id}"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return c.doJSON(req, path)
}

func (c *Client) extractSearchResult(conversationID string, conversation map[string]any, err error) (map[string]any, error) {
	if err != nil {
		return nil, err
	}
	message := latestAssistantSearchMessage(conversation)
	metadata := mapValue(message["metadata"])
	finishDetails := mapValue(metadata["finish_details"])
	answer := searchMessageText(message)
	sources := extractSearchSources(message)
	for _, hit := range searchURLRe.FindAllString(answer, -1) {
		url := cleanSearchURL(hit)
		if url != "" && !searchSourceHasURL(sources, url) {
			sources = append(sources, map[string]string{"title": "", "url": url, "snippet": "", "source_type": ""})
		}
	}
	return map[string]any{
		"conversation_id":      conversationID,
		"status":               firstNonEmpty(cleanString(finishDetails["type"]), cleanString(metadata["status"]), findSearchValue(message, "status")),
		"answer":               answer,
		"sources":              sources,
		"assistant_message_id": cleanString(message["id"]),
		"create_time":          floatValue(message["create_time"]),
	}, nil
}

func latestAssistantSearchMessage(conversation map[string]any) map[string]any {
	var latest map[string]any
	latestTime := -1.0
	for _, rawNode := range mapValue(conversation["mapping"]) {
		message := mapValue(mapValue(rawNode)["message"])
		author := mapValue(message["author"])
		if cleanString(author["role"]) != "assistant" {
			continue
		}
		createTime := floatValue(message["create_time"])
		if latest == nil || createTime >= latestTime {
			latest = message
			latestTime = createTime
		}
	}
	if latest == nil {
		return map[string]any{}
	}
	return latest
}

func extractSearchSources(payload any) []map[string]string {
	sources := []map[string]string{}
	for _, obj := range walkSearchDicts(payload) {
		metadata := mapValue(obj["metadata"])
		url := cleanSearchURL(firstNonEmpty(cleanString(obj["url"]), cleanString(obj["link"]), cleanString(obj["source_url"]), cleanString(metadata["url"])))
		if url == "" || searchSourceHasURL(sources, url) {
			continue
		}
		sources = append(sources, map[string]string{
			"title":       firstNonEmpty(cleanString(obj["title"]), cleanString(obj["name"]), cleanString(obj["source"])),
			"url":         url,
			"snippet":     firstNonEmpty(cleanString(obj["snippet"]), cleanString(obj["text"]), cleanString(obj["description"])),
			"source_type": firstNonEmpty(cleanString(obj["type"]), cleanString(obj["source_type"])),
		})
	}
	return sources
}

func searchSourceHasURL(sources []map[string]string, url string) bool {
	for _, item := range sources {
		if item["url"] == url {
			return true
		}
	}
	return false
}

func searchMessageText(message any) string {
	messageMap := mapValue(message)
	if text := cleanString(messageMap["content"]); text != "" {
		if _, ok := messageMap["content"].(map[string]any); !ok {
			return text
		}
	}
	content := mapValue(messageMap["content"])
	parts := []string{}
	if text := cleanString(content["text"]); text != "" {
		parts = append(parts, text)
	}
	for _, raw := range anyList(content["parts"]) {
		if text := cleanString(raw); text != "" {
			if _, ok := raw.(map[string]any); !ok {
				parts = append(parts, text)
				continue
			}
		}
		item := mapValue(raw)
		for _, key := range []string{"text", "summary", "content"} {
			if text := cleanString(item[key]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) == 0 {
		if text := cleanString(messageMap["content"]); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func findSearchValue(payload any, key string) string {
	switch value := payload.(type) {
	case string:
		if key == "conversation_id" {
			if match := searchConversationIDRe.FindStringSubmatch(value); len(match) > 1 {
				return match[1]
			}
		}
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return ""
		}
		return findSearchValue(decoded, key)
	case map[string]any:
		if text := cleanString(value[key]); text != "" {
			return text
		}
		for _, item := range value {
			if found := findSearchValue(item, key); found != "" {
				return found
			}
		}
	case []any:
		for _, item := range value {
			if found := findSearchValue(item, key); found != "" {
				return found
			}
		}
	}
	return ""
}

func walkSearchDicts(payload any) []map[string]any {
	out := []map[string]any{}
	switch value := payload.(type) {
	case map[string]any:
		out = append(out, value)
		for _, item := range value {
			out = append(out, walkSearchDicts(item)...)
		}
	case []any:
		for _, item := range value {
			out = append(out, walkSearchDicts(item)...)
		}
	}
	return out
}

func cleanSearchURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), ".,;，。；")
}
