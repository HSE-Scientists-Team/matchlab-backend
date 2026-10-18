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
	base := strings.ReplaceAll(string(body), "\r\n", "\n")
	for _, ending := range []struct{ name, value string }{{"LF", "\n"}, {"CRLF", "\r\n"}} {
		for _, tc := range []struct{ name, old, replacement string }{
			{"invalid_grpc_port", "port: 8086", "port: 0"},
			{"invalid_media_port", "port: 8085", "port: 70000"},
			{"password_in_yaml", "postgres:\n", "postgres:\n  password: secret\n"},
		} {
			t.Run(ending.name+"/"+tc.name, func(t *testing.T) {
				modified := strings.Replace(base, tc.old, tc.replacement, 1)
				if modified == base {
					t.Fatal("test fixture replacement did not match the example configuration")
				}
				modified = strings.ReplaceAll(modified, "\n", ending.value)
				path := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(path, []byte(modified), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := Load(path); err == nil {
					t.Fatal("invalid config accepted")
				}
			})
		}
	}
}
