// Package postgres содержит общие настройки подключения и запуска миграций.
package postgres

import (
	"fmt"
	"github.com/spf13/viper"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Host             string        `mapstructure:"host"`
	Port             int           `mapstructure:"port"`
	Database         string        `mapstructure:"database"`
	User             string        `mapstructure:"user"`
	SSLMode          string        `mapstructure:"ssl_mode"`
	Password         string        `mapstructure:"-"`
	MigrationTimeout time.Duration `mapstructure:"migration_timeout"`
}

func (p Config) URL() string {
	u := &url.URL{Scheme: "postgres", User: url.UserPassword(p.User, p.Password), Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), Path: p.Database}
	q := u.Query()
	q.Set("sslmode", p.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

func Load(v *viper.Viper) (Config, error) {
	if v.InConfig("postgres.password") {
		return Config{}, fmt.Errorf("postgres.password нужно передавать через POSTGRES_PASSWORD, а не через YAML")
	}
	if err := v.BindEnv("postgres.password", "POSTGRES_PASSWORD"); err != nil {
		return Config{}, fmt.Errorf("привязка POSTGRES_PASSWORD: %w", err)
	}
	var cfg Config
	if err := v.UnmarshalKey("postgres", &cfg); err != nil {
		return Config{}, fmt.Errorf("разбор настроек PostgreSQL: %w", err)
	}
	cfg.Password = v.GetString("postgres.password")
	if strings.TrimSpace(cfg.Host) == "" || cfg.Port < 1 || cfg.Port > 65535 || strings.TrimSpace(cfg.Database) == "" || strings.TrimSpace(cfg.User) == "" || cfg.Password == "" {
		return Config{}, fmt.Errorf("требуются postgres.host, postgres.port, postgres.database, postgres.user и POSTGRES_PASSWORD")
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = "disable"
	}
	if !v.InConfig("postgres.migration_timeout") {
		cfg.MigrationTimeout = 5 * time.Minute
	}
	if cfg.MigrationTimeout <= 0 {
		return Config{}, fmt.Errorf("postgres.migration_timeout должен быть положительным")
	}
	return cfg, nil
}
