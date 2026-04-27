package chat

// ThinkingConfig controls extended-thinking behavior on providers that
// support it (Anthropic today). Ignored by providers that do not.
type ThinkingConfig struct {
	Enabled      bool `json:"enabled"`
	BudgetTokens int  `json:"budget_tokens,omitempty"`
}

// GenerationParams are the provider-neutral knobs for generation. Fields
// that are pointers are treated as unset when nil; providers use their own
// defaults.
type GenerationParams struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Stop        []string `json:"stop,omitempty"`
	Seed        *int64   `json:"seed,omitempty"`

	// Thinking is honored only on providers with Capabilities.Thinking.
	Thinking *ThinkingConfig `json:"thinking,omitempty"`
}

// Request is a normalized chat request. Either Model or Policy should be set
// (or both; Model wins). Neither set falls back to the router's default policy.
type Request struct {
	// Model is a "provider:model" ref, or a bare alias the config resolves.
	// When set explicitly, fallback is disabled (caller opted out).
	Model string `json:"model,omitempty"`

	// Policy selects a named tier (e.g. "fast", "reasoning") from config.
	Policy string `json:"policy,omitempty"`

	Messages []Message `json:"messages"`

	// System is a convenience shortcut equivalent to a leading RoleSystem
	// message with one text block. If both System and a system-role message
	// are present, System is prepended.
	System string `json:"system,omitempty"`

	Tools      []ToolSpec  `json:"tools,omitempty"`
	ToolChoice *ToolChoice `json:"tool_choice,omitempty"`

	Params GenerationParams `json:"params,omitempty"`

	// SessionID, when set, causes the harness to load the session's messages
	// before dispatching, and append the assistant reply on success.
	SessionID string `json:"session_id,omitempty"`

	// Metadata is opaque provider-agnostic metadata (request id, tenant,
	// trace id). It is not forwarded to providers.
	Metadata map[string]string `json:"metadata,omitempty"`
}
