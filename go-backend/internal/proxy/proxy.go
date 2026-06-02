package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/enetx/g"
	"github.com/enetx/surf"
)

type Service struct {
	proxyURL string
}

func NewService(proxyURL string) *Service {
	return &Service{proxyURL: strings.TrimSpace(proxyURL)}
}

func (s *Service) BrowserHTTPClientWithProfile(profile string, timeout time.Duration) *http.Client {
	return browserHTTPClientForProfile(s.proxyURL, profile, timeout)
}

func browserHTTPClientForProfile(proxyURL, profile string, timeout time.Duration) *http.Client {
	builder := surf.NewClient().
		Builder().
		SecureTLS()
	builder = applyBrowserProfile(builder, profile).
		Session().
		Timeout(timeout)
	if proxyURL = strings.TrimSpace(proxyURL); proxyURL != "" {
		builder = builder.Proxy(g.String(proxyURL))
	}
	client, err := builder.Build().Result()
	if err != nil {
		return &http.Client{Timeout: timeout, Transport: errorTransport{err: fmt.Errorf("browser HTTP client build failed: %w", err)}}
	}
	return client.Std()
}

func applyBrowserProfile(builder *surf.Builder, profile string) *surf.Builder {
	impersonate := builder.Impersonate()
	normalized := strings.ToLower(strings.TrimSpace(profile))
	switch {
	case strings.Contains(normalized, "android"):
		impersonate = impersonate.Android()
	case strings.Contains(normalized, "ios"), strings.Contains(normalized, "iphone"), strings.Contains(normalized, "ipad"):
		impersonate = impersonate.IOS()
	case strings.Contains(normalized, "mac"), strings.Contains(normalized, "darwin"):
		impersonate = impersonate.MacOS()
	case strings.Contains(normalized, "linux"):
		impersonate = impersonate.Linux()
	default:
		impersonate = impersonate.Windows()
	}
	if strings.Contains(normalized, "firefox") || strings.Contains(normalized, "ff") {
		return impersonate.Firefox()
	}
	if strings.Contains(normalized, "edge") {
		return applyEdgeProfile(builder, impersonate, normalized)
	}
	return impersonate.Chrome()
}

type edgeHeaderSnapshotKey struct{}

var edgeBrowserHeaders = []string{
	"User-Agent",
	"Accept-Language",
	"Sec-Ch-Ua",
	"Sec-Ch-Ua-Arch",
	"Sec-Ch-Ua-Bitness",
	"Sec-Ch-Ua-Full-Version",
	"Sec-Ch-Ua-Full-Version-List",
	"Sec-Ch-Ua-Mobile",
	"Sec-Ch-Ua-Model",
	"Sec-Ch-Ua-Platform",
	"Sec-Ch-Ua-Platform-Version",
}

func applyEdgeProfile(builder *surf.Builder, impersonate *surf.Impersonate, profile string) *surf.Builder {
	builder = builder.With(captureEdgeBrowserHeaders, -1)
	builder = impersonate.Chrome()
	builder = builder.With(restoreEdgeBrowserHeaders, 1)
	if strings.Contains(profile, "85") {
		return builder.JA().Edge85()
	}
	return builder.JA().Edge106()
}

func captureEdgeBrowserHeaders(req *surf.Request) error {
	headers := req.GetRequest().Header
	snapshot := make(map[string]string, len(edgeBrowserHeaders))
	for _, key := range edgeBrowserHeaders {
		if value := headers.Get(key); value != "" {
			snapshot[key] = value
		}
	}
	req.WithContext(context.WithValue(req.GetRequest().Context(), edgeHeaderSnapshotKey{}, snapshot))
	return nil
}

func restoreEdgeBrowserHeaders(req *surf.Request) error {
	snapshot, _ := req.GetRequest().Context().Value(edgeHeaderSnapshotKey{}).(map[string]string)
	if snapshot == nil {
		return nil
	}
	headers := req.GetRequest().Header
	for _, key := range edgeBrowserHeaders {
		if value, ok := snapshot[key]; ok {
			headers.Set(key, value)
			continue
		}
		headers.Del(key)
	}
	return nil
}

type errorTransport struct {
	err error
}

func (t errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}
