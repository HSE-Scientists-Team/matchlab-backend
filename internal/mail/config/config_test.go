package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadValidatesSecretsAndAppliesWorkerDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "grpc:\n  host: 0.0.0.0\n  port: 8083\npostgres:\n  host: postgres\n  port: 5432\n  database: matchlab\n  user: matchlab\nsmtp:\n  host: smtp.example.org\n  port: 587\n  from: no-reply@example.org\n  verification_url: https://example.org/verify\n  require_starttls: true\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POSTGRES_PASSWORD", "db-secret")
	t.Setenv("MAIL_ENCRYPTION_KEY", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	t.Setenv("SMTP_USERNAME", "sender")
	t.Setenv("SMTP_PASSWORD", "smtp-secret")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CipherKey) != 32 || cfg.DB.Password != "db-secret" || cfg.SMTP.Password != "smtp-secret" {
		t.Fatalf("секреты не загружены: %#v", cfg)
	}
	if cfg.Worker.PollInterval != 2*time.Second || cfg.Worker.Lease != time.Minute || cfg.Worker.MaxAttempts != 8 || cfg.Worker.Retention != 7*24*time.Hour {
		t.Fatalf("неожиданные настройки обработчика: %#v", cfg.Worker)
	}
}

func TestLoadRejectsEncryptionKeyFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "grpc:\n  host: 0.0.0.0\n  port: 8083\nencryption_key: secret\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("ключ шифрования был принят из YAML")
	}
}
