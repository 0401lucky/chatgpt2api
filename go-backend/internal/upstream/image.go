package upstream

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	urlpkg "net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"chatgpt2api-go-backend/internal/protocol"
)

func (s *Service) GenerateImage(ctx context.Context, accessToken, prompt, model, size, responseFormat string) ([]map[string]any, error) {
	return s.NewClient(accessToken).GenerateImage(
		ctx,
		prompt,
		model,
		size,
		responseFormat,
		firstDuration(s.ImagePollTimeout, 120*time.Second),
		firstDuration(s.ImagePollInitialWait, 10*time.Second),
		firstDuration(s.ImagePollInterval, 10*time.Second),
	)
}

func (s *Service) EditImage(ctx context.Context, accessToken, prompt, model, size, responseFormat string, images []protocol.ImageInput) ([]map[string]any, error) {
	return s.NewClient(accessToken).EditImage(
		ctx,
		prompt,
		model,
		size,
		responseFormat,
		images,
		firstDuration(s.ImagePollTimeout, 120*time.Second),
		firstDuration(s.ImagePollInitialWait, 10*time.Second),
		firstDuration(s.ImagePollInterval, 10*time.Second),
	)
}

func (c *Client) GenerateImage(ctx context.Context, prompt, model, size, responseFormat string, pollTimeout, initialWait, pollInterval time.Duration) ([]map[string]any, error) {
	if c.AccessToken == "" {
		return nil, fmt.Errorf("authenticated upstream account required for image generation")
	}
	model = normalizeImageModel(model)
	if !isSupportedImageModel(model) {
		return nil, fmt.Errorf("unsupported image model, supported models: auto, gpt-image-1, gpt-image-2, codex-gpt-image-2")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	finalPrompt := buildImagePrompt(prompt, size)
	if err := c.bootstrap(ctx); err != nil {
		return nil, err
	}
	requirements, err := c.getChatRequirements(ctx)
	if err != nil {
		return nil, err
	}
	conduitToken, err := c.prepareImageConversation(ctx, finalPrompt, model, requirements)
	if err != nil {
		return nil, err
	}
	last, err := c.streamImageConversation(ctx, finalPrompt, model, requirements, conduitToken, nil)
	if err != nil {
		return nil, err
	}
	return c.imageDataFromLastEvent(ctx, last, prompt, responseFormat, true, pollTimeout, initialWait, pollInterval)
}

func (c *Client) EditImage(ctx context.Context, prompt, model, size, responseFormat string, images []protocol.ImageInput, pollTimeout, initialWait, pollInterval time.Duration) ([]map[string]any, error) {
	if c.AccessToken == "" {
		return nil, fmt.Errorf("authenticated upstream account required for image editing")
	}
	model = normalizeImageModel(model)
	if !isSupportedImageModel(model) {
		return nil, fmt.Errorf("unsupported image model, supported models: auto, gpt-image-1, gpt-image-2, codex-gpt-image-2")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("image is required")
	}
	references := make([]map[string]any, 0, len(images))
	for index, input := range images {
		ref, err := c.uploadImage(ctx, input, index+1)
		if err != nil {
			return nil, err
		}
		references = append(references, ref)
	}
	finalPrompt := buildImagePrompt(prompt, size)
	if err := c.bootstrap(ctx); err != nil {
		return nil, err
	}
	requirements, err := c.getChatRequirements(ctx)
	if err != nil {
		return nil, err
	}
	conduitToken, err := c.prepareImageConversation(ctx, finalPrompt, model, requirements)
	if err != nil {
		return nil, err
	}
	last, err := c.streamImageConversation(ctx, finalPrompt, model, requirements, conduitToken, references)
	if err != nil {
		return nil, err
	}
	return c.imageDataFromLastEvent(ctx, last, prompt, responseFormat, true, pollTimeout, initialWait, pollInterval)
}

func (c *Client) imageDataFromLastEvent(ctx context.Context, last map[string]any, prompt, responseFormat string, shouldPoll bool, pollTimeout, initialWait, pollInterval time.Duration) ([]map[string]any, error) {
	conversationID := cleanString(last["conversation_id"])
	fileIDs := stringList(last["file_ids"])
	sedimentIDs := stringList(last["sediment_ids"])
	pollTarget := imagePollTargetFromEvent(last)
	message := strings.TrimSpace(cleanString(last["text"]))
	blocked, _ := last["blocked"].(bool)
	turnUseCase := cleanString(last["turn_use_case"])
	if message != "" && len(fileIDs) == 0 && len(sedimentIDs) == 0 && blocked {
		return nil, fmt.Errorf("%s", message)
	}
	if message != "" && len(fileIDs) == 0 && len(sedimentIDs) == 0 && !shouldPoll && turnUseCase != "image gen" {
		return nil, fmt.Errorf("%s", message)
	}
	urls, err := c.resolveConversationImageURLs(ctx, conversationID, fileIDs, sedimentIDs, pollTarget, shouldPoll || turnUseCase == "image gen", pollTimeout, initialWait, pollInterval)
	if err != nil {
		return nil, err
	}
	if len(urls) == 0 {
		if message != "" {
			return nil, fmt.Errorf("%s", message)
		}
		return nil, fmt.Errorf("upstream did not return image download url")
	}
	data := make([]map[string]any, 0, len(urls))
	wantBase64 := strings.EqualFold(strings.TrimSpace(responseFormat), "b64_json")
	for _, url := range urls {
		item := map[string]any{"url": url, "revised_prompt": prompt}
		if wantBase64 {
			raw, err := c.downloadImageBytes(ctx, url)
			if err != nil {
				return nil, err
			}
			item["b64_json"] = base64.StdEncoding.EncodeToString(raw)
		}
		data = append(data, item)
	}
	return data, nil
}

func (c *Client) prepareImageConversation(ctx context.Context, prompt, model string, requirements ChatRequirements) (string, error) {
	path := "/backend-api/f/conversation/prepare"
	payload := map[string]any{
		"action":                 "next",
		"fork_from_shared_post":  false,
		"parent_message_id":      newUUID(),
		"model":                  imageModelSlug(model),
		"client_prepare_state":   "success",
		"timezone_offset_min":    -480,
		"timezone":               "Asia/Shanghai",
		"conversation_mode":      map[string]any{"kind": "primary_assistant"},
		"system_hints":           []string{"picture_v2"},
		"supports_buffering":     true,
		"supported_encodings":    []string{"v1"},
		"client_contextual_info": map[string]any{"app_name": "chatgpt.com"},
		"partial_query":          imageUserMessage(prompt, nil),
	}
	result, err := c.postImageJSONPayload(ctx, path, payload, c.imageHeaders(path, requirements, "", "*/*"))
	if err != nil {
		return "", err
	}
	return cleanString(result["conduit_token"]), nil
}

func (c *Client) streamImageConversation(ctx context.Context, prompt, model string, requirements ChatRequirements, conduitToken string, references []map[string]any) (map[string]any, error) {
	path := "/backend-api/f/conversation"
	payload := map[string]any{
		"action":                               "next",
		"messages":                             []map[string]any{imageUserMessage(prompt, references)},
		"parent_message_id":                    newUUID(),
		"model":                                imageModelSlug(model),
		"client_prepare_state":                 "sent",
		"timezone_offset_min":                  -480,
		"timezone":                             "Asia/Shanghai",
		"conversation_mode":                    map[string]any{"kind": "primary_assistant"},
		"enable_message_followups":             true,
		"system_hints":                         []string{"picture_v2"},
		"supports_buffering":                   true,
		"supported_encodings":                  []string{"v1"},
		"paragen_cot_summary_display_override": "allow",
		"force_parallel_switch":                "auto",
		"client_contextual_info": map[string]any{
			"is_dark_mode":      false,
			"time_since_loaded": 1200,
			"page_height":       1072,
			"page_width":        1724,
			"pixel_ratio":       1.2,
			"screen_height":     1440,
			"screen_width":      2560,
			"app_name":          "chatgpt.com",
		},
	}
	resp, err := c.postJSONStream(ctx, path, payload, c.imageHeaders(path, requirements, conduitToken, "text/event-stream"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, upstreamHTTPError(path, resp.StatusCode, body)
	}
	payloads := make(chan string)
	streamErr := make(chan error, 1)
	go func() {
		defer close(payloads)
		streamErr <- iterSSEPayloads(ctx, resp.Body, payloads)
	}()
	last := map[string]any{}
	iterErr := protocol.IterConversationPayloads(ctx, payloads, "", nil, func(event map[string]any) error {
		last = event
		return nil
	})
	upErr := <-streamErr
	if iterErr != nil {
		return nil, iterErr
	}
	if upErr != nil {
		return nil, upErr
	}
	return last, nil
}

func imageUserMessage(prompt string, references []map[string]any) map[string]any {
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
	parts = append(parts, prompt)
	content := map[string]any{"content_type": "text", "parts": []string{prompt}}
	if len(references) > 0 {
		content = map[string]any{"content_type": "multimodal_text", "parts": parts}
	}
	metadata := map[string]any{
		"developer_mode_connector_ids": []any{},
		"selected_github_repos":        []any{},
		"selected_all_github_repos":    false,
		"system_hints":                 []string{"picture_v2"},
		"serialization_metadata":       map[string]any{"custom_symbol_offsets": []any{}},
	}
	if len(references) > 0 {
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
	}
	return map[string]any{
		"id":          newUUID(),
		"author":      map[string]any{"role": "user"},
		"create_time": float64(time.Now().UnixNano()) / 1e9,
		"content":     content,
		"metadata":    metadata,
	}
}

func (c *Client) postImageJSONPayload(ctx context.Context, path string, payload any, headers map[string]string) (map[string]any, error) {
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return c.doJSON(req, path)
}

func (c *Client) uploadImage(ctx context.Context, input protocol.ImageInput, index int) (map[string]any, error) {
	raw := input.Data
	if len(raw) == 0 {
		return nil, fmt.Errorf("image file is empty")
	}
	fileName := strings.TrimSpace(input.FileName)
	if fileName == "" {
		fileName = fmt.Sprintf("image_%d%s", index, imageExtensionFromMime(input.MimeType, raw))
	}
	mimeType := normalizeInputImageMime(input.MimeType, raw, fileName)
	width, height := imageDimensions(raw)
	path := "/backend-api/files"
	uploadMeta, err := c.postImageJSONPayload(ctx, path, map[string]any{
		"file_name": fileName,
		"file_size": len(raw),
		"use_case":  "multimodal",
		"width":     width,
		"height":    height,
	}, c.headers(path, map[string]string{"Content-Type": "application/json", "Accept": "application/json"}))
	if err != nil {
		return nil, err
	}
	uploadURL := cleanString(uploadMeta["upload_url"])
	fileID := cleanString(uploadMeta["file_id"])
	if uploadURL == "" || fileID == "" {
		return nil, fmt.Errorf("image upload metadata missing upload_url or file_id")
	}
	if err := c.putUploadBytes(ctx, uploadURL, raw, mimeType); err != nil {
		return nil, err
	}
	confirmPath := "/backend-api/files/" + fileID + "/uploaded"
	if _, err := c.postImageJSONPayload(ctx, confirmPath, map[string]any{}, c.headers(confirmPath, map[string]string{"Content-Type": "application/json", "Accept": "application/json"})); err != nil {
		return nil, err
	}
	return map[string]any{
		"file_id":   fileID,
		"file_name": fileName,
		"file_size": len(raw),
		"mime_type": mimeType,
		"width":     width,
		"height":    height,
	}, nil
}

func (c *Client) putUploadBytes(ctx context.Context, uploadURL string, raw []byte, mimeType string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	req.Header.Set("x-ms-version", "2020-04-08")
	req.Header.Set("Origin", c.BaseURL)
	req.Header.Set("Referer", c.BaseURL+"/")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("image_upload failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return upstreamHTTPError("image_upload", resp.StatusCode, body)
	}
	return nil
}

func (c *Client) imageHeaders(path string, requirements ChatRequirements, conduitToken, accept string) map[string]string {
	if accept == "" {
		accept = "*/*"
	}
	extra := map[string]string{
		"Accept":       accept,
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
	if conduitToken != "" {
		extra["X-Conduit-Token"] = conduitToken
	}
	if accept == "text/event-stream" {
		extra["X-Oai-Turn-Trace-Id"] = newUUID()
	}
	return c.headers(path, extra)
}

func (c *Client) resolveConversationImageURLs(ctx context.Context, conversationID string, fileIDs, sedimentIDs []string, target imagePollTarget, poll bool, timeout, initialWait, interval time.Duration) ([]string, error) {
	fileIDs = filterFileIDs(fileIDs)
	sedimentIDs = uniqueStrings(sedimentIDs)
	if poll && conversationID != "" && len(fileIDs) == 0 && len(sedimentIDs) == 0 {
		nextFiles, nextSediments, err := c.pollImageResults(ctx, conversationID, target, timeout, initialWait, interval)
		if err != nil {
			return nil, err
		}
		fileIDs = filterFileIDs(nextFiles)
		sedimentIDs = uniqueStrings(nextSediments)
	}
	urls := make([]string, 0)
	for _, fileID := range fileIDs {
		url, err := c.getFileDownloadURL(ctx, conversationID, fileID)
		if err == nil && strings.TrimSpace(url) != "" {
			urls = append(urls, url)
		}
	}
	if len(urls) > 0 || conversationID == "" {
		return uniqueStrings(urls), nil
	}
	for _, sedimentID := range sedimentIDs {
		url, err := c.getAttachmentDownloadURL(ctx, conversationID, sedimentID)
		if err == nil && strings.TrimSpace(url) != "" {
			urls = append(urls, url)
		}
	}
	return uniqueStrings(urls), nil
}

func (c *Client) pollImageResults(ctx context.Context, conversationID string, target imagePollTarget, timeout, initialWait, interval time.Duration) ([]string, []string, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if initialWait > 0 {
		if err := sleepContext(pollCtx, initialWait); err != nil {
			return nil, nil, err
		}
	}
	attempt := 0
	for {
		attempt++
		conversation, err := c.getJSON(pollCtx, "/backend-api/conversation/"+conversationID, "/backend-api/conversation/"+conversationID)
		if err != nil {
			if pollCtx.Err() != nil {
				break
			}
			if !isRetryablePollError(err) {
				return nil, nil, err
			}
			if sleepContext(pollCtx, backoffDuration(attempt)) != nil {
				break
			}
			continue
		}
		fileIDs, sedimentIDs := extractImageToolRecordsForTarget(conversation, target)
		if len(fileIDs) > 0 || len(sedimentIDs) > 0 {
			return fileIDs, sedimentIDs, nil
		}
		if err := sleepContext(pollCtx, interval); err != nil {
			break
		}
	}
	return nil, nil, fmt.Errorf("ChatGPT 生图超时（已等待 %d 秒），可能是账号被限流或生图队列拥堵", int(timeout.Seconds()))
}

func (c *Client) getFileDownloadURL(ctx context.Context, conversationID, fileID string) (string, error) {
	paths := []string{"/backend-api/files/" + fileID + "/download", "/backend-api/files/download/" + fileID}
	var lastErr error
	for _, basePath := range paths {
		path := basePath
		if strings.TrimSpace(conversationID) != "" {
			path += "?conversation_id=" + urlpkg.QueryEscape(strings.TrimSpace(conversationID)) + "&inline=false"
		}
		payload, err := c.getJSON(ctx, path, path)
		if err != nil {
			lastErr = err
			continue
		}
		return firstNonEmpty(cleanString(payload["download_url"]), cleanString(payload["url"])), nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("download url not found")
}

func (c *Client) getAttachmentDownloadURL(ctx context.Context, conversationID, attachmentID string) (string, error) {
	path := "/backend-api/conversation/" + conversationID + "/attachment/" + attachmentID + "/download"
	payload, err := c.getJSON(ctx, path, path)
	if err != nil {
		return "", err
	}
	return firstNonEmpty(cleanString(payload["download_url"]), cleanString(payload["url"])), nil
}

func (c *Client) downloadImageBytes(ctx context.Context, imageURL string) ([]byte, error) {
	target := strings.TrimSpace(imageURL)
	parsed, err := urlpkg.Parse(target)
	if err != nil {
		return nil, err
	}
	if !parsed.IsAbs() {
		base, baseErr := urlpkg.Parse(c.BaseURL)
		if baseErr != nil {
			return nil, baseErr
		}
		parsed = base.ResolveReference(parsed)
		target = parsed.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if c.isChatGPTBackendURL(parsed) {
		path := parsed.EscapedPath()
		for key, value := range c.headers(path, map[string]string{"Accept": "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8"}) {
			req.Header.Set(key, value)
		}
	} else if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "image/avif,image/webp,image/png,image/*,*/*;q=0.8")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, upstreamHTTPError(imageURL, resp.StatusCode, body)
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) isChatGPTBackendURL(parsed *urlpkg.URL) bool {
	if parsed == nil {
		return false
	}
	base, err := urlpkg.Parse(c.BaseURL)
	if err != nil || base.Host == "" {
		return false
	}
	if !strings.EqualFold(parsed.Host, base.Host) {
		return false
	}
	path := parsed.EscapedPath()
	return strings.HasPrefix(path, "/backend-api/") || strings.HasPrefix(path, "/backend-anon/")
}

func extractImageToolRecords(conversation map[string]any) ([]string, []string) {
	return extractImageToolRecordsForTarget(conversation, imagePollTarget{})
}

func extractImageToolRecordsForTarget(conversation map[string]any, target imagePollTarget) ([]string, []string) {
	mapping := mapValue(conversation["mapping"])
	records := make([]imageToolRecord, 0)
	for messageID, rawNode := range mapping {
		node := mapValue(rawNode)
		message := mapValue(node["message"])
		author := mapValue(message["author"])
		if cleanString(author["role"]) != "tool" {
			continue
		}
		if target.hasTarget() && !imageNodeMatchesPollTarget(messageID, node, message, mapping, target, map[string]bool{}) {
			continue
		}
		content := mapValue(message["content"])
		if cleanString(content["content_type"]) != "multimodal_text" {
			continue
		}
		metadata := mapValue(message["metadata"])
		fileIDs := []string{}
		sedimentIDs := []string{}
		for _, rawPart := range anyList(content["parts"]) {
			text := ""
			if part, ok := rawPart.(map[string]any); ok {
				text = cleanString(part["asset_pointer"])
			} else {
				text = cleanString(rawPart)
			}
			fileIDs = append(fileIDs, extractFileIDs(text)...)
			sedimentIDs = append(sedimentIDs, extractSedimentIDs(text)...)
		}
		if cleanString(metadata["async_task_type"]) != "image_gen" && len(fileIDs) == 0 && len(sedimentIDs) == 0 {
			continue
		}
		records = append(records, imageToolRecord{
			MessageID:   messageID,
			CreateTime:  floatValue(message["create_time"]),
			FileIDs:     uniqueStrings(fileIDs),
			SedimentIDs: uniqueStrings(sedimentIDs),
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreateTime > records[j].CreateTime
	})
	if len(records) > 1 {
		bestTime := records[0].CreateTime
		latest := records[:0]
		for _, record := range records {
			if record.CreateTime != bestTime {
				continue
			}
			latest = append(latest, record)
		}
		records = latest
	}
	fileIDs := []string{}
	sedimentIDs := []string{}
	for _, record := range records {
		fileIDs = append(fileIDs, record.FileIDs...)
		sedimentIDs = append(sedimentIDs, record.SedimentIDs...)
	}
	return uniqueStrings(fileIDs), uniqueStrings(sedimentIDs)
}

type imagePollTarget struct {
	TurnExchangeID string
	ImageGenTaskID string
	MessageIDs     []string
}

func (t imagePollTarget) hasTarget() bool {
	return t.TurnExchangeID != "" || t.ImageGenTaskID != "" || len(t.MessageIDs) > 0
}

func imagePollTargetFromEvent(event map[string]any) imagePollTarget {
	target := imagePollTarget{
		TurnExchangeID: cleanString(event["turn_exchange_id"]),
		ImageGenTaskID: cleanString(event["image_gen_task_id"]),
		MessageIDs:     stringList(event["message_ids"]),
	}
	if messageID := cleanString(event["message_id"]); messageID != "" {
		target.MessageIDs = append(target.MessageIDs, messageID)
	}
	target.MessageIDs = uniqueStrings(target.MessageIDs)
	if target.TurnExchangeID == "" && target.ImageGenTaskID == "" {
		target.MessageIDs = nil
	}
	return target
}

func imageNodeMatchesPollTarget(key string, node, message map[string]any, mapping map[string]any, target imagePollTarget, seen map[string]bool) bool {
	if !target.hasTarget() {
		return true
	}
	if key != "" {
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	if imageMessageMatchesPollTarget(key, node, message, target) {
		return true
	}
	parentKey := firstNonEmpty(cleanString(node["parent"]), cleanString(node["parent_id"]))
	if parentKey == "" {
		return false
	}
	parentNode := mapValue(mapping[parentKey])
	if len(parentNode) == 0 {
		return imagePollTargetContainsMessageID(target, parentKey)
	}
	return imageNodeMatchesPollTarget(parentKey, parentNode, mapValue(parentNode["message"]), mapping, target, seen)
}

func imageMessageMatchesPollTarget(key string, node, message map[string]any, target imagePollTarget) bool {
	metadata := mapValue(message["metadata"])
	if target.TurnExchangeID != "" && cleanString(metadata["turn_exchange_id"]) == target.TurnExchangeID {
		return true
	}
	if target.ImageGenTaskID != "" && cleanString(metadata["image_gen_task_id"]) == target.ImageGenTaskID {
		return true
	}
	for _, id := range []string{
		key,
		cleanString(message["id"]),
		cleanString(metadata["message_id"]),
		cleanString(metadata["parent_id"]),
		cleanString(node["parent"]),
		cleanString(node["parent_id"]),
	} {
		if imagePollTargetContainsMessageID(target, id) {
			return true
		}
	}
	return false
}

func imagePollTargetContainsMessageID(target imagePollTarget, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, candidate := range target.MessageIDs {
		if strings.TrimSpace(candidate) == id {
			return true
		}
	}
	return false
}

type imageToolRecord struct {
	MessageID   string
	CreateTime  float64
	FileIDs     []string
	SedimentIDs []string
}

func normalizeImageModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" || model == "auto" {
		return "gpt-image-2"
	}
	return model
}

func isSupportedImageModel(model string) bool {
	switch strings.TrimSpace(model) {
	case "auto", "gpt-image-1", "gpt-image-2", "codex-gpt-image-2":
		return true
	default:
		return false
	}
}

func imageModelSlug(model string) string {
	switch strings.TrimSpace(model) {
	case "gpt-image-2":
		return "gpt-5-3"
	case "codex-gpt-image-2":
		return "codex-gpt-image-2"
	default:
		return "auto"
	}
}

func imageExtensionFromMime(mimeType string, raw []byte) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/png":
		return ".png"
	}
	switch http.DetectContentType(raw) {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

func normalizeInputImageMime(inputMime string, raw []byte, fileName string) string {
	if mimeType := strings.ToLower(strings.TrimSpace(inputMime)); strings.HasPrefix(mimeType, "image/") {
		return mimeType
	}
	if ext := strings.ToLower(filepath.Ext(fileName)); ext != "" {
		if mimeType := mime.TypeByExtension(ext); mimeType != "" {
			return mimeType
		}
	}
	return http.DetectContentType(raw)
}

func imageDimensions(raw []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func buildImagePrompt(prompt, size string) string {
	prompt = strings.TrimSpace(prompt)
	size = strings.TrimSpace(size)
	if size == "" {
		return prompt
	}
	hints := map[string]string{
		"1:1":   "输出为 1:1 正方形构图，主体居中，适合正方形画幅。",
		"3:2":   "输出为 3:2 横版构图，适合摄影、产品展示和横向叙事画幅。",
		"2:3":   "输出为 2:3 竖版构图，适合海报、人物和纵向叙事画幅。",
		"16:9":  "输出为 16:9 横屏构图，适合宽画幅展示。",
		"9:16":  "输出为 9:16 竖屏构图，适合竖版画幅展示。",
		"4:3":   "输出为 4:3 比例，兼顾宽度与高度，适合展示画面细节。",
		"3:4":   "输出为 3:4 比例，纵向构图，适合人物肖像或竖向场景。",
		"1080p": "以 1080 x 1080 像素对应的正方形画幅作为构图偏好，实际像素以上游返回为准。",
		"2k":    "以 2048 x 2048 像素对应的正方形画幅作为构图偏好，实际像素以上游返回为准。",
		"4k":    "以 2880 x 2880 像素对应的正方形画幅作为构图偏好，实际像素以上游返回为准。",
	}
	if hint := hints[size]; hint != "" {
		return prompt + "\n\n" + hint
	}
	if regexp.MustCompile(`^\d+x\d+$`).MatchString(strings.ToLower(size)) {
		return prompt + "\n\n以 " + size + " 像素对应的宽高比作为构图偏好，实际像素以上游返回为准。"
	}
	return prompt + "\n\n输出图片，宽高比为 " + size + "。"
}

func mapValue(value any) map[string]any {
	if item, ok := value.(map[string]any); ok {
		return item
	}
	return map[string]any{}
}

func stringList(value any) []string {
	out := []string{}
	if list, ok := value.([]string); ok {
		return uniqueStrings(list)
	}
	for _, raw := range anyList(value) {
		if text := strings.TrimSpace(fmt.Sprint(raw)); text != "" {
			out = append(out, text)
		}
	}
	return uniqueStrings(out)
}

func extractFileIDs(text string) []string {
	fileIDs := []string{}
	for _, hit := range regexp.MustCompile(`file[-_][A-Za-z0-9]+`).FindAllString(text, -1) {
		if strings.HasPrefix(hit, "file-service") {
			continue
		}
		fileIDs = append(fileIDs, hit)
	}
	return uniqueStrings(fileIDs)
}

func extractSedimentIDs(text string) []string {
	sedimentIDs := []string{}
	for _, match := range regexp.MustCompile(`sediment://([A-Za-z0-9_-]+)`).FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			sedimentIDs = append(sedimentIDs, match[1])
		}
	}
	return uniqueStrings(sedimentIDs)
}

func filterFileIDs(values []string) []string {
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "file_upload" {
			continue
		}
		out = append(out, value)
	}
	return uniqueStrings(out)
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func floatValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		var out float64
		_, _ = fmt.Sscanf(cleanString(value), "%f", &out)
		return out
	}
}

func firstDuration(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func backoffDuration(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 4 {
		attempt = 4
	}
	return time.Duration(1<<attempt) * time.Second
}

func isRetryablePollError(err error) bool {
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"http 429", "http 500", "http 502", "http 503", "http 504", "timeout", "temporarily"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
