package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

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
	query := u.Query()
	query.Set("sslmode", p.SSLMode)
	u.RawQuery = query.Encode()
	return u.String()
}

func Load(path string) (Postgres, error) {
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return Postgres{}, fmt.Errorf("чтение конфигурации %q: %w", path, err)
	}
	if v.InConfig("postgres.password") {
		return Postgres{}, fmt.Errorf("postgres.password нужно передавать через POSTGRES_PASSWORD, а не через YAML")
	}
	if err := v.BindEnv("postgres.password", "POSTGRES_PASSWORD"); err != nil {
		return Postgres{}, fmt.Errorf("привязка POSTGRES_PASSWORD: %w", err)
	}
	var cfg struct {
		Postgres Postgres `mapstructure:"postgres"`
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return Postgres{}, fmt.Errorf("разбор конфигурации %q: %w", path, err)
	}
	p := cfg.Postgres
	p.Password = v.GetString("postgres.password")
	if strings.TrimSpace(p.Host) == "" || p.Port < 1 || p.Port > 65535 || strings.TrimSpace(p.Database) == "" || strings.TrimSpace(p.User) == "" || p.Password == "" {
		return Postgres{}, fmt.Errorf("требуются postgres.host, postgres.port, postgres.database, postgres.user и POSTGRES_PASSWORD")
	}
	if p.SSLMode == "" {
		p.SSLMode = "disable"
	}
	return p, nil
}
