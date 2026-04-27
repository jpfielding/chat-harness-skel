package chat

import "context"

// Provider is the adapter contract implemented by each backend (Anthropic,
// OpenAI, Ollama, ...). Intentionally narrow: capability flags live on
// ModelInfo, not as extra interface methods.
type Provider interface {
	// Name returns the stable provider identifier used in ModelRef.Provider
	// and in TOML config (e.g. "anthropic", "openai", "ollama").
	Name() string

	// Models returns a seed catalog of the models this provider knows how to
	// serve. The authoritative catalog lives in config; Models() is used for
	// sane defaults and for startup validation.
	Models() []ModelInfo

	// Send dispatches a non-streaming request and returns a fully-formed
	// Response. Errors MUST be *ProviderError.
	Send(ctx context.Context, req Request) (Response, error)

	// Stream dispatches a streaming request. Errors returned synchronously
	// MUST be *ProviderError. Errors observed mid-stream are delivered via
	// StreamEvent.Err with Kind = EvError; the next Next() call returns
	// io.EOF.
	Stream(ctx context.Context, req Request) (StreamReader, error)
}
