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

	"github.com/jpfielding/chat-harness-skel/pkg/core"
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
		os.Exit(2)
	}

	if a.ShowVersion {
		fmt.Printf("chat-harness %s (built %s)\n", GitSHA, BuildDate)
		return
	}

	cfg, err := core.Load(a.ConfigPath)
	if err != nil {
		logger.Error("config load failed", "path", a.ConfigPath, "err", err)
		os.Exit(1)
	}

	if a.ValidateConfig {
		notes, _ := cfg.ValidateEnv()
		fmt.Printf("config ok: %s\n", a.ConfigPath)
		fmt.Printf("server.addr=%s  auth.token_env=%s\n", cfg.Server.Addr, cfg.Auth.TokenEnv)
		fmt.Printf("policies (%d):\n", len(cfg.Policy))
		for _, p := range cfg.Policy {
			fmt.Printf("  - %s: %v\n", p.Name, p.Candidates)
		}
		fmt.Printf("router.default_policy=%s  fallback_on=%v  max_attempts=%d  per_attempt_timeout_ms=%d\n",
			cfg.Router.DefaultPolicy, cfg.Router.FallbackOnKinds, cfg.Router.MaxAttempts, cfg.Router.PerAttemptTimeoutMS)
		fmt.Println("providers:")
		for _, n := range notes {
			fmt.Printf("  - %s\n", n)
		}
		return
	}

	svc, err := core.Build(cfg, logger)
	if err != nil {
		logger.Error("service build failed", "err", err)
		os.Exit(1)
	}

	addr := a.Addr
	if addr == "" {
		addr = cfg.Server.Addr
	}
	if addr == "" {
		addr = ":8080"
	}

	tokenEnv := cfg.Auth.TokenEnv
	if tokenEnv == "" {
		tokenEnv = "CHAT_HARNESS_TOKEN"
	}
	token := os.Getenv(tokenEnv)
	openFlag := a.AllowUnauthenticated || os.Getenv("CHAT_HARNESS_INSECURE_ALLOW_OPEN") == "1"
	if token == "" && !openFlag {
		logger.Error("auth token unset; refusing to start in closed mode",
			"token_env", tokenEnv,
			"hint", fmt.Sprintf("set $%s, or pass --allow-unauthenticated for dev", tokenEnv))
		os.Exit(1)
	}
	if token == "" && openFlag {
		logger.Warn("CRIT: running without authentication (--allow-unauthenticated)")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(logger, token, svc),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening",
			"addr", addr,
			"version", GitSHA,
			"providers", providerNames(svc),
		)
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

func providerNames(svc *core.Service) []string {
	return svc.Harness.Providers()
}
