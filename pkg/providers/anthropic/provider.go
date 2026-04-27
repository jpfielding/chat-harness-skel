package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

const (
	// DefaultBaseURL is the production Anthropic API root.
	DefaultBaseURL = "https://api.anthropic.com"
	// DefaultAPIVersion is the anthropic-version header value.
	DefaultAPIVersion = "2023-06-01"
	// providerName is the stable identifier used in ModelRef.Provider.
	providerName = "anthropic"
)

// Config configures the Anthropic provider.
type Config struct {
	APIKey     string        // required (or loaded via ResolveAPIKey)
	BaseURL    string        // optional override; defaults to DefaultBaseURL
	APIVersion string        // optional override; defaults to DefaultAPIVersion
	HTTPClient *http.Client  // optional; defaults to a client with a 2m timeout
	Now        func() time.Time
}

// Provider is a chat.Provider for Anthropic.
type Provider struct {
	cfg Config
}

// New constructs a Provider. APIKey is required.
func New(cfg Config) (*Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic: APIKey required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.APIVersion == "" {
		cfg.APIVersion = DefaultAPIVersion
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Provider{cfg: cfg}, nil
}

// Name returns the provider id.
func (p *Provider) Name() string { return providerName }

// Models returns a small seed catalog; authoritative catalog is user config.
func (p *Provider) Models() []chat.ModelInfo {
	return []chat.ModelInfo{
		{
			Ref:           chat.ModelRef{Provider: providerName, Model: "claude-opus-4-5"},
			ContextTokens: 200000,
			MaxOutput:     64000,
			Capabilities: chat.Capabilities{
				Tools: true, ParallelTools: true, Vision: true, Streaming: true,
				Thinking: true, PromptCache: true, JSONSchemaMode: true,
			},
		},
		{
			Ref:           chat.ModelRef{Provider: providerName, Model: "claude-sonnet-4-5"},
			ContextTokens: 200000,
			MaxOutput:     64000,
			Capabilities: chat.Capabilities{
				Tools: true, ParallelTools: true, Vision: true, Streaming: true, PromptCache: true,
			},
		},
		{
			Ref:           chat.ModelRef{Provider: providerName, Model: "claude-haiku-4-5"},
			ContextTokens: 200000,
			MaxOutput:     64000,
			Capabilities: chat.Capabilities{
				Tools: true, ParallelTools: true, Vision: true, Streaming: true, PromptCache: true,
			},
		},
	}
}

// Send issues a non-streaming request to /v1/messages and returns a
// normalized Response.
func (p *Provider) Send(ctx context.Context, req chat.Request) (chat.Response, error) {
	ref, err := chat.ParseModelRef(req.Model)
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindInvalidRequest, 0, "", false, err)
	}

	body, err := buildMessagesRequest(req, ref)
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindInvalidRequest, 0, ref.Model, false, err)
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindInvalidRequest, 0, ref.Model, false, err)
	}

	url := p.cfg.BaseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindUnknown, 0, ref.Model, false, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", p.cfg.APIVersion)

	start := p.cfg.Now()
	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		kind := classifyTransportError(ctx, err)
		return chat.Response{}, p.wrapErr(kind, 0, ref.Model, false, err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindUnknown, resp.StatusCode, ref.Model, false, err)
	}
	requestID := resp.Header.Get("request-id")

	if resp.StatusCode >= 400 {
		kind := classifyStatus(resp.StatusCode, rawBody)
		retryAfter := parseRetryAfter(resp.Header.Get("retry-after"))
		return chat.Response{}, &chat.ProviderError{
			Kind:       kind,
			Provider:   providerName,
			Model:      ref.Model,
			StatusCode: resp.StatusCode,
			RetryAfter: retryAfter,
			RequestID:  requestID,
			Message:    snippet(rawBody),
		}
	}

	var wire wireMessagesResponse
	if err := json.Unmarshal(rawBody, &wire); err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindUnknown, resp.StatusCode, ref.Model, false, err)
	}
	out := convertResponse(ref, wire)
	out.Latency = p.cfg.Now().Sub(start)
	return out, nil
}

// Stream is not yet implemented; returns a ProviderError of kind Unknown.
// Phase 2 implements the SSE path.
func (p *Provider) Stream(ctx context.Context, req chat.Request) (chat.StreamReader, error) {
	return nil, &chat.ProviderError{
		Kind:     chat.ErrKindUnknown,
		Provider: providerName,
		Message:  "Stream not implemented in Phase 1",
	}
}

func (p *Provider) wrapErr(kind chat.ErrorKind, status int, model string, afterOutput bool, err error) error {
	return &chat.ProviderError{
		Kind:        kind,
		Provider:    providerName,
		Model:       model,
		StatusCode:  status,
		AfterOutput: afterOutput,
		Err:         err,
	}
}

func classifyStatus(status int, body []byte) chat.ErrorKind {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return chat.ErrKindAuthFailed
	case http.StatusNotFound:
		return chat.ErrKindNotFound
	case http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		if looksLikeContextOverflow(body) {
			return chat.ErrKindContextLength
		}
		return chat.ErrKindInvalidRequest
	case http.StatusTooManyRequests:
		return chat.ErrKindRateLimit
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return chat.ErrKindTimeout
	case http.StatusServiceUnavailable:
		return chat.ErrKindOverloaded
	case http.StatusBadRequest:
		if looksLikeContextOverflow(body) {
			return chat.ErrKindContextLength
		}
		return chat.ErrKindInvalidRequest
	}
	if status >= 500 {
		return chat.ErrKindServerError
	}
	return chat.ErrKindUnknown
}

func classifyTransportError(ctx context.Context, err error) chat.ErrorKind {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return chat.ErrKindTimeout
		}
		return chat.ErrKindCanceled
	}
	return chat.ErrKindServerError
}

func looksLikeContextOverflow(body []byte) bool {
	// Anthropic returns error.message containing "context window" or
	// "too long" on overflow. Best-effort heuristic.
	lb := bytes.ToLower(body)
	return bytes.Contains(lb, []byte("context window")) ||
		bytes.Contains(lb, []byte("too long")) ||
		bytes.Contains(lb, []byte("prompt is too long"))
}

func parseRetryAfter(s string) time.Duration {
	if s == "" {
		return 0
	}
	// Anthropic returns seconds as an integer.
	var secs int
	if _, err := fmt.Sscanf(s, "%d", &secs); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

func snippet(body []byte) string {
	const max = 512
	if len(body) <= max {
		return string(body)
	}
	return string(body[:max]) + "…"
}
