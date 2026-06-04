package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Config struct {
	mu                            sync.Mutex
	ProjectRoot                   string
	ConfigFile                    string
	DataDir                       string
	VersionFile                   string
	AuthKey                       string
	RefreshAccountIntervalMinute  int
	ImageAccountConcurrency       int
	ImageRetentionDays            int
	ImagePollTimeoutSecs          int
	ImagePollInitialWaitSecs      int
	ImagePollIntervalSecs         int
	AutoRemoveInvalidAccounts     bool
	AutoRemoveRateLimitedAccounts bool
	BaseURL                       string
	Proxy                         string
	Version                       string
	Raw                           map[string]any
}

var defaultBackupInclude = map[string]bool{
	"config":             true,
	"register":           true,
	"cpa":                true,
	"sub2api":            true,
	"logs":               true,
	"image_tasks":        true,
	"accounts_snapshot":  true,
	"auth_keys_snapshot": true,
	"images":             false,
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
		ProjectRoot:                   projectRoot,
		ConfigFile:                    configFile,
		DataDir:                       dataDir,
		VersionFile:                   filepath.Join(projectRoot, "VERSION"),
		AuthKey:                       authKey,
		RefreshAccountIntervalMinute:  intValue(raw["refresh_account_interval_minute"], 60, 1),
		ImageAccountConcurrency:       intValue(raw["image_account_concurrency"], 3, 1),
		ImageRetentionDays:            intValue(raw["image_retention_days"], 30, 1),
		ImagePollTimeoutSecs:          intValue(raw["image_poll_timeout_secs"], 120, 1),
		ImagePollInitialWaitSecs:      intValue(raw["image_poll_initial_wait_secs"], 10, 0),
		ImagePollIntervalSecs:         intValue(raw["image_poll_interval_secs"], 10, 1),
		AutoRemoveInvalidAccounts:     boolValue(raw["auto_remove_invalid_accounts"], false),
		AutoRemoveRateLimitedAccounts: boolValue(raw["auto_remove_rate_limited_accounts"], false),
		BaseURL:                       strings.TrimRight(strings.TrimSpace(envOr("CHATGPT2API_BASE_URL", cleanString(raw["base_url"]))), "/"),
		Proxy:                         strings.TrimSpace(envOr("CHATGPT2API_PROXY", cleanString(raw["proxy"]))),
		Raw:                           raw,
	}
	cfg.Version = readVersion(cfg.VersionFile)
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	return cfg, nil
}

func (c *Config) PublicConfig() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.publicConfigLocked()
}

func (c *Config) Update(updates map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := copyMap(c.Raw)
	for key, value := range updates {
		if key == "auth-key" || key == "backup_state" {
			continue
		}
		next[key] = value
	}
	if rawBackup, ok := next["backup"]; ok {
		current := normalizeBackupSettings(c.Raw["backup"])
		next["backup"] = normalizeBackupSettingsWithPrevious(rawBackup, current)
	}
	if rawImgbed, ok := next["imgbed"]; ok {
		current := normalizeImgbedSettings(c.Raw["imgbed"])
		next["imgbed"] = normalizeImgbedSettingsWithPrevious(rawImgbed, current)
	}
	if err := writeJSONObject(c.ConfigFile, next); err != nil {
		return nil, err
	}
	c.Raw = next
	c.refreshDerivedLocked()
	return c.publicConfigLocked(), nil
}

func (c *Config) BackupSettings(maskSecrets bool) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	settings := normalizeBackupSettings(c.Raw["backup"])
	if maskSecrets {
		if cleanString(settings["secret_access_key"]) != "" {
			settings["secret_access_key"] = "********"
		}
		if cleanString(settings["passphrase"]) != "" {
			settings["passphrase"] = "********"
		}
	}
	return settings
}

func (c *Config) ImgbedSettings(maskSecrets bool) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	settings := normalizeImgbedSettings(c.Raw["imgbed"])
	if maskSecrets && cleanString(settings["api_token"]) != "" {
		settings["api_token"] = "********"
	}
	return settings
}

