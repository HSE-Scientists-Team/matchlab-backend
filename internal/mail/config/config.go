package config

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/smtp"
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

type SMTP struct {
	Host            string `mapstructure:"host"`
	Port            int    `mapstructure:"port"`
	Username        string `mapstructure:"-"`
	Password        string `mapstructure:"-"`
	From            string `mapstructure:"from"`
	VerificationURL string `mapstructure:"verification_url"`
	RequireStartTLS bool   `mapstructure:"require_starttls"`
}

type Worker struct {
	PollInterval time.Duration `mapstructure:"poll_interval"`
	Lease        time.Duration `mapstructure:"lease"`
	MaxAttempts  int           `mapstructure:"max_attempts"`
	Retention    time.Duration `mapstructure:"retention"`
}

type Config struct {
	GRPC      Endpoint `mapstructure:"grpc"`
	DB        Postgres `mapstructure:"postgres"`
	SMTP      SMTP     `mapstructure:"smtp"`
	Worker    Worker   `mapstructure:"worker"`
	CipherKey []byte   `mapstructure:"-"`
}

func Load(path string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	var cfg Config
	if err := v.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("read config %q: %w", path, err)
	}
	for _, key := range []string{"postgres.password", "smtp.username", "smtp.password", "encryption_key"} {
		if v.InConfig(key) {
			return cfg, fmt.Errorf("%s must be supplied through environment variables", key)
		}
	}
	bindings := map[string]string{
		"postgres.password": "POSTGRES_PASSWORD",
		"smtp.username":     "SMTP_USERNAME",
		"smtp.password":     "SMTP_PASSWORD",
		"encryption_key":    "MAIL_ENCRYPTION_KEY",
	}
	for key, env := range bindings {
		if err := v.BindEnv(key, env); err != nil {
			return cfg, fmt.Errorf("bind %s: %w", env, err)
		}
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("parse config %q: %w", path, err)
	}
	cfg.DB.Password = v.GetString("postgres.password")
	cfg.SMTP.Username = v.GetString("smtp.username")
	cfg.SMTP.Password = v.GetString("smtp.password")
	key, err := hex.DecodeString(v.GetString("encryption_key"))
	if err != nil || len(key) != 32 {
		return Config{}, fmt.Errorf("MAIL_ENCRYPTION_KEY must be a 64-character hex-encoded 32-byte key")
	}
	cfg.CipherKey = key
	if strings.TrimSpace(cfg.GRPC.Host) == "" || cfg.GRPC.Port < 1 || cfg.GRPC.Port > 65535 {
		return Config{}, fmt.Errorf("grpc.host and grpc.port (1-65535) are required")
	}
	if strings.TrimSpace(cfg.DB.Host) == "" || cfg.DB.Port < 1 || cfg.DB.Port > 65535 || strings.TrimSpace(cfg.DB.Database) == "" || strings.TrimSpace(cfg.DB.User) == "" || cfg.DB.Password == "" {
		return Config{}, fmt.Errorf("postgres.host, port, database, user, and POSTGRES_PASSWORD are required")
	}
	if strings.TrimSpace(cfg.SMTP.Host) == "" || cfg.SMTP.Port < 1 || cfg.SMTP.Port > 65535 || strings.TrimSpace(cfg.SMTP.From) == "" || strings.TrimSpace(cfg.SMTP.VerificationURL) == "" {
		return Config{}, fmt.Errorf("smtp.host, port, from, and verification_url are required")
	}
	if _, err := mail.ParseAddress(cfg.SMTP.From); err != nil {
		return Config{}, fmt.Errorf("smtp.from must be a valid email address")
	}
	verificationURL, err := url.Parse(cfg.SMTP.VerificationURL)
	if err != nil || (verificationURL.Scheme != "https" && verificationURL.Scheme != "http") || verificationURL.Host == "" {
		return Config{}, fmt.Errorf("smtp.verification_url must be an absolute HTTP or HTTPS URL")
	}
	if cfg.SMTP.Username != "" && cfg.SMTP.Password == "" || cfg.SMTP.Username == "" && cfg.SMTP.Password != "" {
		return Config{}, fmt.Errorf("SMTP_USERNAME and SMTP_PASSWORD must either both be set or both be empty")
	}
	if !cfg.SMTP.RequireStartTLS && (cfg.SMTP.Username != "" || cfg.SMTP.Password != "") {
		return Config{}, fmt.Errorf("SMTP authentication requires smtp.require_starttls=true")
	}
	if cfg.DB.SSLMode == "" {
		cfg.DB.SSLMode = "disable"
	}
	if cfg.Worker.PollInterval <= 0 {
		cfg.Worker.PollInterval = 2 * time.Second
	}
	if cfg.Worker.Lease <= 0 {
		cfg.Worker.Lease = 60 * time.Second
	}
	if cfg.Worker.MaxAttempts <= 0 {
		cfg.Worker.MaxAttempts = 8
	}
	if cfg.Worker.Retention <= 0 {
		cfg.Worker.Retention = 7 * 24 * time.Hour
	}
	return cfg, nil
}

func (s SMTP) SenderConfig() smtp.Config {
	return smtp.Config{Host: s.Host, Port: s.Port, Username: s.Username, Password: s.Password, From: s.From, VerificationURL: s.VerificationURL, RequireStartTLS: s.RequireStartTLS}
}
