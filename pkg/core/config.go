// Package core loads and validates the chat-harness TOML config and wires
// the resulting Providers, Catalog, and Router into a chat.Harness.
package core

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// Config mirrors the TOML schema described in config.example.toml.
type Config struct {
	Server    ServerConfig             `toml:"server"`
	Auth      AuthConfig               `toml:"auth"`
	Providers map[string]ProviderBlock `toml:"providers"`
	Policy    []PolicyConfig           `toml:"policy"`
	Router    RouterConfig             `toml:"router"`
}

type ServerConfig struct {
	Addr string `toml:"addr"`
}

type AuthConfig struct {
	TokenEnv string `toml:"token_env"`
}

// ProviderBlock carries per-provider options. Some keys are only meaningful
// for specific providers (e.g. base_url for ollama, region for bedrock) but
// unknown keys are ignored so the schema stays open.
type ProviderBlock struct {
	Enabled    bool   `toml:"enabled"`
	APIKeyEnv  string `toml:"api_key_env"`
	BaseURL    string `toml:"base_url"`
	Region     string `toml:"region"`
	Profile    string `toml:"profile"`
	APIVersion string `toml:"api_version"`
}

type PolicyConfig struct {
	Name       string   `toml:"name"`
	Candidates []string `toml:"candidates"`
}

type RouterConfig struct {
	DefaultPolicy       string   `toml:"default_policy"`
	FallbackOnKinds     []string `toml:"fallback_on_kinds"`
	PerAttemptTimeoutMS int      `toml:"per_attempt_timeout_ms"`
	MaxAttempts         int      `toml:"max_attempts"`
}

// Load reads path and returns a validated Config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if _, err := toml.Decode(string(data), &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate performs structural checks on the config. It does not check
// external state (env vars, credentials); use ValidateEnv for that.
func (c *Config) Validate() error {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Router.DefaultPolicy == "" && len(c.Policy) > 0 {
		c.Router.DefaultPolicy = c.Policy[0].Name
	}
	names := map[string]bool{}
	for _, p := range c.Policy {
		if p.Name == "" {
			return fmt.Errorf("policy with empty name")
		}
		if names[p.Name] {
			return fmt.Errorf("duplicate policy %q", p.Name)
		}
		names[p.Name] = true
		if len(p.Candidates) == 0 {
			return fmt.Errorf("policy %q has no candidates", p.Name)
		}
		for _, cand := range p.Candidates {
			if _, err := chat.ParseModelRef(cand); err != nil {
				return fmt.Errorf("policy %q: %w", p.Name, err)
			}
		}
	}
	if c.Router.DefaultPolicy != "" && !names[c.Router.DefaultPolicy] {
		return fmt.Errorf("router.default_policy %q not found in [[policy]] entries", c.Router.DefaultPolicy)
	}
	for _, kind := range c.Router.FallbackOnKinds {
		if !validErrorKind(kind) {
			return fmt.Errorf("router.fallback_on_kinds: unknown error kind %q", kind)
		}
	}
	if c.Router.MaxAttempts == 0 {
		c.Router.MaxAttempts = 3
	}
	if c.Router.PerAttemptTimeoutMS == 0 {
		c.Router.PerAttemptTimeoutMS = 60_000
	}
	return nil
}

// ValidateEnv checks that every enabled provider's credentials are resolvable
// and returns a slice of human-readable notes describing the final state.
// It never consults a network.
func (c *Config) ValidateEnv() ([]string, error) {
	var notes []string
	for name, pb := range c.Providers {
		if !pb.Enabled {
			notes = append(notes, fmt.Sprintf("provider %s: disabled", name))
			continue
		}
		key := defaultAPIKeyEnv(name, pb.APIKeyEnv)
		switch name {
		case "ollama":
			url := pb.BaseURL
			if url == "" {
				url = "http://localhost:11434"
			}
			notes = append(notes, fmt.Sprintf("provider ollama: enabled, base_url=%s", url))
		case "anthropic", "openai":
			if key == "" {
				notes = append(notes, fmt.Sprintf("provider %s: enabled, no explicit api_key_env; will use default fallbacks", name))
			} else if _, ok := os.LookupEnv(key); ok {
				notes = append(notes, fmt.Sprintf("provider %s: enabled, credentials via env $%s", name, key))
			} else {
				notes = append(notes, fmt.Sprintf("provider %s: enabled, $%s unset — will fall back to ~/.config/chat-harness/%s.key", name, key, name))
			}
		default:
			notes = append(notes, fmt.Sprintf("provider %s: enabled (validation deferred; not implemented in Phase 1)", name))
		}
	}
	return notes, nil
}

func defaultAPIKeyEnv(provider, override string) string {
	if override != "" {
		return override
	}
	switch provider {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	}
	return ""
}

func validErrorKind(s string) bool {
	switch chat.ErrorKind(s) {
	case chat.ErrKindRateLimit, chat.ErrKindTimeout, chat.ErrKindContextLength,
		chat.ErrKindOverloaded, chat.ErrKindServerError, chat.ErrKindToolsUnsupported,
		chat.ErrKindUnsupportedContent, chat.ErrKindCanceled, chat.ErrKindAuthFailed,
		chat.ErrKindNotFound, chat.ErrKindInvalidRequest, chat.ErrKindUnknown:
		return true
	}
	return false
}
