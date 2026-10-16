package grpcapp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigRequiresDeclaredPeersAndEnvPassword(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "test-only")
	p := filepath.Join(t.TempDir(), "config.yaml")
	body := "grpc: 0.0.0.0:8087\npeers:\n  user: user:8082\npostgres:\n  host: postgres\n  port: 5432\n  database: matchlab\n  user: matchlab\n  ssl_mode: disable\n"
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, []string{"user"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, []string{"search"}); err == nil {
		t.Fatal("missing peer accepted")
	}
	t.Setenv("POSTGRES_PASSWORD", "")
	if _, err := Load(p, []string{"user"}); err == nil {
		t.Fatal("missing secret accepted")
	}
}
