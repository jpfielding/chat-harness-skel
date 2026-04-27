package chat

import (
	"context"
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

// Harness is the top-level entry point. It wires providers, the router, the
// catalog, and (optionally) a session store into one Send/Stream surface.
//
// Construct with New(...). Harness is safe for concurrent use once built.
type Harness struct {
	providers map[string]Provider
	router    Router
	catalog   *Catalog
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

// Send performs a non-streaming chat turn. Phase-0 skeleton: it validates,
// resolves the model, and dispatches to exactly one provider. Router-driven
// fallback is wired in Phase 4.
func (h *Harness) Send(ctx context.Context, req Request) (Response, error) {
	if err := Validate(req); err != nil {
		return Response{}, err
	}
	ref, err := h.pickSingle(req)
	if err != nil {
		return Response{}, err
	}
	prov, ok := h.providers[ref.Provider]
	if !ok {
		return Response{}, fmt.Errorf("provider %q not registered", ref.Provider)
	}
	// Hand the provider the resolved ModelRef by stamping req.Model.
	req.Model = ref.String()
	start := h.now()
	resp, err := prov.Send(ctx, req)
	if err != nil {
		return Response{}, err
	}
	if resp.Latency == 0 {
		resp.Latency = h.now().Sub(start)
	}
	return resp, nil
}

// Stream performs a streaming chat turn. Phase-0 skeleton mirrors Send.
func (h *Harness) Stream(ctx context.Context, req Request) (StreamReader, error) {
	if err := Validate(req); err != nil {
		return nil, err
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
