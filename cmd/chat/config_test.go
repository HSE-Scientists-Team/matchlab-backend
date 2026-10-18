package main

import (
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/grpcapp"
	"testing"
)

func TestExampleConfigurations(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "test-only")
	for _, path := range []string{"config.example.yaml", "config.compose.yaml"} {
		cfg, err := grpcapp.Load(path, []string{"user"})
		if err != nil || cfg.GRPC == "" || cfg.Peers["user"] == "" {
			t.Fatalf("%s: %+v %v", path, cfg, err)
		}
	}
}
