package protocol

import "testing"

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
