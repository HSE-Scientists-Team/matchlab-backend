package postgres

import (
	"github.com/spf13/viper"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLoadValidatesSecretsAndMigrationTimeout(t *testing.T) {
	base := "postgres:\n  host: postgres\n  port: 5432\n  database: matchlab\n  user: matchlab\n"
	for _, tc := range []struct {
		name, extra, password string
		want                  time.Duration
		valid                 bool
	}{
		{"defaults", "", "password with spaces", 5 * time.Minute, true},
		{"custom timeout", "  migration_timeout: 12m\n", "secret", 12 * time.Minute, true},
		{"missing secret", "", "", 0, false},
		{"secret in yaml", "  password: secret\n", "secret", 0, false},
		{"zero timeout", "  migration_timeout: 0s\n", "secret", 0, false},
		{"negative timeout", "  migration_timeout: -1s\n", "secret", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("POSTGRES_PASSWORD", tc.password)
			v := viper.New()
			v.SetConfigType("yaml")
			if err := v.ReadConfig(strings.NewReader(base + tc.extra)); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(v)
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid configuration accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MigrationTimeout != tc.want || cfg.SSLMode != "disable" {
				t.Fatalf("unexpected configuration: %+v", cfg)
			}
			u, err := url.Parse(cfg.URL())
			if err != nil {
				t.Fatal(err)
			}
			password, _ := u.User.Password()
			if password != tc.password || u.Host != "postgres:5432" || u.Path != "/matchlab" {
				t.Fatal("invalid postgres URL")
			}
		})
	}
}
