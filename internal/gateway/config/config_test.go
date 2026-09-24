package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("http:\n  host: 127.0.0.1\n  port: 8080\nredis:\n  host: localhost\n  port: 6379\n  db: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REDIS_PASSWORD", "test-secret")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Address() != "127.0.0.1:8080" || cfg.Redis.Address() != "localhost:6379" || cfg.Redis.DB != 2 || cfg.Redis.Password != "test-secret" {
		t.Fatalf("unexpected config: HTTP %q, Redis %q, DB %d", cfg.HTTP.Address(), cfg.Redis.Address(), cfg.Redis.DB)
	}
	t.Setenv("REDIS_PASSWORD", "")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "REDIS_PASSWORD") {
		t.Fatalf("missing password: %v", err)
	}
	if err := os.WriteFile(path, []byte("http:\n  host: 127.0.0.1\n  port: 8080\nredis:\n  host: localhost\n  port: 6379\n  password: yaml-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "not YAML") {
		t.Fatalf("YAML password accepted: %v", err)
	}
}
