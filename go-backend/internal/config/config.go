package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	ProjectRoot                  string
	ConfigFile                   string
	DataDir                      string
	VersionFile                  string
	AuthKey                      string
	RefreshAccountIntervalMinute int
	ImageAccountConcurrency      int
	ImageRetentionDays           int
	ImagePollTimeoutSecs         int
	ImagePollInitialWaitSecs     int
	ImagePollIntervalSecs        int
	BaseURL                      string
	Proxy                        string
	Version                      string
	Raw                          map[string]any
}

func Load() (*Config, error) {
	root, configFile, err := resolveProjectRootAndConfig()
	if err != nil {
		return nil, err
	}
	return LoadFrom(root, configFile)
}

func LoadFrom(projectRoot, configFile string) (*Config, error) {
	projectRoot = cleanAbs(projectRoot)
	if projectRoot == "" {
		return nil, errors.New("project root is required")
	}
	if strings.TrimSpace(configFile) == "" {
		configFile = filepath.Join(projectRoot, "config.json")
	}
	configFile = resolvePath(projectRoot, configFile)
	raw := readJSONObject(configFile)
	dataDir := resolvePath(projectRoot, envOr("CHATGPT2API_DATA_DIR", filepath.Join(projectRoot, "data")))
	authKey := strings.TrimSpace(envOr("CHATGPT2API_AUTH_KEY", cleanString(raw["auth-key"])))
	if invalidAuthKey(authKey) {
		return nil, errors.New("auth-key 未设置，请设置 CHATGPT2API_AUTH_KEY 或 config.json 中的 auth-key")
	}
	cfg := &Config{
		ProjectRoot:                  projectRoot,
		ConfigFile:                   configFile,
		DataDir:                      dataDir,
		VersionFile:                  filepath.Join(projectRoot, "VERSION"),
		AuthKey:                      authKey,
		RefreshAccountIntervalMinute: intValue(raw["refresh_account_interval_minute"], 60, 1),
		ImageAccountConcurrency:      intValue(raw["image_account_concurrency"], 3, 1),
		ImageRetentionDays:           intValue(raw["image_retention_days"], 30, 1),
		ImagePollTimeoutSecs:         intValue(raw["image_poll_timeout_secs"], 120, 1),
		ImagePollInitialWaitSecs:     intValue(raw["image_poll_initial_wait_secs"], 10, 0),
		ImagePollIntervalSecs:        intValue(raw["image_poll_interval_secs"], 10, 1),
		BaseURL:                      strings.TrimRight(strings.TrimSpace(envOr("CHATGPT2API_BASE_URL", cleanString(raw["base_url"]))), "/"),
		Proxy:                        strings.TrimSpace(envOr("CHATGPT2API_PROXY", cleanString(raw["proxy"]))),
		Raw:                          raw,
	}
	cfg.Version = readVersion(cfg.VersionFile)
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	return cfg, nil
}

func resolveProjectRootAndConfig() (string, string, error) {
	if raw := strings.TrimSpace(os.Getenv("CHATGPT2API_CONFIG_FILE")); raw != "" {
		configFile := cleanAbs(raw)
		return filepath.Dir(configFile), configFile, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	for dir := cleanAbs(wd); ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "config.json")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return dir, candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	root := cleanAbs(wd)
	return root, filepath.Join(root, "config.json"), nil
}

func resolvePath(root, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if filepath.IsAbs(raw) {
		return cleanAbs(raw)
	}
	return cleanAbs(filepath.Join(root, raw))
}

func cleanAbs(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func invalidAuthKey(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "your_real_auth_key":
		return true
	default:
		return false
	}
}

func readJSONObject(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func readVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "0.0.0"
	}
	if value := strings.TrimSpace(string(data)); value != "" {
		return value
	}
	return "0.0.0"
}

func cleanString(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func intValue(value any, fallback, minimum int) int {
	var n int
	switch v := value.(type) {
	case int:
		n = v
	case int64:
		n = int(v)
	case float64:
		n = int(v)
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			n = fallback
		} else {
			n = int(parsed)
		}
	case string:
		_, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
		if err != nil {
			n = fallback
		}
	default:
		n = fallback
	}
	if n < minimum {
		return minimum
	}
	return n
}
