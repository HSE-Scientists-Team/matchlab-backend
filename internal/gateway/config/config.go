package config

import (
	"fmt"
	"net"
	"net/url"
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
	DB   postgres.Config `mapstructure:"postgres"`
	HTTP Endpoint        `mapstructure:"http"`
	Auth Endpoint        `mapstructure:"auth"`
	User Endpoint        `mapstructure:"user"`
	CORS struct {
		AllowedOrigins []string `mapstructure:"allowed_origins"`
	} `mapstructure:"cors"`
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
	for _, origin := range cfg.CORS.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return Config{}, fmt.Errorf("cors.allowed_origins должен содержать только HTTP(S)-адреса источников без пути: %q", origin)
		}
	}
	var err error
	cfg.DB, err = postgres.Load(v)
	if err != nil {
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
