package chat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Router picks an ordered list of candidate models to try for a Request.
// The harness dispatches to each in order until one succeeds or the list is
// exhausted. Implementations live in pkg/router.
type Router interface {
	Pick(ctx context.Context, req Request) ([]ModelRef, error)
}

// SessionBinder is the narrow interface the harness needs from a session
// store. It is defined here rather than importing pkg/session to keep the
// dependency direction clean (pkg/session depends on pkg/chat, not the
// other way around).
type SessionBinder interface {
	// Load returns (system, messages, version, nil) for the session, or
	// (_, _, _, ErrNotFound) if absent.
	Load(ctx context.Context, id string) (system string, msgs []Message, version int64, err error)

	// Append appends newMsgs to the session identified by id, requiring the
	// session's current Version to equal expectedVersion (optimistic
	// concurrency). Returns the new Version.
	Append(ctx context.Context, id string, expectedVersion int64, newMsgs ...Message) (newVersion int64, err error)
}

// Harness is the top-level entry point. It wires providers, the router, the
// catalog, and (optionally) a session store into one Send/Stream surface.
//
// Construct with New(...). Harness is safe for concurrent use once built.
type Harness struct {
	providers map[string]Provider
	router    Router
	catalog   *Catalog
	sessions  SessionBinder
	logger    *slog.Logger
	now       func() time.Time
}

// HarnessOption configures a Harness.
type HarnessOption func(*Harness)

// WithProvider registers p under its Name(). Later registrations replace
// earlier ones.
func WithProvider(p Provider) HarnessOption {
	return func(h *Harness) { h.providers[p.Name()] = p }
}

// WithRouter sets the router. If unset, the harness requires req.Model to
// be a resolvable ModelRef and performs no fallback.
func WithRouter(r Router) HarnessOption {
	return func(h *Harness) { h.router = r }
}

// WithCatalog sets the model catalog. If unset, a catalog is synthesized
// from each registered Provider's Models().
func WithCatalog(c *Catalog) HarnessOption {
	return func(h *Harness) { h.catalog = c }
}

// WithLogger sets the slog logger. If unset, a discard logger is used.
func WithLogger(l *slog.Logger) HarnessOption {
	return func(h *Harness) { h.logger = l }
}

// WithSessions wires a SessionBinder. If unset, the harness rejects
// requests that specify a SessionID.
func WithSessions(b SessionBinder) HarnessOption {
	return func(h *Harness) { h.sessions = b }
}

// Sessions returns the bound SessionBinder, or nil if none.
func (h *Harness) Sessions() SessionBinder { return h.sessions }

// New constructs a Harness.
func New(opts ...HarnessOption) *Harness {
	h := &Harness{
		providers: make(map[string]Provider),
		catalog:   NewCatalog(),
		logger:    slog.New(slog.NewTextHandler(discardWriter{}, nil)),
		now:       time.Now,
	}
	for _, o := range opts {
		o(h)
	}
	// If the caller didn't provide a catalog, seed it from providers.
	if len(h.catalog.byRef) == 0 {
		for _, p := range h.providers {
			for _, m := range p.Models() {
				h.catalog.Register(m)
			}
		}
	}
	return h
}

// Catalog returns the model catalog. Useful for the /api/models endpoint.
func (h *Harness) Catalog() *Catalog { return h.catalog }

// Providers returns a snapshot of registered provider names.
func (h *Harness) Providers() []string {
	out := make([]string, 0, len(h.providers))
	for name := range h.providers {
		out = append(out, name)
	}
	return out
}

// Send performs a non-streaming chat turn. If req.SessionID is set, the
// harness loads the session's accumulated messages, prepends them to
// req.Messages (caller messages represent only *new* turns), dispatches,
// and appends (user_input..., assistant_reply) on success via optimistic
// concurrency.
func (h *Harness) Send(ctx context.Context, req Request) (Response, error) {
	if err := Validate(req); err != nil {
		return Response{}, err
	}

	var expectedVersion int64
	newUserMsgs := req.Messages
	if req.SessionID != "" {
		if h.sessions == nil {
			return Response{}, errors.New("session_id provided but no session store configured")
		}
		sys, prior, ver, err := h.sessions.Load(ctx, req.SessionID)
		if err != nil {
			return Response{}, err
		}
		expectedVersion = ver
		if sys != "" && req.System == "" {
			req.System = sys
		}
		// Merge: prior session history first, then caller-provided new msgs.
		merged := make([]Message, 0, len(prior)+len(req.Messages))
		merged = append(merged, prior...)
		merged = append(merged, req.Messages...)
		req.Messages = merged
	}

	ref, err := h.pickSingle(req)
	if err != nil {
		return Response{}, err
	}
	prov, ok := h.providers[ref.Provider]
	if !ok {
		return Response{}, fmt.Errorf("provider %q not registered", ref.Provider)
	}
	req.Model = ref.String()

	start := h.now()
	resp, err := prov.Send(ctx, req)
	if err != nil {
		return Response{}, err
	}
	if resp.Latency == 0 {
		resp.Latency = h.now().Sub(start)
	}

	if req.SessionID != "" {
		toAppend := append(append([]Message{}, newUserMsgs...), resp.Message)
		if _, err := h.sessions.Append(ctx, req.SessionID, expectedVersion, toAppend...); err != nil {
			// The turn succeeded with the provider but we couldn't persist.
			// Surface that explicitly so the caller knows their session is
			// out-of-sync.
			return resp, fmt.Errorf("session append failed after successful turn: %w", err)
		}
	}
	return resp, nil
}

// Stream performs a streaming chat turn. Session merging works like Send,
// but because we only know the assistant's final reply once the stream
// completes, session append is the caller's responsibility for now —
// the HTTP layer does it via a buffering stream wrapper in pkg/httpapi.
func (h *Harness) Stream(ctx context.Context, req Request) (StreamReader, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if req.SessionID != "" {
		if h.sessions == nil {
			return nil, errors.New("session_id provided but no session store configured")
		}
		sys, prior, _, err := h.sessions.Load(ctx, req.SessionID)
		if err != nil {
			return nil, err
		}
		if sys != "" && req.System == "" {
			req.System = sys
		}
		merged := make([]Message, 0, len(prior)+len(req.Messages))
		merged = append(merged, prior...)
		merged = append(merged, req.Messages...)
		req.Messages = merged
	}

	ref, err := h.pickSingle(req)
	if err != nil {
		return nil, err
	}
	prov, ok := h.providers[ref.Provider]
	if !ok {
		return nil, fmt.Errorf("provider %q not registered", ref.Provider)
	}
	req.Model = ref.String()
	return prov.Stream(ctx, req)
}

// pickSingle resolves req to a single ModelRef. It honors an explicit
// req.Model ref, falling back to the router only when req.Model is empty.
func (h *Harness) pickSingle(req Request) (ModelRef, error) {
	if req.Model != "" {
		return ParseModelRef(req.Model)
	}
	if h.router == nil {
		return ModelRef{}, fmt.Errorf("no router configured and req.Model is empty")
	}
	refs, err := h.router.Pick(context.Background(), req)
	if err != nil {
		return ModelRef{}, err
	}
	if len(refs) == 0 {
		return ModelRef{}, fmt.Errorf("router returned no candidates")
	}
	return refs[0], nil
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
