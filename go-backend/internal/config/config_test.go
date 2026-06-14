package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromReadsProjectConfig(t *testing.T) {
	t.Setenv("CHATGPT2API_AUTH_KEY", "")
	t.Setenv("CHATGPT2API_DATA_DIR", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"admin-key","image_account_concurrency":5,"image_redundancy_multiplier":2.5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(root, "")
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.AuthKey != "admin-key" {
		t.Fatalf("AuthKey = %q", cfg.AuthKey)
	}
	if cfg.ImageAccountConcurrency != 5 {
		t.Fatalf("ImageAccountConcurrency = %d", cfg.ImageAccountConcurrency)
	}
	if cfg.ImageRedundancyMultiplier != 2.5 {
		t.Fatalf("ImageRedundancyMultiplier = %f", cfg.ImageRedundancyMultiplier)
	}
	if cfg.PublicConfig()["image_redundancy_multiplier"] != 2.5 {
		t.Fatalf("public config = %#v", cfg.PublicConfig()["image_redundancy_multiplier"])
	}
	if cfg.Version != "1.2.3" {
		t.Fatalf("Version = %q", cfg.Version)
	}
	if _, err := os.Stat(cfg.DataDir); err != nil {
		t.Fatalf("DataDir not created: %v", err)
	}
}

func TestLoadFromRejectsMissingAuthKey(t *testing.T) {
	t.Setenv("CHATGPT2API_AUTH_KEY", "")
	t.Setenv("CHATGPT2API_DATA_DIR", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFrom(root, ""); err == nil {
		t.Fatal("LoadFrom() expected auth-key error")
	}
}
