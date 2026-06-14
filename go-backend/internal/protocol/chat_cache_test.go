package protocol

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChatCompletionDedupesInflightTextRequests(t *testing.T) {
	ClearTextChatCacheForTest()
	streamer := &countingStreamer{delay: 40 * time.Millisecond}
	body := map[string]any{
		"model":    "auto",
		"messages": []any{map[string]any{"role": "user", "content": "ping"}},
	}

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := ChatCompletion(context.Background(), streamer, "token", body)
			if err != nil {
				errs <- err
				return
			}
			if got := firstChoiceContent(result); got != "pong" {
				t.Errorf("content = %#v", got)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if streamer.calls != 1 {
		t.Fatalf("stream calls = %d, want 1", streamer.calls)
	}
}

func TestChatCompletionDoesNotCacheImageInput(t *testing.T) {
	ClearTextChatCacheForTest()
	streamer := &countingStreamer{}
	body := map[string]any{
		"model": "auto",
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "describe"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64,aW1n"},
			},
		}},
	}
	for i := 0; i < 2; i++ {
		if _, err := ChatCompletion(context.Background(), streamer, "token", body); err != nil {
			t.Fatal(err)
		}
	}
	if streamer.calls != 2 {
		t.Fatalf("stream calls = %d, want 2", streamer.calls)
	}
}

func TestChatCompletionSearchModelUsesSearchBackend(t *testing.T) {
	ClearTextChatCacheForTest()
	streamer := &searchCountingStreamer{}
	body := map[string]any{
		"model": SearchModel,
		"messages": []any{
			map[string]any{"role": "system", "content": "保持简洁"},
			map[string]any{"role": "user", "content": "今天有什么消息"},
		},
	}
	result, err := ChatCompletion(context.Background(), streamer, "token", body)
	if err != nil {
		t.Fatal(err)
	}
	if got := firstChoiceContent(result); got != "搜索答案" {
		t.Fatalf("content = %#v", got)
	}
	if streamer.searches != 1 || streamer.calls != 0 {
		t.Fatalf("searches=%d stream calls=%d", streamer.searches, streamer.calls)
	}
	if streamer.prompt != "今天有什么消息" {
		t.Fatalf("prompt = %q", streamer.prompt)
	}
}

func TestChatCompletionWebSearchToolUsesSearchBackend(t *testing.T) {
	ClearTextChatCacheForTest()
	streamer := &searchCountingStreamer{
		result: map[string]any{
			"answer":  "搜索答案",
			"sources": []map[string]string{{"title": "Example", "url": "https://example.com/a"}},
		},
	}
	body := map[string]any{
		"model": "gpt-5-mini",
		"tools": []any{map[string]any{"type": "web_search_preview"}},
		"messages": []any{
			map[string]any{"role": "user", "content": "今天有什么消息"},
		},
	}
	result, err := ChatCompletion(context.Background(), streamer, "token", body)
	if err != nil {
		t.Fatal(err)
	}
	message := firstChoiceMessage(result)
	content := message["content"].(string)
	if !strings.Contains(content, "Sources:") || !strings.Contains(content, "https://example.com/a") {
		t.Fatalf("content = %q", content)
	}
	annotations := message["annotations"].([]map[string]any)
	if len(annotations) != 1 || annotations[0]["type"] != "url_citation" {
		t.Fatalf("annotations = %#v", annotations)
	}
	if streamer.searches != 1 || streamer.calls != 0 {
		t.Fatalf("searches=%d stream calls=%d", streamer.searches, streamer.calls)
	}
	if streamer.model != SearchModel {
		t.Fatalf("search model = %q", streamer.model)
	}
}

func firstChoiceContent(result map[string]any) any {
	return firstChoiceMessage(result)["content"]
}

func firstChoiceMessage(result map[string]any) map[string]any {
	switch choices := result["choices"].(type) {
	case []map[string]any:
		return choices[0]["message"].(map[string]any)
	case []any:
		choice := choices[0].(map[string]any)
		return choice["message"].(map[string]any)
	default:
		return nil
	}
}

type searchCountingStreamer struct {
	countingStreamer
	searches int
	prompt   string
	model    string
	result   map[string]any
}

func (s *searchCountingStreamer) Search(ctx context.Context, accessToken, prompt, model string) (map[string]any, error) {
	s.searches++
	s.prompt = prompt
	s.model = model
	if s.result != nil {
		return s.result, nil
	}
	return map[string]any{"answer": "搜索答案"}, nil
}

type countingStreamer struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
}

func (s *countingStreamer) StreamConversation(ctx context.Context, accessToken string, messages []map[string]any, model, prompt string) (<-chan string, <-chan error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	out := make(chan string, 2)
	errCh := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errCh)
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
		out <- `{"message":{"author":{"role":"assistant"},"content":{"parts":["pong"]}}}`
		out <- "[DONE]"
		errCh <- nil
	}()
	return out, errCh
}
