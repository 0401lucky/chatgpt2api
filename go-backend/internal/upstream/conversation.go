package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
	"path/filepath"
	"strings"
	"time"

	"chatgpt2api-go-backend/internal/protocol"
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
		payload, err := c.conversationPayload(ctx, messages, model, timezoneName)
		if err != nil {
			errCh <- err
			return
		}
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
	var lastErr error
	for attempt := 0; attempt <= cloudflareRetryLimit; attempt++ {
		requirements, err := c.getChatRequirementsOnce(ctx)
		if err == nil {
			return requirements, nil
		}
		lastErr = err
		if !isCloudflareError(err) || attempt == cloudflareRetryLimit {
			break
		}
		c.resetBrowserSession()
		if bootstrapErr := c.bootstrap(ctx); bootstrapErr != nil && !isCloudflareError(bootstrapErr) {
			return ChatRequirements{}, bootstrapErr
		}
	}
	return ChatRequirements{}, lastErr
}

func (c *Client) getChatRequirementsOnce(ctx context.Context) (ChatRequirements, error) {
	basePath := "/backend-anon/sentinel/chat-requirements"
	contextName := "noauth_chat_requirements"
	if c.AccessToken != "" {
		basePath = "/backend-api/sentinel/chat-requirements"
		contextName = "auth_chat_requirements"
	}

	sourceP := buildLegacyRequirementsToken(c.userAgent, c.powSources, c.powDataBuild)
	preparePath := basePath + "/prepare"
	preparePayload, err := c.postChatRequirementsJSON(ctx, contextName+"_prepare", preparePath, map[string]any{"p": sourceP})
	if err != nil {
		return ChatRequirements{}, err
	}

	requirements, err := c.buildRequirements(preparePayload, sourceP, "")
	if err != nil {
		return ChatRequirements{}, err
	}
	finalizePath := basePath + "/finalize"
	finalizePayload, err := c.postChatRequirementsJSON(ctx, contextName+"_finalize", finalizePath, map[string]any{
		"prepare_token":   cleanString(preparePayload["prepare_token"]),
		"proof_token":     requirements.ProofToken,
		"turnstile_token": requirements.TurnstileToken,
	})
	if err != nil {
		return ChatRequirements{}, err
	}
	requirements.Token = cleanString(finalizePayload["token"])
	requirements.SOToken = cleanString(finalizePayload["so_token"])
	requirements.Raw = finalizePayload
	if requirements.Token == "" {
		return ChatRequirements{}, fmt.Errorf("missing chat requirements token: %v", finalizePayload)
	}
	return requirements, nil
}

func (c *Client) postChatRequirementsJSON(ctx context.Context, contextName, path string, payload map[string]any) (map[string]any, error) {
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	for key, value := range c.headers(path, map[string]string{"Content-Type": "application/json"}) {
		req.Header.Set(key, value)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w", contextName, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, upstreamHTTPError(contextName, resp.StatusCode, data)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
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

func (c *Client) conversationPayload(ctx context.Context, messages []map[string]any, model, timezoneName string) (map[string]any, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "auto"
	}
	converted, err := c.apiMessagesToConversationMessages(ctx, messages)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"action":                        "next",
		"messages":                      converted,
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
	}, nil
}

func (c *Client) apiMessagesToConversationMessages(ctx context.Context, messages []map[string]any) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(messages))
	for _, item := range messages {
		role := firstNonEmpty(cleanString(item["role"]), "user")
		text := messageContentText(item["content"])
		images, err := c.messageContentImages(ctx, item["content"])
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) == "" && len(images) == 0 {
			continue
		}
		if len(images) == 0 {
			out = append(out, textConversationMessage(role, text))
			continue
		}
		if c.AccessToken == "" {
			return nil, fmt.Errorf("authenticated upstream account required for image input")
		}
		refs := make([]map[string]any, 0, len(images))
		for index, input := range images {
			ref, err := c.uploadImage(ctx, input, index+1)
			if err != nil {
				return nil, err
			}
			refs = append(refs, ref)
		}
		out = append(out, multimodalConversationMessage(role, text, refs))
	}
	return out, nil
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