func (c *Config) StorageInfo() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{
		"backend": map[string]any{
			"type":     "json",
			"data_dir": c.DataDir,
		},
		"health": map[string]any{
			"ok": true,
		},
	}
}

func (c *Config) BackupState() map[string]any {
	return normalizeBackupState(readJSONObject(filepath.Join(c.DataDir, "backup_state.json")))
}

func (c *Config) SaveBackupState(state map[string]any) map[string]any {
	normalized := normalizeBackupState(state)
	_ = writeJSONObject(filepath.Join(c.DataDir, "backup_state.json"), normalized)
	return normalized
}

func (c *Config) ImagesDir() string {
	return ensureDir(filepath.Join(c.DataDir, "images"))
}

func (c *Config) ImageThumbnailsDir() string {
	return ensureDir(filepath.Join(c.DataDir, "image_thumbnails"))
}

func (c *Config) ImageHistoryDir() string {
	return ensureDir(filepath.Join(c.DataDir, "image_history"))
}

func (c *Config) ImageHistoryFile() string {
	return filepath.Join(c.DataDir, "image_history.json")
}

func (c *Config) publicConfigLocked() map[string]any {
	data := copyMap(c.Raw)
	data["refresh_account_interval_minute"] = c.RefreshAccountIntervalMinute
	data["image_retention_days"] = c.ImageRetentionDays
	data["image_poll_timeout_secs"] = c.ImagePollTimeoutSecs
	data["image_poll_initial_wait_secs"] = c.ImagePollInitialWaitSecs
	data["image_poll_interval_secs"] = c.ImagePollIntervalSecs
	data["image_account_concurrency"] = c.ImageAccountConcurrency
	data["proxy"] = c.Proxy
	data["base_url"] = c.BaseURL
	data["auto_remove_invalid_accounts"] = c.AutoRemoveInvalidAccounts
	data["auto_remove_rate_limited_accounts"] = c.AutoRemoveRateLimitedAccounts
	if _, ok := data["global_system_prompt"]; !ok {
		data["global_system_prompt"] = ""
	}
	if _, ok := data["sensitive_words"].([]any); !ok {
		data["sensitive_words"] = []any{}
	}
	if _, ok := data["ai_review"].(map[string]any); !ok {
		data["ai_review"] = map[string]any{}
	}
	data["backup"] = normalizeBackupSettingsWithMask(data["backup"])
	data["backup_state"] = normalizeBackupState(readJSONObject(filepath.Join(c.DataDir, "backup_state.json")))
	data["imgbed"] = normalizeImgbedSettingsWithMask(data["imgbed"])
	delete(data, "auth-key")
	return data
}

func (c *Config) refreshDerivedLocked() {
	c.RefreshAccountIntervalMinute = intValue(c.Raw["refresh_account_interval_minute"], 60, 1)
	c.ImageAccountConcurrency = intValue(c.Raw["image_account_concurrency"], 3, 1)
	c.ImageRetentionDays = intValue(c.Raw["image_retention_days"], 30, 1)
	c.ImagePollTimeoutSecs = intValue(c.Raw["image_poll_timeout_secs"], 120, 1)
	c.ImagePollInitialWaitSecs = intValue(c.Raw["image_poll_initial_wait_secs"], 10, 0)
	c.ImagePollIntervalSecs = intValue(c.Raw["image_poll_interval_secs"], 10, 1)
	c.AutoRemoveInvalidAccounts = boolValue(c.Raw["auto_remove_invalid_accounts"], false)
	c.AutoRemoveRateLimitedAccounts = boolValue(c.Raw["auto_remove_rate_limited_accounts"], false)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(envOr("CHATGPT2API_BASE_URL", cleanString(c.Raw["base_url"]))), "/")
	c.Proxy = strings.TrimSpace(envOr("CHATGPT2API_PROXY", cleanString(c.Raw["proxy"])))
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

func writeJSONObject(path string, value map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err == nil {
		return nil
	}
	return os.WriteFile(path, data, 0o644)
}

