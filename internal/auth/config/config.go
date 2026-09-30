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

type Redis struct {
	Endpoint `mapstructure:",squash"`
	DB       int    `mapstructure:"db"`
	Password string `mapstructure:"-"`
}

type Config struct {
	GRPC  Endpoint `mapstructure:"grpc"`
	Redis Redis    `mapstructure:"redis"`
}

func Load(path string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	var cfg Config
	if err := v.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("чтение конфигурации %q: %w", path, err)
	}
	if v.InConfig("redis.password") {
		return cfg, fmt.Errorf("redis.password нужно передавать через REDIS_PASSWORD, а не через YAML")
	}
	if err := v.BindEnv("redis.password", "REDIS_PASSWORD"); err != nil {
		return cfg, fmt.Errorf("привязка REDIS_PASSWORD: %w", err)
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("разбор конфигурации %q: %w", path, err)
	}
	cfg.Redis.Password = v.GetString("redis.password")
	if strings.TrimSpace(cfg.GRPC.Host) == "" || cfg.GRPC.Port < 1 || cfg.GRPC.Port > 65535 {
		return Config{}, fmt.Errorf("требуются grpc.host и grpc.port (1–65535)")
	}
	if strings.TrimSpace(cfg.Redis.Host) == "" || cfg.Redis.Port < 1 || cfg.Redis.Port > 65535 || cfg.Redis.DB < 0 {
		return Config{}, fmt.Errorf("требуются redis.host, redis.port (1–65535) и неотрицательный redis.db")
	}
	if cfg.Redis.Password == "" {
		return Config{}, fmt.Errorf("требуется переменная окружения REDIS_PASSWORD")
	}
	return cfg, nil
}
