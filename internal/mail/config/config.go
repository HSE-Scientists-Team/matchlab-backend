package config

import (
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"strconv"
	"strings"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/smtp"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/postgres"
	"github.com/spf13/viper"
)

type Endpoint struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

func (e Endpoint) Address() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

type SMTP struct {
	Host            string `mapstructure:"host"`
	Port            int    `mapstructure:"port"`
	Username        string `mapstructure:"-"`
	Password        string `mapstructure:"-"`
	From            string `mapstructure:"from"`
	VerificationURL string `mapstructure:"verification_url"`
	RequireStartTLS bool   `mapstructure:"require_starttls"`
}

type Config struct {
	DB   postgres.Config `mapstructure:"postgres"`
	GRPC Endpoint        `mapstructure:"grpc"`
	SMTP SMTP            `mapstructure:"smtp"`
}

func Load(path string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	var cfg Config
	if err := v.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("чтение конфигурации %q: %w", path, err)
	}
	for _, key := range []string{"smtp.username", "smtp.password"} {
		if v.InConfig(key) {
			return cfg, fmt.Errorf("%s нужно передавать через переменные окружения", key)
		}
	}
	for key, env := range map[string]string{"smtp.username": "SMTP_USERNAME", "smtp.password": "SMTP_PASSWORD"} {
		if err := v.BindEnv(key, env); err != nil {
			return cfg, fmt.Errorf("привязка %s: %w", env, err)
		}
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("разбор конфигурации %q: %w", path, err)
	}
	cfg.SMTP.Username = v.GetString("smtp.username")
	cfg.SMTP.Password = v.GetString("smtp.password")
	if strings.TrimSpace(cfg.GRPC.Host) == "" || cfg.GRPC.Port < 1 || cfg.GRPC.Port > 65535 {
		return Config{}, fmt.Errorf("требуются grpc.host и grpc.port (1–65535)")
	}
	if strings.TrimSpace(cfg.SMTP.Host) == "" || cfg.SMTP.Port < 1 || cfg.SMTP.Port > 65535 || strings.TrimSpace(cfg.SMTP.From) == "" || strings.TrimSpace(cfg.SMTP.VerificationURL) == "" {
		return Config{}, fmt.Errorf("требуются smtp.host, smtp.port, smtp.from и smtp.verification_url")
	}
	if _, err := mail.ParseAddress(cfg.SMTP.From); err != nil {
		return Config{}, fmt.Errorf("smtp.from должен быть корректным адресом электронной почты")
	}
	verificationURL, err := url.Parse(cfg.SMTP.VerificationURL)
	if err != nil || (verificationURL.Scheme != "https" && verificationURL.Scheme != "http") || verificationURL.Host == "" {
		return Config{}, fmt.Errorf("smtp.verification_url должен быть абсолютным URL HTTP или HTTPS")
	}
	if cfg.SMTP.Username != "" && cfg.SMTP.Password == "" || cfg.SMTP.Username == "" && cfg.SMTP.Password != "" {
		return Config{}, fmt.Errorf("SMTP_USERNAME и SMTP_PASSWORD должны быть либо оба заданы, либо оба пусты")
	}
	if !cfg.SMTP.RequireStartTLS && cfg.SMTP.Username != "" {
		return Config{}, fmt.Errorf("для аутентификации SMTP требуется smtp.require_starttls=true")
	}
	cfg.DB, err = postgres.Load(v)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (s SMTP) SenderConfig() smtp.Config {
	return smtp.Config{Host: s.Host, Port: s.Port, Username: s.Username, Password: s.Password, From: s.From, VerificationURL: s.VerificationURL, RequireStartTLS: s.RequireStartTLS}
}
