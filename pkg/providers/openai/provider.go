package openai

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
	DefaultBaseURL = "https://api.openai.com"
	providerName   = "openai"
)

// Config configures the OpenAI provider.
type Config struct {
	APIKey     string
	BaseURL    string       // optional override; defaults to DefaultBaseURL
	OrgID      string       // optional; sent as OpenAI-Organization
	HTTPClient *http.Client // optional; defaults to a client with a 2m timeout
	Now        func() time.Time
}

// Provider is a chat.Provider for OpenAI.
type Provider struct {
	cfg Config
}

// New constructs a Provider.
func New(cfg Config) (*Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("openai: APIKey required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
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

// Models returns a small seed catalog.
func (p *Provider) Models() []chat.ModelInfo {
	return []chat.ModelInfo{
		{
			Ref:           chat.ModelRef{Provider: providerName, Model: "gpt-5"},
			ContextTokens: 400000,
			MaxOutput:     100000,
			Capabilities: chat.Capabilities{
				Tools: true, ToolSchemaStrict: true, ParallelTools: true, Vision: true,
				Streaming: true, JSONObjectMode: true, JSONSchemaMode: true,
			},
		},
		{
			Ref:           chat.ModelRef{Provider: providerName, Model: "gpt-5-mini"},
			ContextTokens: 400000,
			MaxOutput:     100000,
			Capabilities: chat.Capabilities{
				Tools: true, ToolSchemaStrict: true, ParallelTools: true, Vision: true,
				Streaming: true, JSONObjectMode: true, JSONSchemaMode: true,
			},
		},
		{
			Ref:           chat.ModelRef{Provider: providerName, Model: "gpt-5-nano"},
			ContextTokens: 400000,
			MaxOutput:     100000,
			Capabilities: chat.Capabilities{
				Tools: true, ParallelTools: true, Streaming: true, JSONObjectMode: true,
			},
		},
	}
}

// Send issues a non-streaming request to /v1/chat/completions.
func (p *Provider) Send(ctx context.Context, req chat.Request) (chat.Response, error) {
	ref, err := chat.ParseModelRef(req.Model)
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindInvalidRequest, 0, "", false, err)
	}

	body, err := buildChatRequest(req, ref)
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindInvalidRequest, 0, ref.Model, false, err)
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindInvalidRequest, 0, ref.Model, false, err)
	}

	url := p.cfg.BaseURL + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindUnknown, 0, ref.Model, false, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	if p.cfg.OrgID != "" {
		httpReq.Header.Set("OpenAI-Organization", p.cfg.OrgID)
	}

	start := p.cfg.Now()
	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return chat.Response{}, p.wrapErr(classifyTransport(ctx, err), 0, ref.Model, false, err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindUnknown, resp.StatusCode, ref.Model, false, err)
	}
	requestID := resp.Header.Get("x-request-id")

	if resp.StatusCode >= 400 {
		return chat.Response{}, &chat.ProviderError{
			Kind:       classifyStatus(resp.StatusCode, rawBody),
			Provider:   providerName,
			Model:      ref.Model,
			StatusCode: resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("retry-after")),
			RequestID:  requestID,
			Message:    snippet(rawBody),
		}
	}

	var wire wireChatCompletionResponse
	if err := json.Unmarshal(rawBody, &wire); err != nil {
		return chat.Response{}, p.wrapErr(chat.ErrKindUnknown, resp.StatusCode, ref.Model, false, err)
	}
	out := convertResponse(ref, wire)
	out.Latency = p.cfg.Now().Sub(start)
	return out, nil
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

func classifyTransport(ctx context.Context, err error) chat.ErrorKind {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return chat.ErrKindTimeout
		}
		return chat.ErrKindCanceled
	}
	return chat.ErrKindServerError
}

func looksLikeContextOverflow(body []byte) bool {
	lb := bytes.ToLower(body)
	return bytes.Contains(lb, []byte("context_length_exceeded")) ||
		bytes.Contains(lb, []byte("maximum context length")) ||
		bytes.Contains(lb, []byte("too long"))
}

func parseRetryAfter(s string) time.Duration {
	if s == "" {
		return 0
	}
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
