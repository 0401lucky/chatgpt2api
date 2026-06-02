package proxy

import (
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
	return impersonate.Chrome()
}

type errorTransport struct {
	err error
}

func (t errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}
