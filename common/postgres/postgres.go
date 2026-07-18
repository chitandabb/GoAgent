package postgres

import (
	"GopherAI/config"
	"GopherAI/model"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func InitPostgres() error {
	c := config.GetConfig()
	password, err := passwordFromEnv(c.PostgresPasswordEnv)
	if err != nil {
		return err
	}

	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%d sslmode=%s TimeZone=Asia/Shanghai",
		c.PostgresHost,
		c.PostgresUser,
		password,
		c.PostgresDatabaseName,
		c.PostgresPort,
		c.PostgresSSLMode,
	)

	var gormLogger logger.Interface
	if gin.Mode() == gin.DebugMode {
		gormLogger = logger.Default.LogMode(logger.Info)
	} else {
		gormLogger = logger.Default
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxIdleConns(c.PostgresMaxIdleConns)
	sqlDB.SetMaxOpenConns(c.PostgresMaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Hour)

	DB = db
	return migrate()
}

func passwordFromEnv(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("postgres password environment variable is not configured")
	}

	password := os.Getenv(name)
	if password == "" {
		return "", fmt.Errorf("postgres password environment variable %q is empty", name)
	}
	return password, nil
}

func migrate() error {
	return DB.AutoMigrate(
		new(model.User),
		new(model.Session),
		new(model.Message),
	)
}

func InsertUser(user *model.User) (*model.User, error) {
	err := DB.Create(user).Error
	return user, err
}

func GetUserByUsername(username string) (*model.User, error) {
	user := new(model.User)
	err := DB.Where("username = ?", username).First(user).Error
	return user, err
}
