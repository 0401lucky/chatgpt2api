package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type ConversationStreamer interface {
	StreamConversation(ctx context.Context, accessToken string, messages []map[string]any, model, prompt string) (<-chan string, <-chan error)
}

type ConversationState struct {
	Text           string
	ConversationID string
	MessageID      string
	FileIDs        []string
	SedimentIDs    []string
	Blocked        bool
	ToolInvoked    *bool
	TurnUseCase    string
	TurnExchangeID string
	ImageGenTaskID string
	MessageIDs     []string
}

func ChatCompletion(ctx context.Context, streamer ConversationStreamer, accessToken string, body map[string]any) (map[string]any, error) {
	model, messages, err := TextChatParts(body)
	if err != nil {
		return nil, err
	}
	return cachedTextChatCompletion(ctx, body, messages, func() (map[string]any, error) {
		text, err := CollectText(ctx, streamer, accessToken, model, messages)
		if err != nil {
			return nil, err
		}
		return CompletionResponse(model, text, messages), nil
	})
}

func StreamChatCompletion(ctx context.Context, streamer ConversationStreamer, accessToken string, body map[string]any) (<-chan map[string]any, <-chan error, error) {
	model, messages, err := TextChatParts(body)
	if err != nil {
		return nil, nil, err
	}
	chunks, errCh := StreamTextChatCompletion(ctx, streamer, accessToken, model, messages)
	return chunks, errCh, nil
}

func StreamTextChatCompletion(ctx context.Context, streamer ConversationStreamer, accessToken, model string, messages []map[string]any) (<-chan map[string]any, <-chan error) {
	out := make(chan map[string]any)
	errOut := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errOut)
		deltas, errCh := StreamTextDeltas(ctx, streamer, accessToken, model, messages)
		id := "chatcmpl-" + newHex(32)
		created := time.Now().Unix()
		sentRole := false
		for deltaText := range deltas {
			if !sentRole {
				sentRole = true
				out <- CompletionChunk(model, map[string]any{"role": "assistant", "content": deltaText}, nil, id, created)
				continue
			}
			out <- CompletionChunk(model, map[string]any{"content": deltaText}, nil, id, created)
		}
		if err := <-errCh; err != nil {
			errOut <- err
			return
		}
		if !sentRole {
			out <- CompletionChunk(model, map[string]any{"role": "assistant", "content": ""}, nil, id, created)
		}
		out <- CompletionChunk(model, map[string]any{}, "stop", id, created)
		errOut <- nil
	}()
	return out, errOut
}

func CollectText(ctx context.Context, streamer ConversationStreamer, accessToken, model string, messages []map[string]any) (string, error) {
	deltas, errCh := StreamTextDeltas(ctx, streamer, accessToken, model, messages)
	parts := []string{}
	for delta := range deltas {
		parts = append(parts, delta)
	}
	return strings.Join(parts, ""), <-errCh
}

func StreamTextDeltas(ctx context.Context, streamer ConversationStreamer, accessToken, model string, messages []map[string]any) (<-chan string, <-chan error) {
	out := make(chan string)
	errOut := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errOut)
		payloads, upstreamErr := streamer.StreamConversation(ctx, accessToken, messages, model, "")
		iterErr := IterConversationPayloads(ctx, payloads, AssistantHistoryText(messages), AssistantHistoryMessages(messages), func(event map[string]any) error {
			if event["type"] != "conversation.delta" {
				return nil
			}
			delta := clean(event["delta"])
			if delta == "" {
				return nil
			}
			select {
			case out <- delta:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		upErr := <-upstreamErr
		if iterErr != nil {
			errOut <- iterErr
			return
		}
		errOut <- upErr
	}()
	return out, errOut
}

func TextChatParts(body map[string]any) (string, []map[string]any, error) {
	model := firstNonEmpty(clean(body["model"]), "auto")
	messages, err := ChatMessagesFromBody(body)
	if err != nil {
		return "", nil, err
	}
	return model, NormalizeMessages(messages), nil
}

func ChatMessagesFromBody(body map[string]any) ([]map[string]any, error) {
	if messages := asMapSlice(body["messages"]); len(messages) > 0 {
		return messages, nil
	}
	if prompt := strings.TrimSpace(clean(body["prompt"])); prompt != "" {
		return []map[string]any{{"role": "user", "content": prompt}}, nil
	}
	return nil, fmt.Errorf("messages or prompt is required")
}

func NormalizeMessages(messages []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		content, ok := NormalizeMessageContent(message["content"])
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"role":    firstNonEmpty(clean(message["role"]), "user"),
			"content": content,
		})
	}
	return out
}

