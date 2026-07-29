package config

// ============================================================
// 配置层：负责从 .env 文件和 TOML 配置文件加载 MESGuard 的运行配置。
//
// 加载顺序：.env → 确定 TOML 路径 → 解码 TOML → 校验
// 同名环境变量优先级：系统环境变量 > .env 文件中的变量
// TOML 普通字段不会被任意环境变量自动覆盖；敏感值通过 passwordEnv 显式引用。
// ============================================================

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/joho/godotenv"
)

// Config 是 MESGuard 的顶层配置结构，对应 config/mesguard.toml 文件的顶层键。
// 每个字段对应 TOML 中的一个配置块（通过 toml tag 映射）。
type Config struct {
	HTTP     HTTPConfig     `toml:"http"`     // [http] 配置块：API 服务器监听地址
	Auth     AuthConfig     `toml:"auth"`     // [auth] 配置块：Session、Cookie 与可信前端来源
	Log      LogConfig      `toml:"log"`      // [log] 配置块：结构化日志和文件轮转
	Postgres PostgresConfig `toml:"postgres"` // [postgres] 配置块：PostgreSQL 数据库连接配置
	Redis    RedisConfig    `toml:"redis"`    // [redis] 配置块：Redis 缓存连接配置
}

// AuthConfig 定义本地账号认证的 Session 时长和浏览器安全边界。
type AuthConfig struct {
	AllowedOrigins         []string `toml:"allowedOrigins"`
	CookieDomain           string   `toml:"cookieDomain"`
	CookieSecure           bool     `toml:"cookieSecure"`
	SessionIdleMinutes     int      `toml:"sessionIdleMinutes"`
	SessionAbsoluteMinutes int      `toml:"sessionAbsoluteMinutes"`
}

// Validate 检查 Session 时长和可信 Origin。
func (c AuthConfig) Validate() error {
	if c.SessionIdleMinutes <= 0 || c.SessionAbsoluteMinutes <= 0 {
		return errors.New("auth session idle and absolute minutes must be positive")
	}
	if c.SessionIdleMinutes > c.SessionAbsoluteMinutes {
		return errors.New("auth session idle minutes cannot exceed absolute minutes")
	}
	if len(c.AllowedOrigins) == 0 {
		return errors.New("auth allowedOrigins must not be empty")
	}
	for _, rawOrigin := range c.AllowedOrigins {
		origin := strings.TrimSpace(rawOrigin)
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
			return fmt.Errorf("invalid auth allowed origin %q", rawOrigin)
		}
	}
	return nil
}

// ============================================================
// HTTP 配置
// ============================================================

// HTTPConfig 对应 TOML 文件中的 [http] 配置块，用于配置 API 服务器的监听地址。
type HTTPConfig struct {
	Host string `toml:"host"` // 监听主机名，如 "0.0.0.0" 表示监听所有网卡
	Port int    `toml:"port"` // 监听端口号，如 8080
}

// Address 将 Host 和 Port 拼接成 net/http 需要的 "host:port" 格式。
// 例如：Host="0.0.0.0", Port=8080 → 返回 "0.0.0.0:8080"
func (c HTTPConfig) Address() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}

// ============================================================
// 日志配置
// ============================================================

// LogConfig 对应 TOML 文件中的 [log] 配置块。
// 控制台格式由 format 控制；文件日志固定使用 JSON，便于检索和采集。
type LogConfig struct {
	Level       string `toml:"level"`       // debug / info / warn / error
	Format      string `toml:"format"`      // console / json
	Environment string `toml:"environment"` // development / production
	EnableFile  bool   `toml:"enableFile"`  // 是否同时写入滚动日志文件
	OutputDir   string `toml:"outputDir"`   // 日志文件目录
	MaxSize     int    `toml:"maxSizeMB"`   // 单个文件最大 MB
	MaxAge      int    `toml:"maxAgeDays"`  // 最长保留天数
	MaxBackups  int    `toml:"maxBackups"`  // 最多保留的旧文件数
	Compress    bool   `toml:"compress"`    // 是否压缩旧文件
}

// ============================================================
// PostgreSQL 配置
// ============================================================

// PostgresConfig 对应 TOML 文件中的 [postgres] 配置块，
// 用于配置 PostgreSQL 数据库连接池和连接参数。
type PostgresConfig struct {
	Host        string `toml:"host"`         // 数据库主机地址，如 "localhost"
	Port        int    `toml:"port"`         // 数据库端口，默认 5432
	User        string `toml:"user"`         // 数据库用户名
	Database    string `toml:"database"`     // 要连接的数据库名称
	PasswordEnv string `toml:"passwordEnv"`  // 密码来源：指定存放密码的环境变量名（密码不写在 TOML 里，更安全）
	SSLMode     string `toml:"sslMode"`      // SSL 连接模式，如 "disable" / "require" / "verify-full"
	MaxIdle     int    `toml:"maxIdleConns"` // 连接池最大空闲连接数
	MaxOpen     int    `toml:"maxOpenConns"` // 连接池最大打开连接数
}

