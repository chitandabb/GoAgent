package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"
	"github.com/chitandabb/GoAgent/internal/platform/migration"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"
)

var errInvalidCommand = errors.New("invalid migration command")

func main() {
	log := platformlogger.NewBootstrapFor("mesguard-migrate")
	defer platformlogger.Sync(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		command := ""
		if len(os.Args) == 2 {
			command = os.Args[1]
		}
		fields := []zap.Field{zap.String("command", command), zap.Error(err)}
		if errors.Is(err, errInvalidCommand) {
			log.Warn("invalid database migration command", fields...)
		} else {
			log.Error("database migration command failed", fields...)
		}
		platformlogger.Sync(log)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf(
			"%w: usage: mesguard-migrate <up|down|status|version|check>",
			errInvalidCommand,
		)
	}
	command := args[0]
	if !isSupportedCommand(command) {
		return fmt.Errorf(
			"%w: unknown command %q; expected up, down, status, version, or check",
			errInvalidCommand,
			command,
		)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	dsn, err := platformpostgres.ConnectionString(cfg.Postgres)
	if err != nil {
		return fmt.Errorf("build postgres connection: %w", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return errors.New("open postgres connection")
	}
	provider, err := migration.NewProvider(db)
	if err != nil {
		_ = db.Close()
		return err
	}
	defer func() { _ = provider.Close() }()

	if err := provider.Ping(ctx); err != nil {
		return errors.New("postgres is unavailable")
	}

	switch command {
	case "up":
		results, err := provider.Up(ctx)
		if err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		if len(results) == 0 {
			_, err = fmt.Fprintln(output, "database is already current")
			return err
		}
		for _, result := range results {
			if _, err := fmt.Fprintln(output, result.String()); err != nil {
				return err
			}
		}
		return nil
	case "down":
		result, err := provider.Down(ctx)
		if err != nil {
			return fmt.Errorf("migrate down: %w", err)
		}
		_, err = fmt.Fprintln(output, result.String())
		return err
	case "status":
		statuses, err := provider.Status(ctx)
		if err != nil {
			return fmt.Errorf("migration status: %w", err)
		}
		for _, status := range statuses {
			appliedAt := "-"
			if !status.AppliedAt.IsZero() {
				appliedAt = status.AppliedAt.Format("2006-01-02T15:04:05Z07:00")
			}
			if _, err := fmt.Fprintf(
				output,
				"%05d %-7s %s %s\n",
				status.Source.Version,
				status.State,
				appliedAt,
				status.Source.Path,
			); err != nil {
				return err
			}
		}
		return nil
	case "version":
		current, target, err := provider.GetVersions(ctx)
		if err != nil {
			return fmt.Errorf("read migration versions: %w", err)
		}
		_, err = fmt.Fprintf(output, "current=%d target=%d\n", current, target)
		return err
	case "check":
		if err := migration.CheckCurrent(ctx, db); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "database schema is current")
		return err
	default:
		return errors.New("validated migration command was not handled")
	}
}

func isSupportedCommand(command string) bool {
	switch command {
	case "up", "down", "status", "version", "check":
		return true
	default:
		return false
	}
}
