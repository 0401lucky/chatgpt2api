package protocol

import (
	"context"
	"testing"
)

func TestResponseMessagesPreserveInputImageParts(t *testing.T) {
	messages := responseMessages([]any{map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "input_text", "text": "look"},
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64,aW1n"},
		},
	}}, "system note")
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if !MessagesHaveImage(messages) {
		t.Fatalf("image part was dropped: %#v", messages)
	}
	if got := MessageText(messages[1]["content"]); got != "look" {
		t.Fatalf("text = %q", got)
	}
}

func TestResponseWebSearchToolOutputsSearchCall(t *testing.T) {
	streamer := &searchCountingStreamer{
		result: map[string]any{
			"answer":  "搜索答案",
			"sources": []map[string]string{{"title": "Example", "url": "https://example.com/a"}},
		},
	}
	accounts := &responseSearchAccounts{token: "token"}
	result, err := Response(map[string]any{
		"model": "gpt-5-mini",
		"input": "今天有什么消息",
		"tools": []any{map[string]any{"type": "web_search_preview"}},
	}, streamer, nil, accounts)
	if err != nil {
		t.Fatal(err)
	}
	output := result["output"].([]map[string]any)
	if len(output) != 2 || output[0]["type"] != "web_search_call" || output[1]["type"] != "message" {
		t.Fatalf("output = %#v", output)
	}
	content := output[1]["content"].([]map[string]any)[0]
	annotations := content["annotations"].([]map[string]any)
	if len(annotations) != 1 || annotations[0]["url"] != "https://example.com/a" {
		t.Fatalf("annotations = %#v", annotations)
	}
	if streamer.searches != 1 || streamer.model != SearchModel {
		t.Fatalf("searches=%d model=%q", streamer.searches, streamer.model)
	}
}

type responseSearchAccounts struct {
	token   string
	invalid int
}

func (a *responseSearchAccounts) AcquireImageToken(ctx context.Context, allow func(map[string]any) bool) (string, func(), error) {
	return a.token, func() {}, nil
}

func (a *responseSearchAccounts) GetAvailableAccessTokenFor(ctx context.Context, allow func(map[string]any) bool) (string, error) {
	return a.token, nil
}

func (a *responseSearchAccounts) MarkImageResult(accessToken string, success bool) map[string]any {
	return nil
}

func (a *responseSearchAccounts) MarkInvalidToken(accessToken string) map[string]any {
	a.invalid++
	return nil
}
