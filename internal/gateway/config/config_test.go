package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "http:\n  host: 127.0.0.1\n  port: 8080\nauth:\n  host: localhost\n  port: 8081\nuser:\n  host: localhost\n  port: 8082\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Address() != "127.0.0.1:8080" || cfg.Auth.Address() != "localhost:8081" || cfg.User.Address() != "localhost:8082" {
		t.Fatalf("неожиданные адреса: HTTP %q, Auth %q, User %q", cfg.HTTP.Address(), cfg.Auth.Address(), cfg.User.Address())
	}
}

func TestLoadRejectsMissingServiceAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("http:\n  host: 127.0.0.1\n  port: 8080\nauth:\n  host: localhost\n  port: 8081\nuser:\n  host: localhost\n  port: 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("ожидалась ошибка адреса User")
	}
}
