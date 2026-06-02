package upstream

import (
	"strings"
	"testing"
	"time"
)

func TestBuildPOWConfigMatchesPythonTimingSemantics(t *testing.T) {
	config := buildPOWConfig("test-agent", []string{"https://chatgpt.com/backend-api/sentinel/sdk.js"}, "test-build")
	if config[4] != "test-agent" {
		t.Fatalf("user agent = %#v", config[4])
	}
	if config[5] != "https://chatgpt.com/backend-api/sentinel/sdk.js" {
		t.Fatalf("script source = %#v", config[5])
	}
	if config[6] != "test-build" {
		t.Fatalf("data build = %#v", config[6])
	}

	perfNow, ok := config[13].(float64)
	if !ok || perfNow <= 0 {
		t.Fatalf("perfNow = %#v", config[13])
	}
	nowMS := float64(time.Now().UnixNano()) / 1e6
	if perfNow > nowMS/2 {
		t.Fatalf("perfNow looks like epoch milliseconds: %f", perfNow)
	}

	timeOrigin, ok := config[17].(float64)
	if !ok || timeOrigin <= nowMS-24*60*60*1000 || timeOrigin > nowMS {
		t.Fatalf("timeOrigin = %#v, nowMS = %f", config[17], nowMS)
	}
}

func TestBuildPOWConfigUsesPythonNavigatorSeparator(t *testing.T) {
	for i := 0; i < 200; i++ {
		config := buildPOWConfig("test-agent", nil, "")
		navigatorKey, ok := config[10].(string)
		if !ok {
			t.Fatalf("navigator key = %#v", config[10])
		}
		if navigatorKey == "doNotTrack" {
			continue
		}
		if !strings.Contains(navigatorKey, "−") {
			t.Fatalf("navigator key should use python-compatible separator: %q", navigatorKey)
		}
		return
	}
	t.Fatal("navigator key only returned doNotTrack")
}