func (c *Client) messageContentImages(ctx context.Context, content any) ([]protocol.ImageInput, error) {
	inputs := []protocol.ImageInput{}
	for _, raw := range anyList(content) {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		partType := strings.ToLower(cleanString(item["type"]))
		if partType != "image_url" && partType != "input_image" && partType != "image" {
			continue
		}
		input, err := c.imageInputFromContentPart(ctx, item, len(inputs)+1)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

func (c *Client) imageInputFromContentPart(ctx context.Context, item map[string]any, index int) (protocol.ImageInput, error) {
	mimeType := firstNonEmpty(cleanString(item["mime_type"]), cleanString(item["mime"]))
	fileName := firstNonEmpty(cleanString(item["file_name"]), cleanString(item["filename"]))
	imageURL := imageURLFromPart(item)
	if strings.ToLower(cleanString(item["type"])) == "image" {
		if data := cleanString(item["data"]); data != "" {
			imageURL = data
		}
	}
	if imageURL == "" {
		return protocol.ImageInput{}, fmt.Errorf("image input is missing image_url or data")
	}
	if strings.HasPrefix(strings.ToLower(imageURL), "data:") {
		raw, mime, err := decodeDataImageURL(imageURL)
		if err != nil {
			return protocol.ImageInput{}, err
		}
		mimeType = firstNonEmpty(mimeType, mime)
		if fileName == "" {
			fileName = fmt.Sprintf("image_%d%s", index, imageExtensionFromMime(mimeType, raw))
		}
		return protocol.ImageInput{Data: raw, FileName: fileName, MimeType: mimeType}, nil
	}
	if raw, err := base64.StdEncoding.DecodeString(imageURL); err == nil && len(raw) > 0 {
		if fileName == "" {
			fileName = fmt.Sprintf("image_%d%s", index, imageExtensionFromMime(mimeType, raw))
		}
		return protocol.ImageInput{Data: raw, FileName: fileName, MimeType: mimeType}, nil
	}
	raw, err := c.downloadImageBytes(ctx, imageURL)
	if err != nil {
		return protocol.ImageInput{}, err
	}
	if fileName == "" {
		fileName = imageFileNameFromURL(imageURL, index, mimeType, raw)
	}
	return protocol.ImageInput{Data: raw, FileName: fileName, MimeType: mimeType}, nil
}

func imageURLFromPart(item map[string]any) string {
	if nested, ok := item["image_url"].(map[string]any); ok {
		return firstNonEmpty(cleanString(nested["url"]), cleanString(nested["data"]))
	}
	if value := cleanString(item["image_url"]); value != "" {
		return value
	}
	if value := cleanString(item["url"]); value != "" {
		return value
	}
	return ""
}

func decodeDataImageURL(value string) ([]byte, string, error) {
	parts := strings.SplitN(value, ",", 2)
	if len(parts) != 2 {
		return nil, "", fmt.Errorf("invalid data image url")
	}
	meta := strings.TrimPrefix(parts[0], "data:")
	mimeType := strings.Split(meta, ";")[0]
	if strings.Contains(strings.ToLower(meta), ";base64") {
		raw, err := base64.StdEncoding.DecodeString(parts[1])
		return raw, mimeType, err
	}
	text, err := urlpkg.PathUnescape(parts[1])
	if err != nil {
		return nil, "", err
	}
	return []byte(text), mimeType, nil
}

func imageFileNameFromURL(value string, index int, mimeType string, raw []byte) string {
	parsed, err := urlpkg.Parse(value)
	if err == nil {
		base := strings.TrimSpace(filepath.Base(parsed.Path))
		if base != "" && base != "." && base != "/" {
			return base
		}
	}
	return fmt.Sprintf("image_%d%s", index, imageExtensionFromMime(mimeType, raw))
}

func textConversationMessage(role, text string) map[string]any {
	return map[string]any{
		"id":          newUUID(),
		"author":      map[string]any{"role": role},
		"create_time": float64(time.Now().UnixNano()) / 1e9,
		"content":     map[string]any{"content_type": "text", "parts": []any{text}},
		"metadata":    conversationMessageMetadata(nil),
	}
}

func multimodalConversationMessage(role, text string, references []map[string]any) map[string]any {
	parts := make([]any, 0, len(references)+1)
	for _, item := range references {
		parts = append(parts, map[string]any{
			"content_type":  "image_asset_pointer",
			"asset_pointer": "file-service://" + cleanString(item["file_id"]),
			"width":         item["width"],
			"height":        item["height"],
			"size_bytes":    item["file_size"],
		})
	}
	if strings.TrimSpace(text) != "" {
		parts = append(parts, text)
	}
	return map[string]any{
		"id":          newUUID(),
		"author":      map[string]any{"role": role},
		"create_time": float64(time.Now().UnixNano()) / 1e9,
		"content":     map[string]any{"content_type": "multimodal_text", "parts": parts},
		"metadata":    conversationMessageMetadata(references),
	}
}

func conversationMessageMetadata(references []map[string]any) map[string]any {
	metadata := map[string]any{
		"selected_github_repos":     []any{},
		"selected_all_github_repos": false,
		"serialization_metadata":    map[string]any{"custom_symbol_offsets": []any{}},
	}
	if len(references) == 0 {
		return metadata
	}
	attachments := make([]map[string]any, 0, len(references))
	for _, item := range references {
		attachments = append(attachments, map[string]any{
			"id":       item["file_id"],
			"mimeType": item["mime_type"],
			"name":     item["file_name"],
			"size":     item["file_size"],
			"width":    item["width"],
			"height":   item["height"],
		})
	}
	metadata["attachments"] = attachments
	return metadata
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