func ensureDir(path string) string {
	_ = os.MkdirAll(path, 0o755)
	return path
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

func boolValue(value any, fallback bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	case float64:
		return v != 0
	case int:
		return v != 0
	}
	return fallback
}

func copyMap(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func asMap(value any) map[string]any {
	if item, ok := value.(map[string]any); ok {
		return item
	}
	return map[string]any{}
}

func normalizeBackupInclude(value any) map[string]any {
	source := asMap(value)
	out := map[string]any{}
	for key, fallback := range defaultBackupInclude {
		out[key] = boolValue(source[key], fallback)
	}
	return out
}

func normalizeBackupSettings(value any) map[string]any {
	source := asMap(value)
	return map[string]any{
		"enabled":           boolValue(source["enabled"], false),
		"provider":          "cloudflare_r2",
		"account_id":        cleanString(source["account_id"]),
		"access_key_id":     cleanString(source["access_key_id"]),
		"secret_access_key": cleanString(source["secret_access_key"]),
		"bucket":            cleanString(source["bucket"]),
		"prefix":            firstNonEmpty(strings.Trim(cleanString(source["prefix"]), "/"), "backups"),
		"interval_minutes":  intValue(source["interval_minutes"], 360, 1),
		"rotation_keep":     intValue(source["rotation_keep"], 10, 0),
		"encrypt":           boolValue(source["encrypt"], false),
		"passphrase":        cleanString(source["passphrase"]),
		"include":           normalizeBackupInclude(source["include"]),
	}
}

func normalizeBackupSettingsWithPrevious(value any, previous map[string]any) map[string]any {
	out := normalizeBackupSettings(value)
	if cleanString(out["secret_access_key"]) == "********" {
		out["secret_access_key"] = cleanString(previous["secret_access_key"])
	}
	if cleanString(out["passphrase"]) == "********" {
		out["passphrase"] = cleanString(previous["passphrase"])
	}
	return out
}

func normalizeBackupSettingsWithMask(value any) map[string]any {
	out := normalizeBackupSettings(value)
	if cleanString(out["secret_access_key"]) != "" {
		out["secret_access_key"] = "********"
	}
	if cleanString(out["passphrase"]) != "" {
		out["passphrase"] = "********"
	}
	return out
}

func normalizeImgbedSettings(value any) map[string]any {
	source := asMap(value)
	return map[string]any{
		"enabled":           boolValue(source["enabled"], false),
		"base_url":          strings.TrimRight(cleanString(source["base_url"]), "/"),
		"api_token":         cleanString(source["api_token"]),
		"folder_prefix":     firstNonEmpty(strings.Trim(cleanString(source["folder_prefix"]), "/"), "chatgpt2api"),
		"timeout_seconds":   intValue(source["timeout_seconds"], 30, 1),
		"fallback_to_local": boolValue(source["fallback_to_local"], true),
	}
}

func normalizeImgbedSettingsWithPrevious(value any, previous map[string]any) map[string]any {
	out := normalizeImgbedSettings(value)
	if cleanString(out["api_token"]) == "" || cleanString(out["api_token"]) == "********" {
		out["api_token"] = cleanString(previous["api_token"])
	}
	return out
}

func normalizeImgbedSettingsWithMask(value any) map[string]any {
	out := normalizeImgbedSettings(value)
	if cleanString(out["api_token"]) != "" {
		out["api_token"] = "********"
	}
	return out
}

func normalizeBackupState(value any) map[string]any {
	source := asMap(value)
	return map[string]any{
		"running":          false,
		"last_started_at":  optionalString(source["last_started_at"]),
		"last_finished_at": optionalString(source["last_finished_at"]),
		"last_status":      firstNonEmpty(cleanString(source["last_status"]), "idle"),
		"last_error":       optionalString(source["last_error"]),
		"last_object_key":  optionalString(source["last_object_key"]),
	}
}

func optionalString(value any) any {
	if text := cleanString(value); text != "" {
		return text
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
