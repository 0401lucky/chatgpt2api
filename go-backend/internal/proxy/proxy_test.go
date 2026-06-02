package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEdgeProfilePreservesRequestBrowserHeaders(t *testing.T) {
	const edgeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36 Edg/143.0.0.0"
	const edgeCHUA = `"Microsoft Edge";v="143", "Chromium";v="143", "Not A(Brand";v="24"`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != edgeUA {
			t.Fatalf("user-agent = %q", got)
		}
		if got := r.Header.Get("Sec-Ch-Ua"); got != edgeCHUA {
			t.Fatalf("sec-ch-ua = %q", got)
		}
		if got := r.Header.Get("Accept-Language"); got != "zh-CN,zh;q=0.9,en;q=0.8" {
			t.Fatalf("accept-language = %q", got)
		}
		if strings.Contains(r.Header.Get("Sec-Ch-Ua"), "Google Chrome") {
			t.Fatalf("edge profile leaked chrome header: %q", r.Header.Get("Sec-Ch-Ua"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := (&Service{}).BrowserHTTPClientWithProfile("edge101", 5*time.Second)
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", edgeUA)
	req.Header.Set("Sec-Ch-Ua", edgeCHUA)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
}