func NormalizeMessageContent(content any) (any, bool) {
	switch value := content.(type) {
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return nil, false
		}
		return value, true
	case []any:
		parts := make([]any, 0, len(value))
		for _, raw := range value {
			switch item := raw.(type) {
			case string:
				if strings.TrimSpace(item) != "" {
					parts = append(parts, map[string]any{"type": "text", "text": item})
				}
			case map[string]any:
				normalized, ok := normalizeContentPart(item)
				if ok {
					parts = append(parts, normalized)
				}
			}
		}
		if len(parts) == 0 {
			return nil, false
		}
		return parts, true
	default:
		text := strings.TrimSpace(clean(content))
		if text == "" {
			return nil, false
		}
		return text, true
	}
}

func normalizeContentPart(item map[string]any) (map[string]any, bool) {
	partType := strings.ToLower(clean(item["type"]))
	switch partType {
	case "text", "input_text", "output_text":
		text := clean(item["text"])
		if strings.TrimSpace(text) == "" {
			return nil, false
		}
		return map[string]any{"type": partType, "text": text}, true
	case "image_url", "input_image", "image":
		next := copyAnyMap(item)
		if partType == "" {
			next["type"] = "image_url"
		}
		return next, true
	default:
		return nil, false
	}
}

func MessageText(content any) string {
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
				switch strings.ToLower(clean(item["type"])) {
				case "text", "input_text", "output_text":
					parts = append(parts, clean(item["text"]))
				}
			}
		}
		return strings.Join(parts, "")
	default:
		return clean(content)
	}
}

func MessagesHaveImage(messages []map[string]any) bool {
	for _, message := range messages {
		if ContentHasImage(message["content"]) {
			return true
		}
	}
	return false
}

func ContentHasImage(content any) bool {
	for _, raw := range anyList(content) {
		item := asMap(raw)
		switch strings.ToLower(clean(item["type"])) {
		case "image_url", "input_image", "image":
			return true
		}
	}
	return false
}

func copyAnyMap(item map[string]any) map[string]any {
	out := make(map[string]any, len(item))
	for key, value := range item {
		out[key] = value
	}
	return out
}

func IterConversationPayloads(ctx context.Context, payloads <-chan string, historyText string, historyMessages []string, emit func(map[string]any) error) error {
	state := &ConversationState{}
	historyIndex := 0
	for payload := range payloads {
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			event := conversationBaseEvent("conversation.done", state)
			event["done"] = true
			if err := emit(event); err != nil {
				return err
			}
			break
		}
		var raw any
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			updateConversationState(state, payload, nil)
			event := conversationBaseEvent("conversation.raw", state)
			event["payload"] = payload
			if err := emit(event); err != nil {
				return err
			}
			continue
		}
		eventMap, ok := raw.(map[string]any)
		if !ok {
			event := conversationBaseEvent("conversation.event", state)
			event["raw"] = raw
			if err := emit(event); err != nil {
				return err
			}
			continue
		}
		updateConversationState(state, payload, eventMap)
		if historyIndex < len(historyMessages) && EventAssistantText(eventMap, historyText) == historyMessages[historyIndex] {
			historyIndex++
			state.Text = ""
			continue
		}
		nextText := AssistantText(eventMap, state.Text, historyText)
		if nextText != state.Text {
			delta := nextText
			if strings.HasPrefix(nextText, state.Text) {
				delta = nextText[len(state.Text):]
			}
			state.Text = nextText
			event := conversationBaseEvent("conversation.delta", state)
			event["raw"] = eventMap
			event["delta"] = delta
			if err := emit(event); err != nil {
				return err
			}
			continue
		}
		event := conversationBaseEvent("conversation.event", state)
		event["raw"] = eventMap
		if err := emit(event); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func AssistantText(event map[string]any, currentText, historyText string) string {
	for _, candidate := range []any{event, event["v"]} {
		m := asMap(candidate)
		message := asMap(m["message"])
		author := asMap(message["author"])
		if strings.ToLower(clean(author["role"])) != "assistant" {
			continue
		}
		text := AssistantMessageText(message)
		if text != "" {
			return StripHistory(text, historyText)
		}
	}
	return ApplyTextPatch(event, currentText, historyText)
}

