package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"costume-tree/internal/application"
	"costume-tree/internal/config"
	"costume-tree/internal/storage"
	"costume-tree/internal/web"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
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

	if err := database.Migrate(context.Background()); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	handler, err := web.New(logger, database)
	if err != nil {
		return err
	}

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
