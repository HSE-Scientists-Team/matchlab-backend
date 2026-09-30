package config

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndBuildsPostgresURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "grpc:\n  host: 0.0.0.0\n  port: 8082\nauth:\n  host: auth\n  port: 8081\nmail:\n  host: mail\n  port: 8083\npostgres:\n  host: postgres\n  port: 5432\n  database: matchlab\n  user: matchlab\n  ssl_mode: disable\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POSTGRES_PASSWORD", "пароль с пробелами")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPC.Address() != "0.0.0.0:8082" || cfg.Auth.Address() != "auth:8081" || cfg.Mail.Address() != "mail:8083" {
		t.Fatalf("неожиданные адреса: %#v", cfg)
	}
	u, err := url.Parse(cfg.DB.URL())
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if password != "пароль с пробелами" || u.Host != "postgres:5432" || u.Path != "/matchlab" || u.Query().Get("sslmode") != "disable" {
		t.Fatalf("неожиданный URL PostgreSQL: %s", cfg.DB.URL())
	}
}

func TestLoadRejectsPasswordInYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "grpc:\n  host: 0.0.0.0\n  port: 8082\nauth:\n  host: auth\n  port: 8081\nmail:\n  host: mail\n  port: 8083\npostgres:\n  host: postgres\n  port: 5432\n  database: matchlab\n  user: matchlab\n  password: secret\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POSTGRES_PASSWORD", "secret")
	if _, err := Load(path); err == nil {
		t.Fatal("пароль PostgreSQL был принят из YAML")
	}
}
