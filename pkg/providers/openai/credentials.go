package openai

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ResolveAPIKey looks up the OpenAI API key in this order:
//   1. The OPENAI_API_KEY env var
//   2. ~/.config/chat-harness/openai.key (one line)
func ResolveAPIKey() (string, error) {
	if v := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		p := filepath.Join(home, ".config", "chat-harness", "openai.key")
		if data, err := os.ReadFile(p); err == nil {
			if k := strings.TrimSpace(string(data)); k != "" {
				return k, nil
			}
		}
	}
	return "", errors.New("openai: no API key found (tried OPENAI_API_KEY env and ~/.config/chat-harness/openai.key)")
}
