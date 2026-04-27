package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

const validConfig = `
[server]
addr = ":8080"

[auth]
token_env = "CHAT_HARNESS_TOKEN"

[providers.anthropic]
enabled = true

[providers.openai]
enabled = true

[[policy]]
name = "fast"
candidates = ["openai:gpt-5-mini", "anthropic:claude-haiku-4-5"]

[[policy]]
name = "reasoning"
candidates = ["anthropic:claude-opus-4-5", "openai:gpt-5"]

[router]
default_policy = "fast"
fallback_on_kinds = ["RateLimit", "Timeout", "ServerError"]
per_attempt_timeout_ms = 30000
max_attempts = 3
`

func TestLoad_Valid(t *testing.T) {
	p := writeTmp(t, validConfig)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Policy) != 2 {
		t.Errorf("got %d policies", len(cfg.Policy))
	}
	if cfg.Router.DefaultPolicy != "fast" {
		t.Errorf("default_policy=%q", cfg.Router.DefaultPolicy)
	}
}

func TestLoad_BadModelRef(t *testing.T) {
	p := writeTmp(t, `
[[policy]]
name = "x"
candidates = ["missing-colon"]
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "missing-colon") {
		t.Fatalf("want ref error, got %v", err)
	}
}

func TestLoad_UnknownFallbackKind(t *testing.T) {
	p := writeTmp(t, `
[[policy]]
name = "x"
candidates = ["openai:gpt-5"]
[router]
default_policy = "x"
fallback_on_kinds = ["MadeUpKind"]
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "MadeUpKind") {
		t.Fatalf("want unknown-kind error, got %v", err)
	}
}

func TestLoad_UnknownDefaultPolicy(t *testing.T) {
	p := writeTmp(t, `
[[policy]]
name = "one"
candidates = ["openai:gpt-5"]
[router]
default_policy = "ghost"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unknown-default error, got %v", err)
	}
}

func TestValidateEnv_NotesShape(t *testing.T) {
	p := writeTmp(t, validConfig)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	notes, err := cfg.ValidateEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 {
		t.Fatal("expected at least one note")
	}
}
