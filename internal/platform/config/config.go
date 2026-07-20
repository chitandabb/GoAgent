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
	HTTP     HTTPConfig     `toml:"http"`
	Postgres PostgresConfig `toml:"postgres"`
	Redis    RedisConfig    `toml:"redis"`
}

// HTTPConfig 对应 TOML 中的 [http] 配置块。
type HTTPConfig struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

// Address 组装 net/http Server 需要的监听地址。
func (c HTTPConfig) Address() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}

// PostgresConfig 对应 TOML 中的 [postgres] 配置块。
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

// Password 根据 passwordEnv 指定的名称读取环境变量，避免密码写入 TOML。
func (c PostgresConfig) Password() (string, error) {
	return requiredEnv(c.PasswordEnv)
}

// RedisConfig 对应 TOML 中的 [redis] 配置块。
type RedisConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Password string `toml:"password"`
	Database int    `toml:"database"`
}

// Address 组装 Redis 客户端需要的 host:port 地址。
func (c RedisConfig) Address() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}

// Load 按“.env -> TOML -> 校验”的顺序加载类型化配置。
// 已存在的系统环境变量优先级高于 .env 文件。
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

// Validate 在服务启动前检查必要配置，避免运行过程中才发现配置缺失。
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

// loadDotEnv 加载仓库根目录的 .env；文件不存在时允许继续启动。
func loadDotEnv() error {
	err := godotenv.Load(".env")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// requiredEnv 读取必须存在且非空的环境变量。
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
