package chat

import (
	"fmt"
	"strings"
	"sync"
)

// ModelRef addresses a model as (Provider, Model). Always carry both; model
// names are not globally unique (Azure deployments, Bedrock ARNs, Ollama
// local aliases all overlap).
type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// String renders a ModelRef as "provider:model".
func (r ModelRef) String() string { return r.Provider + ":" + r.Model }

// ParseModelRef accepts "provider:model" and returns a ModelRef. The model
// portion may contain additional colons (e.g. "llama3.1:8b"); only the first
// colon is the provider separator.
func ParseModelRef(s string) (ModelRef, error) {
	i := strings.IndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return ModelRef{}, fmt.Errorf("invalid model ref %q: want provider:model", s)
	}
	return ModelRef{Provider: s[:i], Model: s[i+1:]}, nil
}

// Capabilities enumerates what a given model supports. The capability
// fingerprint in pkg/router uses these flags to filter candidates.
type Capabilities struct {
	Tools            bool `json:"tools"`
	ToolSchemaStrict bool `json:"tool_schema_strict"`
	ParallelTools    bool `json:"parallel_tools"`
	Vision           bool `json:"vision"`
	Audio            bool `json:"audio"`
	Streaming        bool `json:"streaming"`
	Thinking         bool `json:"thinking"`
	JSONObjectMode   bool `json:"json_object_mode"`
	JSONSchemaMode   bool `json:"json_schema_mode"`
	PromptCache      bool `json:"prompt_cache"`
}

// Pricing is per-million-token rates used for cost estimation. Rates not
// known for a model should be left zero; callers should treat a zero as
// "unknown" and not assume free.
type Pricing struct {
	InputPerM      float64 `json:"input_per_m"`
	OutputPerM     float64 `json:"output_per_m"`
	CacheReadPerM  float64 `json:"cache_read_per_m"`
	CacheWritePerM float64 `json:"cache_write_per_m"`
}

// ModelInfo describes a model registered in the catalog.
type ModelInfo struct {
	Ref           ModelRef     `json:"ref"`
	NativeID      string       `json:"native_id,omitempty"` // provider-native id on the wire; defaults to Ref.Model
	ContextTokens int          `json:"context_tokens"`
	MaxOutput     int          `json:"max_output"`
	Capabilities  Capabilities `json:"capabilities"`
	Pricing       Pricing      `json:"pricing"`
}

// Native returns the id to use on the wire with the provider.
func (m ModelInfo) Native() string {
	if m.NativeID != "" {
		return m.NativeID
	}
	return m.Ref.Model
}

// Catalog is a registry of ModelInfo. Concurrent safe.
type Catalog struct {
	mu    sync.RWMutex
	byRef map[string]ModelInfo // keyed by ModelRef.String()
}

// NewCatalog returns an empty Catalog.
func NewCatalog() *Catalog {
	return &Catalog{byRef: make(map[string]ModelInfo)}
}

// Register adds or replaces m in the catalog.
func (c *Catalog) Register(m ModelInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byRef[m.Ref.String()] = m
}

// Lookup returns the ModelInfo for ref, or (zero, false) if absent.
func (c *Catalog) Lookup(ref ModelRef) (ModelInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.byRef[ref.String()]
	return m, ok
}

// List returns all registered ModelInfo in an unspecified order.
func (c *Catalog) List() []ModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ModelInfo, 0, len(c.byRef))
	for _, m := range c.byRef {
		out = append(out, m)
	}
	return out
}
