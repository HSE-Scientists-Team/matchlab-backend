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

func (e Endpoint) Address() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

type Redis struct {
	Endpoint `mapstructure:",squash"`
	DB       int    `mapstructure:"db"`
	Password string `mapstructure:"-"`
}

type Config struct {
	HTTP  Endpoint `mapstructure:"http"`
	Redis Redis    `mapstructure:"redis"`
}

func Load(path string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	var cfg Config
	if err := v.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("read config %q: %w", path, err)
	}
	if v.InConfig("redis.password") {
		return cfg, fmt.Errorf("redis.password must be supplied through REDIS_PASSWORD, not YAML")
	}
	if err := v.BindEnv("redis.password", "REDIS_PASSWORD"); err != nil {
		return cfg, fmt.Errorf("bind REDIS_PASSWORD: %w", err)
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("parse config %q: %w", path, err)
	}
	cfg.Redis.Password = v.GetString("redis.password")
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if strings.TrimSpace(c.HTTP.Host) == "" || c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		return fmt.Errorf("http.host and http.port (1-65535) are required")
	}
	if strings.TrimSpace(c.Redis.Host) == "" || c.Redis.Port < 1 || c.Redis.Port > 65535 || c.Redis.DB < 0 {
		return fmt.Errorf("redis.host, redis.port (1-65535), and nonnegative redis.db are required")
	}
	if c.Redis.Password == "" {
		return fmt.Errorf("REDIS_PASSWORD environment variable is required")
	}
	return nil
}
