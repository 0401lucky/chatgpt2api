package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ChatRequirements struct {
	Token          string
	ProofToken     string
	TurnstileToken string
	SOToken        string
	Raw            map[string]any
}

func (s *Service) StreamConversation(ctx context.Context, accessToken string, messages []map[string]any, model, prompt string) (<-chan string, <-chan error) {
	return s.NewClient(accessToken).StreamConversation(ctx, messages, model, prompt)
}

func (c *Client) StreamConversation(ctx context.Context, messages []map[string]any, model, prompt string) (<-chan string, <-chan error) {
	out := make(chan string)
	errCh := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errCh)
		if len(messages) == 0 && strings.TrimSpace(prompt) != "" {
			messages = []map[string]any{{"role": "user", "content": prompt}}
		}
		if len(messages) == 0 {
			errCh <- fmt.Errorf("messages or prompt is required")
			return
		}
		if err := c.bootstrap(ctx); err != nil {
			errCh <- err
			return
		}
		requirements, err := c.getChatRequirements(ctx)
		if err != nil {
			errCh <- err
			return
		}
		path, timezoneName := c.chatTarget()
		payload := c.conversationPayload(messages, model, timezoneName)
		resp, err := c.postJSONStream(ctx, path, payload, c.conversationHeaders(path, requirements))
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			errCh <- upstreamHTTPError(path, resp.StatusCode, body)
			return
		}
		errCh <- iterSSEPayloads(ctx, resp.Body, out)
	}()
	return out, errCh
}

func (c *Client) getChatRequirements(ctx context.Context) (ChatRequirements, error) {
	path := "/backend-anon/sentinel/chat-requirements"
	contextName := "noauth_chat_requirements"
	sourceP := ""
	if c.AccessToken != "" {
		path = "/backend-api/sentinel/chat-requirements"
		contextName = "auth_chat_requirements"
	}
	sourceP = buildLegacyRequirementsToken(c.userAgent, c.powSources, c.powDataBuild)
	body, _ := json.Marshal(map[string]any{"p": sourceP})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	for key, value := range c.headers(path, map[string]string{"Content-Type": "application/json"}) {
		req.Header.Set(key, value)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return ChatRequirements{}, fmt.Errorf("%s failed: %w", contextName, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatRequirements{}, upstreamHTTPError(contextName, resp.StatusCode, data)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return ChatRequirements{}, err
	}
	turnstileSourceP := sourceP
	turnstileFallbackP := ""
	if c.AccessToken != "" {
		turnstileSourceP = ""
		turnstileFallbackP = sourceP
	}
	requirements, err := c.buildRequirements(payload, turnstileSourceP, turnstileFallbackP)
	if err != nil {
		return ChatRequirements{}, err
	}
	if requirements.Token == "" {
		return ChatRequirements{}, fmt.Errorf("missing chat requirements token: %v", payload)
	}
	return requirements, nil
}

func (c *Client) buildRequirements(data map[string]any, sourceP string, fallbackP string) (ChatRequirements, error) {
	if boolField(mapField(data["arkose"]), "required") {
		return ChatRequirements{}, fmt.Errorf("chat requirements requires arkose token, which is not implemented in Go backend")
	}
	proofToken := ""
	proof := mapField(data["proofofwork"])
	if boolField(proof, "required") {
		token, err := buildProofToken(cleanString(proof["seed"]), cleanString(proof["difficulty"]), c.userAgent, c.powSources, c.powDataBuild)
		if err != nil {
			return ChatRequirements{}, err
		}
		proofToken = token
	}
	turnstileToken := ""
	turnstile := mapField(data["turnstile"])
	if boolField(turnstile, "required") {
		dx := cleanString(turnstile["dx"])
		token, status := solveTurnstileTokenWithStatus(dx, sourceP)
		if token == "" && fallbackP != sourceP {
			var fallbackStatus string
			token, fallbackStatus = solveTurnstileTokenWithStatus(dx, fallbackP)
			status += ";fallback=" + fallbackStatus
		}
		if token == "" {
			return ChatRequirements{}, fmt.Errorf("chat requirements requires turnstile token, but Go backend could not solve it: %s", status)
		}
		turnstileToken = token
	}
	return ChatRequirements{
		Token:          cleanString(data["token"]),
		ProofToken:     proofToken,
		TurnstileToken: turnstileToken,
		SOToken:        cleanString(data["so_token"]),
		Raw:            data,
	}, nil
}

func (c *Client) chatTarget() (string, string) {
	if c.AccessToken != "" {
		return "/backend-api/conversation", "Asia/Shanghai"
	}
	return "/backend-anon/conversation", "America/Los_Angeles"
}

func (c *Client) conversationPayload(messages []map[string]any, model, timezoneName string) map[string]any {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "auto"
	}
	return map[string]any{
		"action":                        "next",
		"messages":                      c.apiMessagesToConversationMessages(messages),
		"model":                         model,
		"parent_message_id":             newUUID(),
		"conversation_mode":             map[string]any{"kind": "primary_assistant"},
		"conversation_origin":           nil,
		"force_paragen":                 false,
		"force_paragen_model_slug":      "",
		"force_rate_limit":              false,
		"force_use_sse":                 true,
		"history_and_training_disabled": true,
		"reset_rate_limits":             false,
		"suggestions":                   []any{},
		"supported_encodings":           []any{},
		"system_hints":                  []any{},
		"timezone":                      timezoneName,
		"timezone_offset_min":           -480,
		"variant_purpose":               "comparison_implicit",
		"websocket_request_id":          newUUID(),
		"client_contextual_info": map[string]any{
			"is_dark_mode":        false,
			"time_since_loaded":   120,
			"page_height":         900,
			"page_width":          1400,
			"pixel_ratio":         2,
			"screen_height":       1440,
			"screen_width":        2560,
			"app_name":            "chatgpt.com",
			"current_time":        time.Now().Format(time.RFC3339),
			"timezone_offset_min": -480,
		},
	}
}

