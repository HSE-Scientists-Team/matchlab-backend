package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("POSTGRES_PASSWORD", "test-db-password")
	t.Setenv("S3_MEDIA_ACCESS_KEY", "test-access")
	t.Setenv("S3_MEDIA_SECRET_KEY", "test-secret")
}

func TestExampleConfigs(t *testing.T) {
	setSecrets(t)
	for _, name := range []string{"config.example.yaml", "config.compose.yaml"} {
		cfg, err := Load(filepath.Join("..", "..", "..", "cmd", "media", name))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.S3.AccessKey != "test-access" || cfg.S3.SecretKey != "test-secret" || cfg.DB.Password != "test-db-password" || cfg.S3.RequestTimeout != 5*time.Second || cfg.Upload.URLTTL != 10*time.Minute {
			t.Fatalf("unexpected config: %s", name)
		}
	}
}

func TestSecretsRequiredAndForbiddenInYAML(t *testing.T) {
	setSecrets(t)
	path := filepath.Join("..", "..", "..", "cmd", "media", "config.example.yaml")
	t.Run("missing secret", func(t *testing.T) {
		t.Setenv("S3_MEDIA_SECRET_KEY", "")
		if _, err := Load(path); err == nil {
			t.Fatal("missing secret accepted")
		}
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ section, key string }{{"postgres", "password"}, {"s3", "access_key"}, {"s3", "secret_key"}} {
		t.Run(tc.key, func(t *testing.T) {
			bad := strings.Replace(string(data), tc.section+":", tc.section+":\n  "+tc.key+": forbidden-test-value", 1)
			file := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(file, []byte(bad), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(file); err == nil {
				t.Fatal("YAML secret accepted")
			}
		})
	}
}

func TestInvalidSettings(t *testing.T) {
	setSecrets(t)
	cfg, err := Load(filepath.Join("..", "..", "..", "cmd", "media", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"endpoint", func(c *Config) { c.S3.Endpoint = "ftp://storage" }},
		{"public credentials", func(c *Config) { c.S3.PublicEndpoint = "http://user:password@storage" }},
		{"timeout", func(c *Config) { c.S3.RequestTimeout = 0 }},
		{"ttl", func(c *Config) { c.Upload.URLTTL = 8 * 24 * time.Hour }},
		{"size", func(c *Config) { c.Upload.MaxSizeBytes = 0 }},
		{"small part", func(c *Config) { c.Multipart.PartSizeBytes = 5242879 }},
		{"large part", func(c *Config) { c.Multipart.PartSizeBytes = 5368709121 }},
		{"multipart ttl", func(c *Config) { c.Multipart.SessionTTL = 0 }},
		{"cleanup interval", func(c *Config) { c.Multipart.CleanupInterval = 0 }},
		{"too many parts", func(c *Config) { c.Upload.MaxSizeBytes = c.Multipart.PartSizeBytes*10000 + 1 }},
		{"types", func(c *Config) { c.Upload.AllowedContentTypes = []string{"image/*"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := cfg
			tc.mutate(&bad)
			if err := bad.Validate(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
