package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

const (
	DefaultBaseURL = "http://localhost:11434"
	providerName   = "ollama"
)

// Config configures the Ollama provider.
type Config struct {
	BaseURL    string
	HTTPClient *http.Client
	Now        func() time.Time
	// SupportsTools overrides the version probe. Useful for tests against a
	// fake server. If nil, the probe result is used.
	SupportsTools *bool
}

// Provider is a chat.Provider for Ollama.
type Provider struct {
	cfg           Config
	supportsTools bool
}

// New constructs a Provider. It attempts a best-effort /api/version probe
// to determine tool support. If the probe fails or returns a version older
// than 0.3.0, tools are disabled — no simulated fallback.
func New(cfg Config) (*Provider, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	p := &Provider{cfg: cfg}
	if cfg.SupportsTools != nil {
		p.supportsTools = *cfg.SupportsTools
	} else {
		p.supportsTools = p.probeTools()
	}
	return p, nil
}

// Name returns the provider id.
func (p *Provider) Name() string { return providerName }

// Models returns a seed catalog. Ollama models are user-local; the
// authoritative list should come from config.
func (p *Provider) Models() []chat.ModelInfo {
	caps := chat.Capabilities{
		Streaming:     true,
		Tools:         p.supportsTools,
		ParallelTools: p.supportsTools,
	}
	return []chat.ModelInfo{
		{Ref: chat.ModelRef{Provider: providerName, Model: "llama3.1:8b"}, ContextTokens: 128000, MaxOutput: 4096, Capabilities: caps},
		{Ref: chat.ModelRef{Provider: providerName, Model: "llama3.1:70b"}, ContextTokens: 128000, MaxOutput: 4096, Capabilities: caps},
		{Ref: chat.ModelRef{Provider: providerName, Model: "qwen2.5:7b"}, ContextTokens: 32000, MaxOutput: 4096, Capabilities: caps},
	}
}

// SupportsTools reports whether the connected Ollama version advertises
// tool-call support.
func (p *Provider) SupportsTools() bool { return p.supportsTools }

// probeTools queries /api/version and returns true if the server version
// is >= 0.3.0. Failures return false; the provider remains usable for
// text-only requests.
func (p *Provider) probeTools() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.BaseURL+"/api/version", nil)
	if err != nil {
		return false
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return false
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return false
	}
	return versionGTE(v.Version, 0, 3, 0)
}

func versionGTE(ver string, major, minor, patch int) bool {
	parts := strings.Split(ver, ".")
	if len(parts) < 3 {
		return false
	}
	pMaj, _ := strconv.Atoi(parts[0])
	pMin, _ := strconv.Atoi(parts[1])
	// Trim pre-release suffixes from patch.
	pp := parts[2]
	if i := strings.IndexAny(pp, "-+"); i >= 0 {
		pp = pp[:i]
	}
	pPat, _ := strconv.Atoi(pp)
	switch {
	case pMaj != major:
		return pMaj > major
	case pMin != minor:
		return pMin > minor
	default:
		return pPat >= patch
	}
}

// Send issues a non-streaming request to /api/chat.
func (p *Provider) Send(ctx context.Context, req chat.Request) (chat.Response, error) {
	ref, err := chat.ParseModelRef(req.Model)
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindInvalidRequest, 0, "", false, err)
	}
	if len(req.Tools) > 0 && !p.supportsTools {
		return chat.Response{}, &chat.ProviderError{
			Kind:     chat.ErrKindToolsUnsupported,
			Provider: providerName,
			Model:    ref.Model,
			Message:  "ollama server version does not support tool calls",
		}
	}
	body, err := buildChatRequest(req, ref, false)
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindInvalidRequest, 0, ref.Model, false, err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindInvalidRequest, 0, ref.Model, false, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/api/chat", bytes.NewReader(raw))
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindUnknown, 0, ref.Model, false, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	start := p.cfg.Now()
	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return chat.Response{}, p.wrapErr(classifyTransport(ctx, err), 0, ref.Model, false, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindUnknown, resp.StatusCode, ref.Model, false, err)
	}
	if resp.StatusCode >= 400 {
		return chat.Response{}, &chat.ProviderError{
			Kind:       classifyStatus(resp.StatusCode, respBody),
			Provider:   providerName,
			Model:      ref.Model,
			StatusCode: resp.StatusCode,
			Message:    snippet(respBody),
		}
	}
	var wire wireChatResponse
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindUnknown, resp.StatusCode, ref.Model, false, err)
	}
	out := convertResponse(ref, wire)
	out.Latency = p.cfg.Now().Sub(start)
	return out, nil
}

// Stream: Phase 4 ships text streaming. Tool-call streaming is gated on
// supportsTools; if tools are used the implementation falls back to a
// synthesized single-delta shape from Ollama's final chunk.
func (p *Provider) Stream(ctx context.Context, req chat.Request) (chat.StreamReader, error) {
	ref, err := chat.ParseModelRef(req.Model)
	if err != nil {
		return nil, p.wrapErr(chat.ErrKindInvalidRequest, 0, "", false, err)
	}
	if len(req.Tools) > 0 && !p.supportsTools {
		return nil, &chat.ProviderError{
			Kind: chat.ErrKindToolsUnsupported, Provider: providerName, Model: ref.Model,
			Message: "ollama server version does not support tool calls",
		}
	}
	body, err := buildChatRequest(req, ref, true)
	if err != nil {
		return nil, p.wrapErr(chat.ErrKindInvalidRequest, 0, ref.Model, false, err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, p.wrapErr(chat.ErrKindInvalidRequest, 0, ref.Model, false, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/api/chat", bytes.NewReader(raw))
	if err != nil {
		return nil, p.wrapErr(chat.ErrKindUnknown, 0, ref.Model, false, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, p.wrapErr(classifyTransport(ctx, err), 0, ref.Model, false, err)
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &chat.ProviderError{
			Kind:       classifyStatus(resp.StatusCode, b),
			Provider:   providerName,
			Model:      ref.Model,
			StatusCode: resp.StatusCode,
			Message:    snippet(b),
		}
	}
	return newStreamReader(ctx, ref, resp), nil
}

func (p *Provider) wrapErr(kind chat.ErrorKind, status int, model string, afterOutput bool, err error) error {
	return &chat.ProviderError{
		Kind: kind, Provider: providerName, Model: model, StatusCode: status, AfterOutput: afterOutput, Err: err,
	}
}

func classifyStatus(status int, body []byte) chat.ErrorKind {
	switch {
	case status == http.StatusNotFound:
		return chat.ErrKindNotFound
	case status == http.StatusTooManyRequests:
		return chat.ErrKindRateLimit
	case status == http.StatusServiceUnavailable:
		return chat.ErrKindOverloaded
	case status == http.StatusBadRequest:
		lb := bytes.ToLower(body)
		if bytes.Contains(lb, []byte("context length")) || bytes.Contains(lb, []byte("token limit")) {
			return chat.ErrKindContextLength
		}
		return chat.ErrKindInvalidRequest
	case status >= 500:
		return chat.ErrKindServerError
	}
	return chat.ErrKindUnknown
}

func classifyTransport(ctx context.Context, err error) chat.ErrorKind {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return chat.ErrKindTimeout
		}
		return chat.ErrKindCanceled
	}
	return chat.ErrKindServerError
}

func snippet(body []byte) string {
	const max = 512
	if len(body) <= max {
		return string(body)
	}
	return string(body[:max]) + "…"
}

var _ = fmt.Sprintf
