package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type JSONStore struct {
	AccountsPath string
	AuthKeysPath string
}

func NewJSONStore(dataDir string) *JSONStore {
	return &JSONStore{
		AccountsPath: filepath.Join(dataDir, "accounts.json"),
		AuthKeysPath: filepath.Join(dataDir, "auth_keys.json"),
	}
}

func (s *JSONStore) LoadAccounts() ([]map[string]any, error) {
	return loadJSONList(s.AccountsPath)
}

func (s *JSONStore) SaveAccounts(accounts []map[string]any) error {
	return saveJSONValue(s.AccountsPath, accounts)
}

func (s *JSONStore) LoadAuthKeys() ([]map[string]any, error) {
	data, err := os.ReadFile(s.AuthKeysPath)
	if os.IsNotExist(err) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return []map[string]any{}, nil
	}
	if object, ok := raw.(map[string]any); ok {
		raw = object["items"]
	}
	return normalizeList(raw), nil
}

func (s *JSONStore) SaveAuthKeys(keys []map[string]any) error {
	return saveJSONValue(s.AuthKeysPath, map[string]any{"items": keys})
}

func loadJSONList(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return []map[string]any{}, nil
	}
	return normalizeList(raw), nil
}

func normalizeList(raw any) []map[string]any {
	items, ok := raw.([]any)
	if !ok {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		copied := make(map[string]any, len(object))
		for key, value := range object {
			copied[key] = value
		}
		out = append(out, copied)
	}
	return out
}

func saveJSONValue(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.json")
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
	// Windows 上目标文件存在时 Rename 可能失败，退回到直接写入，避免首版引入额外依赖。
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
