package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

type Endpoint struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

func (e Endpoint) Address() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

type Config struct {
	HTTP Endpoint `mapstructure:"http"`
	Auth Endpoint `mapstructure:"auth"`
	User Endpoint `mapstructure:"user"`
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
	if err := validateEndpoint("http", cfg.HTTP); err != nil {
		return Config{}, err
	}
	if err := validateEndpoint("auth", cfg.Auth); err != nil {
		return Config{}, err
	}
	if err := validateEndpoint("user", cfg.User); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateEndpoint(name string, endpoint Endpoint) error {
	if strings.TrimSpace(endpoint.Host) == "" || endpoint.Port < 1 || endpoint.Port > 65535 {
		return fmt.Errorf("требуются %s.host и %s.port (1–65535)", name, name)
	}
	return nil
}
