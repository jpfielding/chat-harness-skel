package anthropic

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ResolveAPIKey looks up the Anthropic API key in this order:
//   1. The ANTHROPIC_API_KEY env var
//   2. ~/.config/chat-harness/anthropic.key (one line)
// Returns an error listing the locations tried if none of them work.
func ResolveAPIKey() (string, error) {
	if v := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		p := filepath.Join(home, ".config", "chat-harness", "anthropic.key")
		if data, err := os.ReadFile(p); err == nil {
			if k := strings.TrimSpace(string(data)); k != "" {
				return k, nil
			}
		}
	}
	return "", errors.New("anthropic: no API key found (tried ANTHROPIC_API_KEY env and ~/.config/chat-harness/anthropic.key)")
}
