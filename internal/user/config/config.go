package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/postgres"
	"github.com/spf13/viper"
)

type Endpoint struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

func (e Endpoint) Address() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

type Config struct {
	GRPC Endpoint        `mapstructure:"grpc"`
	Auth Endpoint        `mapstructure:"auth"`
	Mail Endpoint        `mapstructure:"mail"`
	DB   postgres.Config `mapstructure:"postgres"`
}

func Load(path string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	var cfg Config
	if err := v.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("чтение конфигурации %q: %w", path, err)
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("разбор конфигурации %q: %w", path, err)
	}
	var err error
	cfg.DB, err = postgres.Load(v)
	if err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(cfg.GRPC.Host) == "" || cfg.GRPC.Port < 1 || cfg.GRPC.Port > 65535 {
		return Config{}, fmt.Errorf("требуются grpc.host и grpc.port (1–65535)")
	}
	if strings.TrimSpace(cfg.Auth.Host) == "" || cfg.Auth.Port < 1 || cfg.Auth.Port > 65535 {
		return Config{}, fmt.Errorf("требуются auth.host и auth.port (1–65535)")
	}
	if strings.TrimSpace(cfg.Mail.Host) == "" || cfg.Mail.Port < 1 || cfg.Mail.Port > 65535 {
		return Config{}, fmt.Errorf("требуются mail.host и mail.port (1–65535)")
	}
	return cfg, nil
}