func EventAssistantText(event map[string]any, historyText string) string {
	for _, candidate := range []any{event, event["v"]} {
		m := asMap(candidate)
		message := asMap(m["message"])
		author := asMap(message["author"])
		if clean(author["role"]) == "assistant" {
			return StripHistory(AssistantMessageText(message), historyText)
		}
	}
	return ""
}

func AssistantMessageText(message map[string]any) string {
	content := asMap(message["content"])
	var out []string
	for _, part := range anyList(content["parts"]) {
		if text, ok := part.(string); ok {
			out = append(out, text)
		}
	}
	return strings.Join(out, "")
}

func StripHistory(text, historyText string) string {
	for historyText != "" && strings.HasPrefix(text, historyText) {
		text = text[len(historyText):]
	}
	return text
}

func ApplyTextPatch(event map[string]any, currentText, historyText string) string {
	if event["p"] == "/message/content/parts/0" {
		return ApplyPatchOp(event, currentText, historyText)
	}
	if value, ok := event["v"].(string); ok && currentText != "" && event["p"] == nil && event["o"] == nil {
		return currentText + value
	}
	if event["o"] == "patch" {
		text := currentText
		for _, raw := range anyList(event["v"]) {
			if op, ok := raw.(map[string]any); ok {
				text = ApplyTextPatch(op, text, historyText)
			}
		}
		return text
	}
	text := currentText
	for _, raw := range anyList(event["v"]) {
		if op, ok := raw.(map[string]any); ok {
			text = ApplyTextPatch(op, text, historyText)
		}
	}
	return text
}

func ApplyPatchOp(operation map[string]any, currentText, historyText string) string {
	value := clean(operation["v"])
	switch operation["o"] {
	case "append":
		return currentText + value
	case "replace":
		return StripHistory(value, historyText)
	default:
		return currentText
	}
}

func updateConversationState(state *ConversationState, payload string, event map[string]any) {
	if match := regexp.MustCompile(`"conversation_id"\s*:\s*"([^"]+)"`).FindStringSubmatch(payload); len(match) > 1 && state.ConversationID == "" {
		state.ConversationID = match[1]
	}
	_, fileIDs, sedimentIDs := extractConversationIDs(payload)
	imageContext := false
	if event != nil {
		imageContext = isImageToolEvent(event)
		if event["o"] == "patch" && (strings.Contains(payload, "asset_pointer") || strings.Contains(payload, "file-service://")) {
			imageContext = true
		}
	}
	if state.ToolInvoked != nil && *state.ToolInvoked {
		imageContext = true
	}
	if imageContext {
		state.FileIDs = addUniqueStrings(state.FileIDs, fileIDs)
		state.SedimentIDs = addUniqueStrings(state.SedimentIDs, sedimentIDs)
	}
	if event == nil {
		return
	}
	if id := clean(event["conversation_id"]); id != "" {
		state.ConversationID = id
	}
	value := asMap(event["v"])
	if id := clean(value["conversation_id"]); id != "" {
		state.ConversationID = id
	}
	if event["type"] == "moderation" {
		moderation := asMap(event["moderation_response"])
		if blocked, ok := moderation["blocked"].(bool); ok && blocked {
			state.Blocked = true
		}
	}
	if event["type"] == "server_ste_metadata" {
		metadata := asMap(event["metadata"])
		if invoked, ok := metadata["tool_invoked"].(bool); ok {
			state.ToolInvoked = &invoked
		}
		if useCase := clean(metadata["turn_use_case"]); useCase != "" {
			state.TurnUseCase = useCase
		}
	}
	if messageID := assistantMessageIDFromPayload(event); messageID != "" {
		state.MessageID = messageID
	}
	mergeConversationPollTarget(state, conversationPollTargetFromPayload(event))
}

