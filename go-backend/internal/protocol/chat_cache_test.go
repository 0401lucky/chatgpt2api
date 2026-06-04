package protocol

import (
	"context"
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

func firstChoiceContent(result map[string]any) any {
	switch choices := result["choices"].(type) {
	case []map[string]any:
		return choices[0]["message"].(map[string]any)["content"]
	case []any:
		choice := choices[0].(map[string]any)
		return choice["message"].(map[string]any)["content"]
	default:
		return nil
	}
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
