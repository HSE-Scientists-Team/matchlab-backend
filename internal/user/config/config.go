package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

type Endpoint struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

func (e Endpoint) Address() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

type Postgres struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Database string `mapstructure:"database"`
	User     string `mapstructure:"user"`
	SSLMode  string `mapstructure:"ssl_mode"`
	Password string `mapstructure:"-"`
}

func (p Postgres) URL() string {
	u := &url.URL{Scheme: "postgres", User: url.UserPassword(p.User, p.Password), Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), Path: p.Database}
	q := u.Query()
	q.Set("sslmode", p.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

type Config struct {
	GRPC Endpoint `mapstructure:"grpc"`
	Auth Endpoint `mapstructure:"auth"`
	Mail Endpoint `mapstructure:"mail"`
	DB   Postgres `mapstructure:"postgres"`
}

func Load(path string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	var cfg Config
	if err := v.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("read config %q: %w", path, err)
	}
	if v.InConfig("postgres.password") {
		return cfg, fmt.Errorf("postgres.password must be supplied through POSTGRES_PASSWORD, not YAML")
	}
	if err := v.BindEnv("postgres.password", "POSTGRES_PASSWORD"); err != nil {
		return cfg, fmt.Errorf("bind POSTGRES_PASSWORD: %w", err)
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("parse config %q: %w", path, err)
	}
	cfg.DB.Password = v.GetString("postgres.password")
	if strings.TrimSpace(cfg.GRPC.Host) == "" || cfg.GRPC.Port < 1 || cfg.GRPC.Port > 65535 {
		return Config{}, fmt.Errorf("grpc.host and grpc.port (1-65535) are required")
	}
	if strings.TrimSpace(cfg.Auth.Host) == "" || cfg.Auth.Port < 1 || cfg.Auth.Port > 65535 {
		return Config{}, fmt.Errorf("auth.host and auth.port (1-65535) are required")
	}
	if strings.TrimSpace(cfg.Mail.Host) == "" || cfg.Mail.Port < 1 || cfg.Mail.Port > 65535 {
		return Config{}, fmt.Errorf("mail.host and mail.port (1-65535) are required")
	}
	if strings.TrimSpace(cfg.DB.Host) == "" || cfg.DB.Port < 1 || cfg.DB.Port > 65535 || strings.TrimSpace(cfg.DB.Database) == "" || strings.TrimSpace(cfg.DB.User) == "" || cfg.DB.Password == "" {
		return Config{}, fmt.Errorf("postgres.host, port, database, user, and POSTGRES_PASSWORD are required")
	}
	if cfg.DB.SSLMode == "" {
		cfg.DB.SSLMode = "disable"
	}
	return cfg, nil
}
