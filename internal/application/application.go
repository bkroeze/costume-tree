// Package application owns process-level HTTP server startup and shutdown.
// The command composes configuration and feature dependencies before calling Run;
// this package does not construct persistence or presentation dependencies itself.
package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Config contains process-level server settings.
type Config struct {
	Address         string
	ShutdownTimeout time.Duration
}

// Dependencies are constructed by the command composition root.
type Dependencies struct {
	Handler http.Handler
	Logger  *slog.Logger
}

// Run serves HTTP requests until the context is canceled or the server fails.
func Run(ctx context.Context, cfg Config, deps Dependencies) error {
	if deps.Handler == nil {
		return errors.New("application: HTTP handler is required")
	}
	if deps.Logger == nil {
		return errors.New("application: logger is required")
	}
	if cfg.Address == "" {
		return errors.New("application: listen address is required")
	}
	if cfg.ShutdownTimeout <= 0 {
		return errors.New("application: shutdown timeout must be positive")
	}

	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("application: listen on %q: %w", cfg.Address, err)
	}

	server := &http.Server{
		Handler:           deps.Handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()

	deps.Logger.Info("HTTP server started", "address", listener.Addr().String())

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("application: serve HTTP: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return fmt.Errorf("application: shut down HTTP server: %w", err)
	}
	if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("application: serve HTTP during shutdown: %w", err)
	}

	deps.Logger.Info("HTTP server stopped")
	return nil
}
