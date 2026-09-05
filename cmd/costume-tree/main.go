package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"costume-tree/internal/application"
	"costume-tree/internal/config"
	"costume-tree/internal/storage"
	"costume-tree/internal/web"
)

const operatorCommandTimeout = 5 * time.Minute

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var err error
	if len(os.Args) > 1 {
		err = runOperatorCommand(os.Args[1:])
	} else {
		err = run(logger)
	}
	if err != nil {
		logger.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) (runErr error) {
	settings, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}

	database, err := storage.Open(settings.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if closeErr := database.Close(); closeErr != nil && runErr == nil {
			runErr = fmt.Errorf("close database: %w", closeErr)
		}
	}()

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStartup()
	if err := database.Migrate(startupCtx); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	if err := database.Ready(startupCtx); err != nil {
		return fmt.Errorf("database readiness validation failed: %w", err)
	}

	handler, err := web.New(logger, database)
	if err != nil {
		return err
	}
	handler = http.TimeoutHandler(
		http.MaxBytesHandler(handler, settings.MaxBodyBytes),
		settings.RequestTimeout,
		"request timed out\n",
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return application.Run(ctx, application.Config{
		Address:         settings.Address,
		ShutdownTimeout: settings.ShutdownTimeout,
	}, application.Dependencies{
		Handler: handler,
		Logger:  logger,
	})
}

func runOperatorCommand(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return errors.New(operatorUsage)
	}

	ctx, cancel := context.WithTimeout(context.Background(), operatorCommandTimeout)
	defer cancel()
	switch args[0] {
	case "backup":
		if len(args) != 2 {
			return errors.New(operatorUsage)
		}
		settings, err := config.Load(os.LookupEnv)
		if err != nil {
			return err
		}
		if err := storage.BackupFile(ctx, settings.DatabasePath, args[1]); err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}
		return nil
	case "restore":
		if len(args) != 3 {
			return errors.New(operatorUsage)
		}
		if err := storage.Restore(ctx, args[1], args[2]); err != nil {
			return fmt.Errorf("restore failed: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], operatorUsage)
	}
}

const operatorUsage = `usage:
  costume-tree                         start the web application
  costume-tree backup BACKUP_PATH      create a validated snapshot from COSTUME_TREE_DB_PATH
  costume-tree restore BACKUP DB_PATH  validate and restore into a new path

Backup may run while the application is serving. Restore is intentionally
cold-only: stop the application first and restore into a path that does not
already exist. The process user must own the database and /data directory.`
