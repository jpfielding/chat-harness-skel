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

// FallbackPolicy controls the Harness's fallback executor. Empty
// FallbackOnKinds disables fallback (only the first candidate is tried).
type FallbackPolicy struct {
	// FallbackOnKinds lists ErrorKinds that should trigger trying the next
	// candidate. Any other error halts immediately.
	FallbackOnKinds map[ErrorKind]bool
	// MaxAttempts caps the number of candidates tried per request. 0 = no cap.
	MaxAttempts int
	// PerAttemptTimeout, if set, wraps each candidate's provider call in
	// context.WithTimeout. 0 = no per-attempt timeout.
	PerAttemptTimeout time.Duration
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
	fallback  FallbackPolicy
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

// WithFallback sets the FallbackPolicy. Without this option, fallback is
// disabled (only the first router candidate is tried).
func WithFallback(fp FallbackPolicy) HarnessOption {
	return func(h *Harness) { h.fallback = fp }
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

	candidates, err := h.pickCandidates(ctx, req)
	if err != nil {
		return Response{}, err
	}

	resp, attempts, err := h.runSendFallback(ctx, req, candidates)
	if err != nil {
		return Response{Attempts: attempts}, err
	}
	resp.Attempts = attempts

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

	candidates, err := h.pickCandidates(ctx, req)
	if err != nil {
		return nil, err
	}
	// Streaming fallback is handshake-only: we may try multiple candidates
	// while Stream() itself returns sync errors, but once a reader is
	// returned (bytes possibly in flight), no further fallback.
	for _, ref := range candidates {
		prov, ok := h.providers[ref.Provider]
		if !ok {
			continue
		}
		attemptReq := req
		attemptReq.Model = ref.String()
		reader, err := prov.Stream(ctx, attemptReq)
		if err == nil {
			return reader, nil
		}
		if !h.shouldFallback(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("all stream candidates failed")
}

// pickCandidates resolves req to an ordered list of ModelRefs.
// An explicit req.Model disables fallback (single-element list).
// When the router is unset and req.Model is empty, the call fails.
func (h *Harness) pickCandidates(ctx context.Context, req Request) ([]ModelRef, error) {
	if req.Model != "" {
		ref, err := ParseModelRef(req.Model)
		if err != nil {
			return nil, err
		}
		return []ModelRef{ref}, nil
	}
	if h.router == nil {
		return nil, fmt.Errorf("no router configured and req.Model is empty")
	}
	refs, err := h.router.Pick(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("router returned no candidates")
	}
	return refs, nil
}

// runSendFallback iterates candidates, stopping at the first success or a
// non-fallbackable error. Returns the successful Response along with the
// Attempts log (including any failures that preceded success). The max-
// attempts cap, per-attempt timeout, and AfterOutput gating all apply.
func (h *Harness) runSendFallback(ctx context.Context, req Request, candidates []ModelRef) (Response, []Attempt, error) {
	max := h.fallback.MaxAttempts
	if max == 0 || max > len(candidates) {
		max = len(candidates)
	}
	var attempts []Attempt
	var lastErr error
	for i := 0; i < max; i++ {
		ref := candidates[i]
		prov, ok := h.providers[ref.Provider]
		if !ok {
			lastErr = fmt.Errorf("provider %q not registered", ref.Provider)
			attempts = append(attempts, Attempt{
				Provider: ref.Provider,
				Model:    ref.Model,
				Error:    &ProviderError{Kind: ErrKindNotFound, Provider: ref.Provider, Model: ref.Model, Message: "provider not registered"},
			})
			continue
		}

		attemptCtx := ctx
		var cancel context.CancelFunc
		if h.fallback.PerAttemptTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, h.fallback.PerAttemptTimeout)
		}
		attemptReq := req
		attemptReq.Model = ref.String()

		start := h.now()
		resp, err := prov.Send(attemptCtx, attemptReq)
		if cancel != nil {
			cancel()
		}
		latency := h.now().Sub(start)

		att := Attempt{
			Provider: ref.Provider,
			Model:    ref.Model,
			Latency:  latency,
		}
		if err != nil {
			var pe *ProviderError
			if errors.As(err, &pe) {
				att.Error = pe
				att.RequestID = pe.RequestID
			} else {
				att.Error = &ProviderError{Kind: ErrKindUnknown, Provider: ref.Provider, Model: ref.Model, Err: err}
			}
			attempts = append(attempts, att)
			lastErr = err

			if att.Error != nil && att.Error.AfterOutput {
				// Cannot fall back safely once bytes were emitted.
				return Response{}, attempts, err
			}
			if !h.shouldFallback(err) {
				return Response{}, attempts, err
			}
			// For context_length: only fall back to a strictly larger context window.
			if pe != nil && pe.Kind == ErrKindContextLength {
				candidates = h.filterLargerContext(pe.Model, candidates[i+1:])
				// Reset the loop: we've rewritten the list ahead, adjust i and max.
				candidates = append([]ModelRef{}, candidates...) // defensive copy
				// Rebuild the loop shape.
				next := []ModelRef{}
				next = append(next, candidates...)
				candidates = next
				i = -1
				max = len(candidates)
				if max == 0 {
					return Response{}, attempts, err
				}
				continue
			}
			continue
		}

		if resp.Latency == 0 {
			resp.Latency = latency
		}
		attempts = append(attempts, att)
		resp.Ref = ref
		return resp, attempts, nil
	}
	return Response{}, attempts, lastErr
}

func (h *Harness) shouldFallback(err error) bool {
	if h.fallback.FallbackOnKinds == nil {
		return false
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		return false
	}
	return h.fallback.FallbackOnKinds[pe.Kind]
}

// filterLargerContext returns the subset of remaining candidates whose
// catalog ContextTokens strictly exceeds the failed model's. If the
// catalog has no data for either side, the candidate is kept (config
// is authoritative).
func (h *Harness) filterLargerContext(failedModel string, remaining []ModelRef) []ModelRef {
	var failedCtx int
	for _, mi := range h.catalog.List() {
		if mi.Ref.Model == failedModel {
			failedCtx = mi.ContextTokens
			break
		}
	}
	if failedCtx == 0 {
		return remaining
	}
	out := make([]ModelRef, 0, len(remaining))
	for _, ref := range remaining {
		info, ok := h.catalog.Lookup(ref)
		if !ok || info.ContextTokens == 0 || info.ContextTokens > failedCtx {
			out = append(out, ref)
		}
	}
	return out
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
