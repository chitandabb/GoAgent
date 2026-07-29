// Package sqlserver 提供公司 ERP SQL Server 的只读基础设施适配器。
package sqlserver

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/chitandabb/GoAgent/internal/platform/config"

	_ "github.com/microsoft/go-mssqldb"
)

func ConnectionString(cfg config.SQLServerConfig) (string, error) {
	password, err := cfg.Password()
	if err != nil {
		return "", err
	}
	dsn := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(cfg.User, password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
	}
	query := dsn.Query()
	query.Set("database", cfg.Database)
	query.Set("encrypt", cfg.Encrypt)
	query.Set("TrustServerCertificate", strconv.FormatBool(cfg.TrustServerCertificate))
	query.Set("ApplicationIntent", "ReadOnly")
	query.Set("app name", "MESGuard")
	dsn.RawQuery = query.Encode()
	return dsn.String(), nil
}

func Open(_ context.Context, cfg config.SQLServerConfig) (*sql.DB, error) {
	dsn, err := ConnectionString(cfg)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlserver: %w", err)
	}
	db.SetMaxIdleConns(cfg.MaxIdle)
	db.SetMaxOpenConns(cfg.MaxOpen)
	db.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}