func conversationBaseEvent(eventType string, state *ConversationState) map[string]any {
	var toolInvoked any
	if state.ToolInvoked != nil {
		toolInvoked = *state.ToolInvoked
	}
	return map[string]any{
		"type":              eventType,
		"text":              state.Text,
		"conversation_id":   state.ConversationID,
		"message_id":        state.MessageID,
		"file_ids":          append([]string{}, state.FileIDs...),
		"sediment_ids":      append([]string{}, state.SedimentIDs...),
		"blocked":           state.Blocked,
		"tool_invoked":      toolInvoked,
		"turn_use_case":     state.TurnUseCase,
		"turn_exchange_id":  state.TurnExchangeID,
		"image_gen_task_id": state.ImageGenTaskID,
		"message_ids":       append([]string{}, state.MessageIDs...),
	}
}

type conversationPollTarget struct {
	TurnExchangeID string
	ImageGenTaskID string
	MessageIDs     []string
}

func (t conversationPollTarget) hasTarget() bool {
	return t.TurnExchangeID != "" || t.ImageGenTaskID != "" || len(t.MessageIDs) > 0
}

func mergeConversationPollTarget(state *ConversationState, target conversationPollTarget) {
	if !target.hasTarget() {
		return
	}
	if target.TurnExchangeID != "" && state.TurnExchangeID != "" && target.TurnExchangeID != state.TurnExchangeID {
		state.MessageIDs = nil
		state.ImageGenTaskID = ""
	}
	if target.ImageGenTaskID != "" && state.ImageGenTaskID != "" && target.ImageGenTaskID != state.ImageGenTaskID {
		state.MessageIDs = nil
		state.TurnExchangeID = ""
	}
	if target.TurnExchangeID != "" {
		state.TurnExchangeID = target.TurnExchangeID
	}
	if target.ImageGenTaskID != "" {
		state.ImageGenTaskID = target.ImageGenTaskID
	}
	state.MessageIDs = addUniqueStrings(state.MessageIDs, target.MessageIDs)
}

func conversationPollTargetFromPayload(event map[string]any) conversationPollTarget {
	target := conversationPollTarget{}
	mergeMetadata := func(metadata map[string]any) {
		if id := clean(metadata["turn_exchange_id"]); id != "" {
			target.TurnExchangeID = id
		}
		if id := clean(metadata["image_gen_task_id"]); id != "" {
			target.ImageGenTaskID = id
		}
		for _, key := range []string{"message_id", "parent_id"} {
			if id := clean(metadata[key]); id != "" {
				target.MessageIDs = addUniqueStrings(target.MessageIDs, []string{id})
			}
		}
	}
	mergeMessage := func(message map[string]any) {
		if len(message) == 0 {
			return
		}
		if id := clean(message["id"]); id != "" {
			target.MessageIDs = addUniqueStrings(target.MessageIDs, []string{id})
		}
		mergeMetadata(asMap(message["metadata"]))
	}
	mergeMetadata(asMap(event["metadata"]))
	if id := clean(event["message_id"]); id != "" {
		target.MessageIDs = addUniqueStrings(target.MessageIDs, []string{id})
	}
	mergeMessage(asMap(event["message"]))
	value := asMap(event["v"])
	mergeMessage(asMap(value["message"]))
	if target.TurnExchangeID == "" && target.ImageGenTaskID == "" {
		target.MessageIDs = nil
	}
	return target
}

func assistantMessageIDFromPayload(event map[string]any) string {
	if id := assistantMessageIDFromMessage(asMap(event["message"])); id != "" {
		return id
	}
	if id := clean(event["message_id"]); id != "" {
		return id
	}
	value := asMap(event["v"])
	return assistantMessageIDFromMessage(asMap(value["message"]))
}

func assistantMessageIDFromMessage(message map[string]any) string {
	id := clean(message["id"])
	if id == "" {
		return ""
	}
	author := asMap(message["author"])
	role := clean(author["role"])
	if role != "" && !strings.EqualFold(role, "assistant") {
		return ""
	}
	return id
}

