package upstream

import (
	"bytes"
	"crypto/sha3"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"time"
)

const defaultPOWScript = "https://chatgpt.com/backend-api/sentinel/sdk.js"

var scriptSrcRE = regexp.MustCompile(`(?is)<script[^>]+src=["']([^"']+)["']`)

func parsePOWResources(html string) ([]string, string) {
	matches := scriptSrcRE.FindAllStringSubmatch(html, -1)
	sources := make([]string, 0, len(matches))
	dataBuild := ""
	for _, match := range matches {
		src := match[1]
		sources = append(sources, src)
		if dataBuild == "" {
			dataBuild = regexp.MustCompile(`c/[^/]*/_`).FindString(src)
		}
	}
	if len(sources) == 0 {
		sources = []string{defaultPOWScript}
	}
	if dataBuild == "" {
		if match := regexp.MustCompile(`<html[^>]*data-build=["']([^"']*)["']`).FindStringSubmatch(html); len(match) > 1 {
			dataBuild = match[1]
		}
	}
	return sources, dataBuild
}

func buildLegacyRequirementsToken(userAgent string, scriptSources []string, dataBuild string) string {
	seed := fmt.Sprintf("%f", rand.Float64())
	config := buildPOWConfig(userAgent, scriptSources, dataBuild)
	answer, _ := powGenerate(seed, "0fffff", config, 500000)
	return "gAAAAAC" + answer
}

func buildProofToken(seed, difficulty, userAgent string, scriptSources []string, dataBuild string) (string, error) {
	config := buildPOWConfig(userAgent, scriptSources, dataBuild)
	answer, solved := powGenerate(seed, difficulty, config, 500000)
	if !solved {
		return "", fmt.Errorf("failed to solve proof token: difficulty=%s", difficulty)
	}
	return "gAAAAAB" + answer, nil
}

func buildPOWConfig(userAgent string, scriptSources []string, dataBuild string) []any {
	if len(scriptSources) == 0 {
		scriptSources = []string{defaultPOWScript}
	}
	navigatorKeys := []string{
		"registerProtocolHandler-function registerProtocolHandler() { [native code] }",
		"storage-[object StorageManager]",
		"locks-[object LockManager]",
		"appCodeName-Mozilla",
		"permissions-[object Permissions]",
		"share-function share() { [native code] }",
		"webdriver-false",
		"managed-[object NavigatorManagedData]",
		"canShare-function canShare() { [native code] }",
		"vendor-Google Inc.",
		"mediaDevices-[object MediaDevices]",
		"vibrate-function vibrate() { [native code] }",
		"storageBuckets-[object StorageBucketManager]",
		"mediaCapabilities-[object MediaCapabilities]",
		"cookieEnabled-true",
		"virtualKeyboard-[object VirtualKeyboard]",
		"product-Gecko",
		"presentation-[object Presentation]",
		"onLine-true",
		"mimeTypes-[object MimeTypeArray]",
		"credentials-[object CredentialsContainer]",
		"serviceWorker-[object ServiceWorkerContainer]",
		"keyboard-[object Keyboard]",
		"gpu-[object GPU]",
		"doNotTrack",
		"serial-[object Serial]",
		"pdfViewerEnabled-true",
		"language-zh-CN",
		"geolocation-[object Geolocation]",
		"userAgentData-[object NavigatorUAData]",
		"getUserMedia-function getUserMedia() { [native code] }",
		"sendBeacon-function sendBeacon() { [native code] }",
		"hardwareConcurrency-32",
		"windowControlsOverlay-[object WindowControlsOverlay]",
	}
	windowKeys := []string{
		"0", "window", "self", "document", "name", "location", "customElements", "history", "navigation",
		"innerWidth", "innerHeight", "scrollX", "scrollY", "visualViewport", "screenX", "screenY", "outerWidth",
		"outerHeight", "devicePixelRatio", "screen", "chrome", "navigator", "onresize", "performance", "crypto",
		"indexedDB", "sessionStorage", "localStorage", "scheduler", "alert", "atob", "btoa", "fetch", "matchMedia",
		"postMessage", "queueMicrotask", "requestAnimationFrame", "setInterval", "setTimeout", "caches",
		"__NEXT_DATA__", "__BUILD_MANIFEST", "__NEXT_PRELOADREADY",
	}
	documentKeys := []string{"_reactListeningo743lnnpvdg", "location"}
	cores := []int{8, 16, 24, 32}
	now := time.Now().In(time.FixedZone("EST", -5*3600)).Format("Mon Jan 02 2006 15:04:05") + " GMT-0500 (Eastern Standard Time)"
	perfNow := float64(time.Now().UnixNano()) / 1e6
	return []any{
		randomChoiceInt([]int{3000, 4000, 5000}),
		now,
		4294705152,
		0,
		userAgent,
		randomChoice(scriptSources),
		dataBuild,
		"en-US",
		"en-US,es-US,en,es",
		0,
		randomChoice(navigatorKeys),
		randomChoice(documentKeys),
		randomChoice(windowKeys),
		perfNow,
		newUUID(),
		"",
		randomChoiceInt(cores),
		float64(time.Now().UnixNano())/1e6 - perfNow,
	}
}

func powGenerate(seed, difficulty string, config []any, limit int) (string, bool) {
	target, err := hex.DecodeString(difficulty)
	if err != nil {
		target = []byte{0x0f, 0xff, 0xff}
	}
	diffLen := len(difficulty) / 2
	seedBytes := []byte(seed)
	part1 := mustMarshal(config[:3])
	part1 = append(part1[:len(part1)-1], ',')
	part2 := mustMarshal(config[4:9])
	part2 = append([]byte(","), part2[1:len(part2)-1]...)
	part2 = append(part2, ',')
	part3 := mustMarshal(config[10:])
	part3 = append([]byte(","), part3[1:]...)
	for i := 0; i < limit; i++ {
		finalJSON := bytes.Join([][]byte{
			part1,
			[]byte(fmt.Sprint(i)),
			part2,
			[]byte(fmt.Sprint(i >> 1)),
			part3,
		}, nil)
		encoded := []byte(base64.StdEncoding.EncodeToString(finalJSON))
		hash := sha3.Sum512(append(seedBytes, encoded...))
		if bytes.Compare(hash[:diffLen], target) <= 0 {
			return string(encoded), true
		}
	}
	return "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D" + base64.StdEncoding.EncodeToString([]byte(`"`+seed+`"`)), false
}

func mustMarshal(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

func randomChoice(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[rand.Intn(len(items))]
}

func randomChoiceInt(items []int) int {
	if len(items) == 0 {
		return 0
	}
	return items[rand.Intn(len(items))]
}
