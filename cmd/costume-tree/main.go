package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"costume-tree/internal/application"
	"costume-tree/internal/config"
	"costume-tree/internal/photos"
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

	database, err := storage.Open(settings.PostgresURL(), settings.DBMaxOpenConns)
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

	photoService, err := photos.New(settings.CostumeTreeDir, storage.NewCostumeItemPhotoRepository(database), logger)
	if err != nil {
		return fmt.Errorf("initialize photo service: %w", err)
	}
	photoContext, cancelPhotos := context.WithCancel(context.Background())
	if err := photoService.Start(photoContext); err != nil {
		cancelPhotos()
		return fmt.Errorf("start photo service: %w", err)
	}
	defer func() {
		cancelPhotos()
		photoService.Close()
	}()

	handler, err := web.New(logger, web.Dependencies{Readiness: database, Photos: photoService})
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
