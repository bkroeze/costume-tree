package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"costume-tree/internal/application"
	"costume-tree/internal/config"
	"costume-tree/internal/web"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	settings, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}

	handler, err := web.New(logger)
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
