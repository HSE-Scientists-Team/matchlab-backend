package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurations(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "test-password")
	for _, name := range []string{"config.example.yaml", "config.compose.yaml"} {
		cfg, err := Load(filepath.Join("..", "..", "..", "cmd", "support", name))
		if err != nil || cfg.GRPC.Port != 8086 || cfg.Media.Port != 8085 {
			t.Fatalf("%s: %+v %v", name, cfg, err)
		}
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "cmd", "support", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{strings.Replace(string(body), "port: 8086", "port: 0", 1), strings.Replace(string(body), "port: 8085", "port: 70000", 1), string(body) + "  password: secret\n"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
