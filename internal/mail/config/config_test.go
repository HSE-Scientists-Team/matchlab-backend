package config

import (
	"os"
	"path/filepath"
	"testing"
)

const testConfig = "grpc:\n  host: 0.0.0.0\n  port: 8083\nsmtp:\n  host: smtp.example.org\n  port: 587\n  from: no-reply@example.org\n  verification_url: https://example.org/verify\n  require_starttls: true\n"

func TestLoadSMTPSettingsAndOptionalCredentials(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "postgres-test")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(testConfig+"postgres:\n  host: localhost\n  port: 5432\n  database: matchlab\n  user: matchlab\n  ssl_mode: disable\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTP.Host != "smtp.example.org" || cfg.SMTP.Username != "" || cfg.SMTP.Password != "" {
		t.Fatalf("неожиданные настройки SMTP: %#v", cfg.SMTP)
	}
	t.Setenv("SMTP_USERNAME", "sender")
	t.Setenv("SMTP_PASSWORD", "smtp-secret")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTP.Username != "sender" || cfg.SMTP.Password != "smtp-secret" {
		t.Fatal("учётные данные SMTP не загружены")
	}
}

func TestLoadRejectsSMTPCredentialsInYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(testConfig+"  password: secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("пароль SMTP был принят из YAML")
	}
}
