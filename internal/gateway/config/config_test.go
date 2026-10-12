package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "postgres-test")
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "http:\n  host: 127.0.0.1\n  port: 8080\nauth:\n  host: localhost\n  port: 8081\nuser:\n  host: localhost\n  port: 8082\nmedia:\n  host: localhost\n  port: 8085\nsupport:\n  host: localhost\n  port: 8086\n"
	if err := os.WriteFile(path, []byte(body+"postgres:\n  host: localhost\n  port: 5432\n  database: matchlab\n  user: matchlab\n  ssl_mode: disable\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Address() != "127.0.0.1:8080" || cfg.Auth.Address() != "localhost:8081" || cfg.User.Address() != "localhost:8082" || cfg.Media.Address() != "localhost:8085" {
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

func TestLoadRejectsMissingMediaAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "http:\n  host: localhost\n  port: 8080\nauth:\n  host: localhost\n  port: 8081\nuser:\n  host: localhost\n  port: 8082\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("missing Media endpoint accepted")
	}
}

func TestExampleConfigurations(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "postgres-test")
	for _, name := range []string{"config.example.yaml", "config.compose.yaml"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(filepath.Join("..", "..", "..", "cmd", "gateway", name)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoadRejectsMissingSupportAddress(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "postgres-test")
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "cmd", "gateway", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.Replace(strings.ReplaceAll(string(body), "\r\n", "\n"), "support:\n  host: localhost\n  port: 8086\n", "", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("missing Support endpoint accepted")
	}
}
