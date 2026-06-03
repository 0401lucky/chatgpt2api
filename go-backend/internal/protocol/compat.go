package protocol

import (
	"context"
	"fmt"
	"strings"
	"time"

	"chatgpt2api-go-backend/internal/account"
)

func Response(body map[string]any, chat ConversationStreamer, image ImageGenerator, accounts ImageAccountPool) (map[string]any, error) {
	model := firstNonEmpty(clean(body["model"]), "auto")
	prompt := responsePrompt(body["input"], body["instructions"])
	if prompt == "" {
		return nil, fmt.Errorf("input text is required")
	}
	id := "resp_" + newHex(32)
	created := time.Now().Unix()
	if hasImageTool(body["tools"]) {
		if image == nil || accounts == nil {
			return nil, fmt.Errorf("image generation upstream is not configured")
		}
		if model == "auto" {
			model = "gpt-image-2"
		}
		token, release, err := accounts.AcquireImageToken(context.Background(), nil)
		if err != nil {
			return nil, err
		}
		defer release()
		data, err := image.GenerateImage(context.Background(), token, prompt, model, "1:1", "b64_json")
		if err != nil {
			accounts.MarkImageResult(token, false)
			if account.IsInvalidTokenError(err) {
				accounts.MarkInvalidToken(token)
			}
			return nil, err
		}
		accounts.MarkImageResult(token, true)
		output := make([]map[string]any, 0, len(data))
		for i, item := range data {
			if b64 := clean(item["b64_json"]); b64 != "" {
				output = append(output, map[string]any{
					"id":             fmt.Sprintf("ig_%d", i+1),
					"type":           "image_generation_call",
					"status":         "completed",
					"result":         b64,
					"revised_prompt": firstNonEmpty(clean(item["revised_prompt"]), prompt),
				})
			}
		}
		return map[string]any{
			"id":         id,
			"object":     "response",
			"created_at": created,
			"status":     "completed",
			"error":      nil,
			"model":      model,
			"output":     output,
			"usage":      ImageUsage(prompt, len(output)),
		}, nil
	}
	if chat == nil || accounts == nil {
		return nil, fmt.Errorf("chat completions upstream is not configured")
	}
	messages := []map[string]any{{"role": "user", "content": prompt}}
	token, err := accounts.GetAvailableAccessTokenFor(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	text, err := CollectText(context.Background(), chat, token, model, messages)
	if err != nil {
		if account.IsInvalidTokenError(err) {
			accounts.MarkInvalidToken(token)
		}
		return nil, err
	}
	output := []map[string]any{{
		"id":      "msg_" + newHex(32),
		"type":    "message",
		"status":  "completed",
		"role":    "assistant",
		"content": []map[string]any{{"type": "output_text", "text": text, "annotations": []any{}}},
	}}
	return map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": created,
		"status":     "completed",
		"error":      nil,
		"model":      model,
		"output":     output,
	}, nil
}

func AnthropicMessage(body map[string]any, chat ConversationStreamer, accounts ImageAccountPool) (map[string]any, error) {
	if chat == nil || accounts == nil {
		return nil, fmt.Errorf("messages upstream is not configured")
	}
	model := firstNonEmpty(clean(body["model"]), "auto")
	messages := NormalizeMessages(asMapSlice(body["messages"]))
	if system := MessageText(body["system"]); strings.TrimSpace(system) != "" {
		messages = append([]map[string]any{{"role": "system", "content": system}}, messages...)
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages is required")
	}
	token, err := accounts.GetAvailableAccessTokenFor(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	text, err := CollectText(context.Background(), chat, token, model, messages)
	if err != nil {
		if account.IsInvalidTokenError(err) {
			accounts.MarkInvalidToken(token)
		}
		return nil, err
	}
	return map[string]any{
		"id":            "msg_" + newHex(32),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       []map[string]any{{"type": "text", "text": text}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  CountMessageTokens(messages, model),
			"output_tokens": CountTextTokens(text, model),
		},
	}, nil
}

func ImageUsage(prompt string, count int) map[string]any {
	input := CountTextTokens(prompt, "gpt-image-1")
	output := 1056 * count
	return map[string]any{"input_tokens": input, "output_tokens": output, "total_tokens": input + output}
}

func responsePrompt(input any, instructions any) string {
	parts := []string{}
	if text := strings.TrimSpace(clean(instructions)); text != "" {
		parts = append(parts, text)
	}
	switch value := input.(type) {
	case string:
		parts = append(parts, value)
	case map[string]any:
		if content := MessageText(value["content"]); content != "" {
			parts = append(parts, content)
		}
	case []any:
		for _, raw := range value {
			if item, ok := raw.(map[string]any); ok {
				if text := MessageText(item["content"]); text != "" {
					parts = append(parts, text)
					continue
				}
				if text := MessageText([]any{item}); text != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func hasImageTool(tools any) bool {
	for _, raw := range anyList(tools) {
		item := asMap(raw)
		if strings.EqualFold(clean(item["type"]), "image_generation") {
			return true
		}
	}
	return false
}
