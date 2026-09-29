package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTakesRedisPasswordFromEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("grpc:\n  host: 0.0.0.0\n  port: 8081\nredis:\n  host: localhost\n  port: 6379\n  db: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REDIS_PASSWORD", "secret")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPC.Address() != "0.0.0.0:8081" || cfg.Redis.Address() != "localhost:6379" || cfg.Redis.DB != 2 || cfg.Redis.Password != "secret" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestLoadRejectsPasswordInYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("grpc:\n  host: 0.0.0.0\n  port: 8081\nredis:\n  host: localhost\n  port: 6379\n  password: secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REDIS_PASSWORD", "secret")
	if _, err := Load(path); err == nil {
		t.Fatal("accepted redis password from YAML")
	}
}
