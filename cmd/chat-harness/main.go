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
)

// Set via -ldflags at build time.
var (
	GitSHA    = "dev"
	BuildDate = "unknown"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	a, err := parseArgs(os.Args[1:])
	if err != nil {
		// flag.ContinueOnError has already printed usage.
		os.Exit(2)
	}

	if a.ShowVersion {
		fmt.Printf("chat-harness %s (built %s)\n", GitSHA, BuildDate)
		return
	}

	if a.ValidateConfig {
		// Phase 1 fleshes this out; for now we just confirm the file is readable.
		if _, err := os.Stat(a.ConfigPath); err != nil {
			logger.Error("config not found", "path", a.ConfigPath, "err", err)
			os.Exit(1)
		}
		fmt.Printf("config ok: %s\n", a.ConfigPath)
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	addr := a.Addr
	if addr == "" {
		addr = ":8080"
	}

	// Auth: default closed. Refuse to start if token is missing unless
	// --allow-unauthenticated or CHAT_HARNESS_INSECURE_ALLOW_OPEN=1.
	token := os.Getenv("CHAT_HARNESS_TOKEN")
	openFlag := a.AllowUnauthenticated || os.Getenv("CHAT_HARNESS_INSECURE_ALLOW_OPEN") == "1"
	if token == "" && !openFlag {
		logger.Error("CHAT_HARNESS_TOKEN is unset; refusing to start in closed mode",
			"hint", "set CHAT_HARNESS_TOKEN, or pass --allow-unauthenticated for dev")
		os.Exit(1)
	}
	if token == "" && openFlag {
		logger.Warn("CRIT: running without authentication (--allow-unauthenticated)")
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(logger, token),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr, "version", GitSHA)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-errCh:
		if err != nil {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
