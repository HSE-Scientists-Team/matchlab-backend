package config

import (
	"fmt"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/postgres"
	"github.com/spf13/viper"
	"net"
	"strconv"
	"strings"
)

type Endpoint struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

func (e Endpoint) Address() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

type Config struct {
	DB    postgres.Config `mapstructure:"postgres"`
	GRPC  Endpoint        `mapstructure:"grpc"`
	Media Endpoint        `mapstructure:"media"`
}

func Load(path string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	var cfg Config
	if err := v.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("чтение конфигурации: %w", err)
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("разбор конфигурации: %w", err)
	}
	for name, endpoint := range map[string]Endpoint{"grpc": cfg.GRPC, "media": cfg.Media} {
		if strings.TrimSpace(endpoint.Host) == "" || endpoint.Port < 1 || endpoint.Port > 65535 {
			return Config{}, fmt.Errorf("требуются %s.host и %s.port (1–65535)", name, name)
		}
	}
	var err error
	cfg.DB, err = postgres.Load(v)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}
