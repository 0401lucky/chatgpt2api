package register

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	mathrand "math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func clean(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func intValue(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		if typed > 0 {
			return typed
		}
	case int64:
		if typed > 0 {
			return int(typed)
		}
	case float64:
		if typed > 0 {
			return int(typed)
		}
	case json.Number:
		if n, err := typed.Int64(); err == nil && n > 0 {
			return int(n)
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func floatValue(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		if n, err := typed.Float64(); err == nil {
			return n
		}
	case string:
		var n float64
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%f", &n); err == nil {
			return n
		}
	}
	return fallback
}

func boolValue(value any, fallback bool) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return fallback
}

func asMap(value any) map[string]any {
	if item, ok := value.(map[string]any); ok {
		return item
	}
	return map[string]any{}
}

func asAnyList(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	case []string:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func asMapSlice(value any) []map[string]any {
	out := []map[string]any{}
	switch typed := value.(type) {
	case []map[string]any:
		for _, item := range typed {
			out = append(out, copyMap(item))
		}
	case []any:
		for _, raw := range typed {
			if item, ok := raw.(map[string]any); ok {
				out = append(out, copyMap(item))
			}
		}
	case map[string]any:
		out = append(out, copyMap(typed))
	}
	return out
}

func asStringSlice(value any) []string {
	out := []string{}
	switch typed := value.(type) {
	case []string:
		for _, item := range typed {
			if text := strings.TrimSpace(item); text != "" {
				out = append(out, text)
			}
		}
	case []any:
		for _, raw := range typed {
			if text := clean(raw); text != "" {
				out = append(out, text)
			}
		}
	case string:
		for _, part := range strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == '\t' }) {
			if text := strings.TrimSpace(part); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func copyMap(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return copyMap(value)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return copyMap(value)
	}
	return out
}

func mergeMap(left, right map[string]any) map[string]any {
	out := copyMap(left)
	for key, value := range right {
		out[key] = value
	}
	return out
}

func trimLogs(values []map[string]any, limit int) []map[string]any {
	if limit > 0 && len(values) > limit {
		return append([]map[string]any(nil), values[len(values)-limit:]...)
	}
	if values == nil {
		return []map[string]any{}
	}
	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func newHex(n int) string {
	if n < 1 {
		n = 16
	}
	raw := make([]byte, (n+1)/2)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	value := hex.EncodeToString(raw)
	if len(value) > n {
		return value[:n]
	}
	return value
}

func newUUID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		now := time.Now().UnixNano()
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", uint32(now), uint16(now>>32), uint16(now>>16), uint16(now>>48), uint64(now)&0xffffffffffff)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:])
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func round1(value float64) float64 {
	return math.Round(value*10) / 10
}

func randomLower(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(letters[mathrand.Intn(len(letters))])
	}
	return b.String()
}

func randomAlphaNum(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(chars[mathrand.Intn(len(chars))])
	}
	return b.String()
}

func httpClientForProxy(proxyURL string, timeout time.Duration) (*http.Client, error) {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil || parsed.Host == "" {
			return nil, fmt.Errorf("invalid proxy url")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https":
			transport.Proxy = http.ProxyURL(parsed)
		default:
			return nil, fmt.Errorf("unsupported proxy scheme: %s", parsed.Scheme)
		}
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}
