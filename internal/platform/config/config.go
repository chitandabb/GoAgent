package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/joho/godotenv"
)

type Config struct {
	HTTP      HTTPConfig      `toml:"http"`
	Postgres  PostgresConfig  `toml:"postgres"`
	Redis     RedisConfig     `toml:"redis"`
	SQLServer SQLServerConfig `toml:"sqlserver"`
	LLM       LLMConfig       `toml:"llm"`
}

type HTTPConfig struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

func (c HTTPConfig) Address() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}

type PostgresConfig struct {
	Host        string `toml:"host"`
	Port        int    `toml:"port"`
	User        string `toml:"user"`
	Database    string `toml:"database"`
	PasswordEnv string `toml:"passwordEnv"`
	SSLMode     string `toml:"sslMode"`
	MaxIdle     int    `toml:"maxIdleConns"`
	MaxOpen     int    `toml:"maxOpenConns"`
}

func (c PostgresConfig) Password() (string, error) {
	return requiredEnv(c.PasswordEnv)
}

type RedisConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Password string `toml:"password"`
	Database int    `toml:"database"`
}

func (c RedisConfig) Address() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}

type SQLServerConfig struct {
	Host        string `toml:"host"`
	Port        int    `toml:"port"`
	User        string `toml:"user"`
	Database    string `toml:"database"`
	PasswordEnv string `toml:"passwordEnv"`
	Encrypt     bool   `toml:"encrypt"`
}

type LLMConfig struct {
	BaseURL   string `toml:"baseURL"`
	Model     string `toml:"model"`
	APIKeyEnv string `toml:"apiKeyEnv"`
}

func (c LLMConfig) APIKey() (string, error) {
	return requiredEnv(c.APIKeyEnv)
}

func Load() (Config, error) {
	if err := loadDotEnv(); err != nil {
		return Config{}, err
	}

	path := strings.TrimSpace(os.Getenv("MESGUARD_CONFIG_FILE"))
	if path == "" {
		path = "config/mesguard.toml"
	}

	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTP.Host) == "" || c.HTTP.Port <= 0 {
		return errors.New("http host and port are required")
	}
	if strings.TrimSpace(c.Postgres.Host) == "" || c.Postgres.Port <= 0 ||
		strings.TrimSpace(c.Postgres.User) == "" || strings.TrimSpace(c.Postgres.Database) == "" {
		return errors.New("postgres host, port, user, and database are required")
	}
	if strings.TrimSpace(c.Postgres.PasswordEnv) == "" {
		return errors.New("postgres passwordEnv is required")
	}
	if strings.TrimSpace(c.Redis.Host) == "" || c.Redis.Port <= 0 {
		return errors.New("redis host and port are required")
	}
	return nil
}

func loadDotEnv() error {
	err := godotenv.Load(".env")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func requiredEnv(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("environment variable name is not configured")
	}
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("environment variable %q is empty", name)
	}
	return value, nil
}