func extractConversationIDs(payload string) (string, []string, []string) {
	conversationID := ""
	if match := regexp.MustCompile(`"conversation_id"\s*:\s*"([^"]+)"`).FindStringSubmatch(payload); len(match) > 1 {
		conversationID = match[1]
	}
	fileIDs := []string{}
	for _, hit := range regexp.MustCompile(`file[-_][A-Za-z0-9]+`).FindAllString(payload, -1) {
		if strings.HasPrefix(hit, "file-service") {
			continue
		}
		fileIDs = addUniqueStrings(fileIDs, []string{hit})
	}
	sedimentIDs := []string{}
	for _, match := range regexp.MustCompile(`sediment://([A-Za-z0-9_-]+)`).FindAllStringSubmatch(payload, -1) {
		if len(match) > 1 {
			sedimentIDs = addUniqueStrings(sedimentIDs, []string{match[1]})
		}
	}
	return conversationID, fileIDs, sedimentIDs
}

func isImageToolEvent(event map[string]any) bool {
	value := asMap(event["v"])
	message := asMap(event["message"])
	if len(message) == 0 {
		message = asMap(value["message"])
	}
	author := asMap(message["author"])
	if clean(author["role"]) != "tool" {
		return false
	}
	metadata := asMap(message["metadata"])
	if clean(metadata["async_task_type"]) == "image_gen" {
		return true
	}
	content := asMap(message["content"])
	if clean(content["content_type"]) != "multimodal_text" {
		return false
	}
	for _, raw := range anyList(content["parts"]) {
		part := asMap(raw)
		if clean(part["content_type"]) == "image_asset_pointer" {
			return true
		}
		assetPointer := clean(part["asset_pointer"])
		if strings.HasPrefix(assetPointer, "file-service://") || strings.HasPrefix(assetPointer, "sediment://") {
			return true
		}
	}
	return false
}

func addUniqueStrings(values []string, candidates []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		values = append(values, candidate)
	}
	return values
}

func AssistantHistoryText(messages []map[string]any) string {
	parts := []string{}
	for _, item := range messages {
		if clean(item["role"]) == "assistant" {
			parts = append(parts, clean(item["content"]))
		}
	}
	return strings.Join(parts, "")
}

func AssistantHistoryMessages(messages []map[string]any) []string {
	out := []string{}
	for _, item := range messages {
		if clean(item["role"]) == "assistant" && clean(item["content"]) != "" {
			out = append(out, clean(item["content"]))
		}
	}
	return out
}

func CompletionChunk(model string, delta map[string]any, finishReason any, completionID string, created int64) map[string]any {
	if completionID == "" {
		completionID = "chatcmpl-" + newHex(32)
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	return map[string]any{
		"id":      completionID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finishReason}},
	}
}

func CompletionResponse(model, content string, messages []map[string]any) map[string]any {
	promptTokens := CountMessageTokens(messages, model)
	completionTokens := CountTextTokens(content, model)
	return map[string]any{
		"id":      "chatcmpl-" + newHex(32),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
}

func CountMessageTokens(messages []map[string]any, model string) int {
	total := 3
	for _, message := range messages {
		total += 3
		for _, value := range message {
			if text, ok := value.(string); ok {
				total += CountTextTokens(text, model)
			}
		}
	}
	return total
}

func CountTextTokens(text, model string) int {
	if text == "" {
		return 0
	}
	asciiCount, nonASCII := 0, 0
	for _, r := range text {
		if r < 128 {
			asciiCount++
		} else {
			nonASCII++
		}
	}
	return (asciiCount+3)/4 + nonASCII
}

func IsStream(body map[string]any) bool {
	switch value := body["stream"].(type) {
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

func asMapSlice(value any) []map[string]any {
	switch list := value.(type) {
	case []map[string]any:
		return list
	case []any:
		out := make([]map[string]any, 0, len(list))
		for _, raw := range list {
			if item, ok := raw.(map[string]any); ok {
				out = append(out, item)
			}
		}
		return out
	default:
		return nil
	}
}

func asMap(value any) map[string]any {
	if item, ok := value.(map[string]any); ok {
		return item
	}
	return map[string]any{}
}

func anyList(value any) []any {
	switch list := value.(type) {
	case []any:
		return list
	case []map[string]any:
		out := make([]any, 0, len(list))
		for _, item := range list {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func clean(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newHex(length int) string {
	value := strings.ReplaceAll(fmt.Sprintf("%d%s", time.Now().UnixNano(), strings.ReplaceAll(time.Now().String(), " ", "")), "-", "")
	if len(value) >= length {
		return value[:length]
	}
	return value + strings.Repeat("0", length-len(value))
}
