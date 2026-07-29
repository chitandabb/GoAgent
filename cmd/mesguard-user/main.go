package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/chitandabb/GoAgent/internal/auth"
	"github.com/chitandabb/GoAgent/internal/platform/config"
	platformlogger "github.com/chitandabb/GoAgent/internal/platform/logger"
	"github.com/chitandabb/GoAgent/internal/platform/migration"
	platformpostgres "github.com/chitandabb/GoAgent/internal/platform/postgres"

	"go.uber.org/zap"
)

const defaultPasswordEnv = "MESGUARD_INITIAL_USER_PASSWORD"

func main() {
	log := platformlogger.NewBootstrapFor("mesguard-user")
	defer platformlogger.Sync(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, log); err != nil {
		log.Error("local user command failed", zap.Error(err))
		platformlogger.Sync(log)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer, log *zap.Logger) error {
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	db, closeDB, err := platformpostgres.Open(ctx, cfg.Postgres, log.Named("postgres"))
	if err != nil {
		return err
	}
	defer func() { _ = closeDB() }()
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get postgres sql db: %w", err)
	}
	if err := migration.CheckCurrent(ctx, sqlDB); err != nil {
		return fmt.Errorf("check database migration version: %w", err)
	}

	password, ok := os.LookupEnv(options.passwordEnv)
	if !ok || password == "" {
		return fmt.Errorf("password environment variable %q is empty", options.passwordEnv)
	}
	hasher, err := auth.NewArgon2idHasher(auth.DefaultArgon2idParams())
	if err != nil {
		return fmt.Errorf("build password hasher: %w", err)
	}
	service, err := auth.NewUserProvisioner(platformpostgres.NewUserRepository(db), hasher)
	if err != nil {
		return fmt.Errorf("build user provisioner: %w", err)
	}
	user, err := service.Create(ctx, auth.CreateUserInput{
		Username:           options.username,
		DisplayName:        options.displayName,
		Password:           password,
		Role:               auth.Role(options.role),
		MustChangePassword: options.mustChangePassword,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "created user id=%s username=%s role=%s\n", user.ID, user.Username, user.Role)
	return err
}

type commandOptions struct {
	username           string
	displayName        string
	role               string
	passwordEnv        string
	mustChangePassword bool
}

func parseOptions(args []string) (commandOptions, error) {
	flags := flag.NewFlagSet("mesguard-user", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options commandOptions
	flags.StringVar(&options.username, "username", "", "normalized login username")
	flags.StringVar(&options.displayName, "display-name", "", "display name")
	flags.StringVar(&options.role, "role", string(auth.RoleAnalyst), "analyst or admin")
	flags.StringVar(&options.passwordEnv, "password-env", defaultPasswordEnv, "environment variable containing initial password")
	flags.BoolVar(&options.mustChangePassword, "must-change-password", true, "require password change after first login")
	if err := flags.Parse(args); err != nil {
		return commandOptions{}, fmt.Errorf("parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return commandOptions{}, errors.New("unexpected positional arguments")
	}
	if strings.TrimSpace(options.username) == "" || strings.TrimSpace(options.displayName) == "" {
		return commandOptions{}, errors.New("usage: mesguard-user -username <name> -display-name <name> [-role analyst|admin]")
	}
	if strings.TrimSpace(options.passwordEnv) == "" {
		return commandOptions{}, errors.New("password environment variable name is empty")
	}
	return options, nil
}
