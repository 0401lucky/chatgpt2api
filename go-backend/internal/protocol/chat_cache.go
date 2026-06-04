package protocol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

const (
	chatCompletionCacheTTL        = 60 * time.Second
	chatCompletionCacheMaxEntries = 256
)

var chatCacheableKeys = map[string]struct{}{
	"frequency_penalty":     {},
	"max_completion_tokens": {},
	"max_tokens":            {},
	"metadata":              {},
	"model":                 {},
	"presence_penalty":      {},
	"reasoning_effort":      {},
	"response_format":       {},
	"seed":                  {},
	"stop":                  {},
	"temperature":           {},
	"tool_choice":           {},
	"tools":                 {},
	"top_p":                 {},
	"user":                  {},
}

type chatCacheEntry struct {
	ExpiresAt time.Time
	Value     map[string]any
}

type inflightChatCall struct {
	cond  *sync.Cond
	done  bool
	value map[string]any
	err   error
}

type chatCompletionCacheStore struct {
	mu       sync.Mutex
	entries  map[string]chatCacheEntry
	inflight map[string]*inflightChatCall
}

var textChatCache = &chatCompletionCacheStore{
	entries:  map[string]chatCacheEntry{},
	inflight: map[string]*inflightChatCall{},
}

func ClearTextChatCacheForTest() {
	textChatCache.mu.Lock()
	defer textChatCache.mu.Unlock()
	textChatCache.entries = map[string]chatCacheEntry{}
	textChatCache.inflight = map[string]*inflightChatCall{}
}

func cachedTextChatCompletion(ctx context.Context, body map[string]any, messages []map[string]any, compute func() (map[string]any, error)) (map[string]any, error) {
	if IsSearchModel(clean(body["model"])) {
		return compute()
	}
	if MessagesHaveImage(messages) {
		return compute()
	}
	key := textChatCacheKey(body, messages)
	return textChatCache.getOrCompute(ctx, key, compute)
}

func (c *chatCompletionCacheStore) getOrCompute(ctx context.Context, key string, compute func() (map[string]any, error)) (map[string]any, error) {
	if key == "" {
		return compute()
	}
	now := time.Now()
	c.mu.Lock()
	c.pruneLocked(now)
	if entry, ok := c.entries[key]; ok && entry.ExpiresAt.After(now) {
		value := deepCopyMap(entry.Value)
		c.mu.Unlock()
		return value, nil
	}
	call := c.inflight[key]
	if call == nil {
		call = &inflightChatCall{}
		call.cond = sync.NewCond(&c.mu)
		c.inflight[key] = call
		c.mu.Unlock()
		return c.computeOwner(key, call, compute)
	}
	for !call.done {
		if ctx.Err() != nil {
			c.mu.Unlock()
			return nil, ctx.Err()
		}
		call.cond.Wait()
	}
	value := deepCopyMap(call.value)
	err := call.err
	c.mu.Unlock()
	return value, err
}

func (c *chatCompletionCacheStore) computeOwner(key string, call *inflightChatCall, compute func() (map[string]any, error)) (map[string]any, error) {
	value, err := compute()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && value != nil {
		c.entries[key] = chatCacheEntry{ExpiresAt: time.Now().Add(chatCompletionCacheTTL), Value: deepCopyMap(value)}
		c.pruneLocked(time.Now())
	}
	delete(c.inflight, key)
	call.value = deepCopyMap(value)
	call.err = err
	call.done = true
	call.cond.Broadcast()
	return value, err
}

func (c *chatCompletionCacheStore) pruneLocked(now time.Time) {
	for key, entry := range c.entries {
		if !entry.ExpiresAt.After(now) {
			delete(c.entries, key)
		}
	}
	for len(c.entries) > chatCompletionCacheMaxEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range c.entries {
			if oldestKey == "" || entry.ExpiresAt.Before(oldest) {
				oldestKey = key
				oldest = entry.ExpiresAt
			}
		}
		delete(c.entries, oldestKey)
	}
}

func textChatCacheKey(body map[string]any, messages []map[string]any) string {
	payload := map[string]any{"messages": messages, "stream": false}
	for key := range chatCacheableKeys {
		if value, ok := body[key]; ok {
			payload[key] = value
		}
	}
	data, err := json.Marshal(canonicalJSONValue(payload))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func canonicalJSONValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(item))
		for key, child := range item {
			out[key] = canonicalJSONValue(child)
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(item))
		for _, child := range item {
			out = append(out, canonicalJSONValue(child))
		}
		return out
	case []any:
		out := make([]any, 0, len(item))
		for _, child := range item {
			out = append(out, canonicalJSONValue(child))
		}
		return out
	default:
		return item
	}
}

func deepCopyMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return copyAnyMap(value)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return copyAnyMap(value)
	}
	return out
}