func (c *Client) apiMessagesToConversationMessages(messages []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, item := range messages {
		role := firstNonEmpty(cleanString(item["role"]), "user")
		text := messageContentText(item["content"])
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, map[string]any{
			"id":          newUUID(),
			"author":      map[string]any{"role": role},
			"create_time": float64(time.Now().UnixNano()) / 1e9,
			"content":     map[string]any{"content_type": "text", "parts": []any{text}},
			"metadata": map[string]any{
				"selected_github_repos":     []any{},
				"selected_all_github_repos": false,
				"serialization_metadata":    map[string]any{"custom_symbol_offsets": []any{}},
			},
		})
	}
	return out
}

func messageContentText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, raw := range value {
			switch item := raw.(type) {
			case string:
				parts = append(parts, item)
			case map[string]any:
				switch strings.ToLower(cleanString(item["type"])) {
				case "text", "input_text", "output_text":
					parts = append(parts, cleanString(item["text"]))
				}
			}
		}
		return strings.Join(parts, "")
	default:
		return cleanString(content)
	}
}

func (c *Client) conversationHeaders(path string, requirements ChatRequirements) map[string]string {
	extra := map[string]string{
		"Accept":       "text/event-stream",
		"Content-Type": "application/json",
		"OpenAI-Sentinel-Chat-Requirements-Token": requirements.Token,
	}
	if requirements.ProofToken != "" {
		extra["OpenAI-Sentinel-Proof-Token"] = requirements.ProofToken
	}
	if requirements.TurnstileToken != "" {
		extra["OpenAI-Sentinel-Turnstile-Token"] = requirements.TurnstileToken
	}
	if requirements.SOToken != "" {
		extra["OpenAI-Sentinel-SO-Token"] = requirements.SOToken
	}
	return c.headers(path, extra)
}

func (c *Client) postJSONStream(ctx context.Context, path string, payload any, headers map[string]string) (*http.Response, error) {
	data, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(data))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w", path, err)
	}
	return resp, nil
}

func iterSSEPayloads(ctx context.Context, reader io.Reader, out chan<- string) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "" {
			continue
		}
		select {
		case out <- payload:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func mapField(value any) map[string]any {
	if item, ok := value.(map[string]any); ok {
		return item
	}
	return map[string]any{}
}

func boolField(item map[string]any, key string) bool {
	switch value := item[key].(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}
