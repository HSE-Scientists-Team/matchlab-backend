package config

import (
	"fmt"
	"mime"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

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

type S3 struct {
	Endpoint       string        `mapstructure:"endpoint"`
	PublicEndpoint string        `mapstructure:"public_endpoint"`
	Region         string        `mapstructure:"region"`
	Bucket         string        `mapstructure:"bucket"`
	UsePathStyle   bool          `mapstructure:"use_path_style"`
	RequestTimeout time.Duration `mapstructure:"request_timeout"`
	AccessKey      string        `mapstructure:"-"`
	SecretKey      string        `mapstructure:"-"`
}

type Config struct {
	GRPC   Endpoint `mapstructure:"grpc"`
	DB     Postgres `mapstructure:"postgres"`
	S3     S3       `mapstructure:"s3"`
	Upload struct {
		URLTTL              time.Duration `mapstructure:"url_ttl"`
		MaxSizeBytes        int64         `mapstructure:"max_size_bytes"`
		AllowedContentTypes []string      `mapstructure:"allowed_content_types"`
	} `mapstructure:"upload"`
	Download struct {
		URLTTL time.Duration `mapstructure:"url_ttl"`
	} `mapstructure:"download"`
}

func Load(path string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	var cfg Config
	if err := v.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("чтение конфигурации: %w", err)
	}
	for key, env := range map[string]string{"postgres.password": "POSTGRES_PASSWORD", "s3.access_key": "S3_MEDIA_ACCESS_KEY", "s3.secret_key": "S3_MEDIA_SECRET_KEY"} {
		if v.InConfig(key) {
			return Config{}, fmt.Errorf("%s нужно передавать через %s, а не YAML", key, env)
		}
		if err := v.BindEnv(key, env); err != nil {
			return Config{}, fmt.Errorf("привязка %s: %w", env, err)
		}
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("разбор конфигурации: %w", err)
	}
	cfg.DB.Password = v.GetString("postgres.password")
	cfg.S3.AccessKey = v.GetString("s3.access_key")
	cfg.S3.SecretKey = v.GetString("s3.secret_key")
	if cfg.DB.SSLMode == "" {
		cfg.DB.SSLMode = "disable"
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.GRPC.Host) == "" || c.GRPC.Port < 1 || c.GRPC.Port > 65535 {
		return fmt.Errorf("требуются grpc.host и grpc.port (1–65535)")
	}
	if strings.TrimSpace(c.DB.Host) == "" || c.DB.Port < 1 || c.DB.Port > 65535 || strings.TrimSpace(c.DB.Database) == "" || strings.TrimSpace(c.DB.User) == "" || c.DB.Password == "" {
		return fmt.Errorf("требуются параметры PostgreSQL и POSTGRES_PASSWORD")
	}
	if err := c.S3.Validate(); err != nil {
		return err
	}
	if c.Upload.MaxSizeBytes <= 0 || !validTTL(c.Upload.URLTTL) || !validTTL(c.Download.URLTTL) {
		return fmt.Errorf("требуются положительный upload.max_size_bytes и TTL в целых секундах от 1 секунды до 7 дней")
	}
	if len(c.Upload.AllowedContentTypes) == 0 {
		return fmt.Errorf("требуется upload.allowed_content_types")
	}
	for _, value := range c.Upload.AllowedContentTypes {
		typ, params, err := mime.ParseMediaType(value)
		if err != nil || len(params) != 0 || strings.Contains(typ, "*") || strings.Count(typ, "/") != 1 {
			return fmt.Errorf("некорректный разрешённый Content-Type: %q", value)
		}
	}
	return nil
}

func validTTL(ttl time.Duration) bool {
	return ttl >= time.Second && ttl <= 7*24*time.Hour && ttl%time.Second == 0
}

func (c S3) Validate() error {
	for name, value := range map[string]string{"s3.endpoint": c.Endpoint, "s3.public_endpoint": c.PublicEndpoint} {
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%s должен быть HTTP(S)-адресом без пути, учётных данных и query", name)
		}
	}
	if strings.TrimSpace(c.Region) == "" || strings.TrimSpace(c.Bucket) == "" || c.AccessKey == "" || c.SecretKey == "" || c.RequestTimeout <= 0 {
		return fmt.Errorf("требуются s3.region, s3.bucket, положительный s3.request_timeout, S3_MEDIA_ACCESS_KEY и S3_MEDIA_SECRET_KEY")
	}
	return nil
}