// Password 根据 PasswordEnv 指定的环境变量名读取数据库密码。
// 设计意图：密码不写入 TOML 配置文件（防止泄露），而是通过环境变量注入（如 Docker secret / CI 环境变量）。
func (c PostgresConfig) Password() (string, error) {
	return requiredEnv(c.PasswordEnv)
}

// ============================================================
// Redis 配置
// ============================================================

// RedisConfig 对应 TOML 文件中的 [redis] 配置块，
// 用于配置 Redis 客户端连接参数。
type RedisConfig struct {
	Host     string `toml:"host"`     // Redis 服务器地址，如 "localhost"
	Port     int    `toml:"port"`     // Redis 端口，默认 6379
	Password string `toml:"password"` // Redis 认证密码（可选，可留空）
	Database int    `toml:"database"` // 选择的 Redis 数据库编号，默认 0
}

// Address 将 Host 和 Port 拼接成 Redis 客户端需要的 "host:port" 格式。
func (c RedisConfig) Address() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}

// ============================================================
// 配置加载入口
// ============================================================

// Load 是配置加载的唯一入口，按以下顺序执行：
//  1. 加载 .env 文件（仓库根目录的 .env，主要用于本地开发）
//  2. 读取 MESGUARD_CONFIG_FILE 环境变量，确定 TOML 文件路径
//     - 如果该环境变量为空，默认使用 config/mesguard.toml
//  3. 解码 TOML 文件到 Config 结构体
//  4. 调用 Validate() 检查必要字段是否都已填写
//
// 同名环境变量优先级：系统环境变量（已存在的）> .env 文件中的变量。
// 原因：godotenv 默认不会覆盖已存在的系统环境变量，生产环境通过 Docker/K8s
// 注入的密码和配置文件路径应优先于本地 .env。
func Load() (Config, error) {
	// 第一步：加载 .env 文件
	// 本地开发时，开发者可以在 .env 中写 MESGUARD_CONFIG_FILE 等变量。
	// 文件不存在时静默跳过（生产环境通常不部署 .env）。
	if err := loadDotEnv(); err != nil {
		return Config{}, err
	}

	// 第二步：确定 TOML 配置文件路径
	// 优先读取 MESGUARD_CONFIG_FILE 环境变量，允许外部指定配置路径。
	// 典型用法：MESGUARD_CONFIG_FILE=/etc/mesguard/config.toml ./mesguard-api
	path := strings.TrimSpace(os.Getenv("MESGUARD_CONFIG_FILE"))
	if path == "" {
		path = "config/mesguard.toml"
	}

	// 第三步：解码 TOML 文件到 Config 结构体
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}

	// 第四步：校验配置完整性，确保启动前所有必要字段都已填写
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// ============================================================
// 配置校验
// ============================================================

// Validate 在服务启动前检查配置是否完整，避免运行到一半才发现缺配置。
// 如果任何必填字段缺失，返回明确的错误信息，启动失败。
func (c Config) Validate() error {
	// 校验 HTTP 配置
	if strings.TrimSpace(c.HTTP.Host) == "" || c.HTTP.Port <= 0 {
		return errors.New("http host and port are required")
	}
	if err := c.Auth.Validate(); err != nil {
		return err
	}
	if err := c.Log.Validate(); err != nil {
		return err
	}
	// 校验 PostgreSQL 配置：至少需要 host、port、user、database 和 passwordEnv
	if strings.TrimSpace(c.Postgres.Host) == "" || c.Postgres.Port <= 0 ||
		strings.TrimSpace(c.Postgres.User) == "" || strings.TrimSpace(c.Postgres.Database) == "" {
		return errors.New("postgres host, port, user, and database are required")
	}
	if strings.TrimSpace(c.Postgres.PasswordEnv) == "" {
		return errors.New("postgres passwordEnv is required")
	}
	// 校验 Redis 配置
	if strings.TrimSpace(c.Redis.Host) == "" || c.Redis.Port <= 0 {
		return errors.New("redis host and port are required")
	}
	return nil
}

// Validate 检查日志级别、格式和文件轮转参数。
func (c LogConfig) Validate() error {
	switch strings.ToLower(strings.TrimSpace(c.Level)) {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("log level must be debug, info, warn, or error")
	}
	switch strings.ToLower(strings.TrimSpace(c.Format)) {
	case "console", "json":
	default:
		return errors.New("log format must be console or json")
	}
	if strings.TrimSpace(c.Environment) == "" {
		return errors.New("log environment is required")
	}
	if c.EnableFile && (strings.TrimSpace(c.OutputDir) == "" || c.MaxSize <= 0 || c.MaxAge <= 0 || c.MaxBackups <= 0) {
		return errors.New("log file outputDir, maxSizeMB, maxAgeDays, and maxBackups must be configured")
	}
	return nil
}

// ============================================================
// 内部辅助函数
// ============================================================

// loadDotEnv 加载仓库根目录的 .env 文件。
// 设计意图：本地开发时使用 .env 管理环境变量，生产环境通过 Docker/K8s 注入。
// 文件不存在时返回 nil（允许继续启动），其他错误（权限问题等）则返回。
func loadDotEnv() error {
	err := godotenv.Load(".env")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// requiredEnv 读取指定名称的环境变量，确保其已设置且非空。
// 用于读取敏感信息（如数据库密码），避免硬编码在配置文件中。
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
