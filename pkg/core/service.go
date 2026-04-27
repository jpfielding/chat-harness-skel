package core

import (
	"fmt"
	"log/slog"
	"os"

	"time"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
	"github.com/jpfielding/chat-harness-skel/pkg/providers/anthropic"
	"github.com/jpfielding/chat-harness-skel/pkg/providers/ollama"
	"github.com/jpfielding/chat-harness-skel/pkg/providers/openai"
	"github.com/jpfielding/chat-harness-skel/pkg/router"
	"github.com/jpfielding/chat-harness-skel/pkg/session"
)

// Service bundles a configured Harness, its logger, and the Config used to
// build it. The HTTP layer takes a *Service to build handlers.
type Service struct {
	Cfg      *Config
	Harness  *chat.Harness
	Sessions session.Store
	Logger   *slog.Logger
}

// Build constructs providers from cfg, wires them into a Harness, and
// returns the resulting Service. Providers whose credentials fail to load
// are skipped with a warning rather than failing startup — the user can
// start the server with only some providers enabled.
func Build(cfg *Config, logger *slog.Logger) (*Service, error) {
	store := session.NewMemoryStore(session.MaxMessagesCap)
	catalog := chat.NewCatalog()
	harness := chat.New(
		chat.WithLogger(logger),
		chat.WithSessions(session.NewBinder(store)),
		chat.WithCatalog(catalog),
	)

	for name, pb := range cfg.Providers {
		if !pb.Enabled {
			continue
		}
		switch name {
		case "anthropic":
			key, err := resolveKey(pb.APIKeyEnv, "ANTHROPIC_API_KEY", anthropic.ResolveAPIKey)
			if err != nil {
				logger.Warn("anthropic provider skipped", "err", err)
				continue
			}
			p, err := anthropic.New(anthropic.Config{
				APIKey:     key,
				BaseURL:    pb.BaseURL,
				APIVersion: pb.APIVersion,
			})
			if err != nil {
				return nil, fmt.Errorf("anthropic.New: %w", err)
			}
			chat.WithProvider(p)(harness)
			registerModels(harness, p.Models())
		case "openai":
			key, err := resolveKey(pb.APIKeyEnv, "OPENAI_API_KEY", openai.ResolveAPIKey)
			if err != nil {
				logger.Warn("openai provider skipped", "err", err)
				continue
			}
			p, err := openai.New(openai.Config{
				APIKey:  key,
				BaseURL: pb.BaseURL,
			})
			if err != nil {
				return nil, fmt.Errorf("openai.New: %w", err)
			}
			chat.WithProvider(p)(harness)
			registerModels(harness, p.Models())
		case "ollama":
			p, err := ollama.New(ollama.Config{BaseURL: pb.BaseURL})
			if err != nil {
				logger.Warn("ollama provider skipped", "err", err)
				continue
			}
			chat.WithProvider(p)(harness)
			registerModels(harness, p.Models())
			logger.Info("ollama registered", "tools_supported", p.SupportsTools())
		default:
			logger.Warn("unknown provider in config", "name", name)
		}
	}

	// Build the router from policies in config.
	policies := make([]router.Policy, 0, len(cfg.Policy))
	for _, p := range cfg.Policy {
		cands := make([]chat.ModelRef, 0, len(p.Candidates))
		for _, s := range p.Candidates {
			ref, err := chat.ParseModelRef(s)
			if err != nil {
				return nil, err
			}
			cands = append(cands, ref)
		}
		policies = append(policies, router.Policy{Name: p.Name, Candidates: cands})
	}
	r, err := router.NewPolicyRouter(catalog, cfg.Router.DefaultPolicy, policies)
	if err != nil {
		return nil, fmt.Errorf("build router: %w", err)
	}
	chat.WithRouter(r)(harness)

	// Fallback policy.
	fb := chat.FallbackPolicy{
		FallbackOnKinds:   map[chat.ErrorKind]bool{},
		MaxAttempts:       cfg.Router.MaxAttempts,
		PerAttemptTimeout: time.Duration(cfg.Router.PerAttemptTimeoutMS) * time.Millisecond,
	}
	for _, k := range cfg.Router.FallbackOnKinds {
		fb.FallbackOnKinds[chat.ErrorKind(k)] = true
	}
	chat.WithFallback(fb)(harness)

	return &Service{Cfg: cfg, Harness: harness, Sessions: store, Logger: logger}, nil
}

// resolveKey tries the configured env var, then the provider's ResolveAPIKey
// (which falls back to ~/.config/chat-harness/<name>.key).
func resolveKey(envOverride, envDefault string, resolve func() (string, error)) (string, error) {
	env := envOverride
	if env == "" {
		env = envDefault
	}
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	return resolve()
}

func registerModels(h *chat.Harness, models []chat.ModelInfo) {
	for _, m := range models {
		h.Catalog().Register(m)
	}
}
