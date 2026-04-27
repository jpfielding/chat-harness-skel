package router

import (
	"context"
	"fmt"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// Policy is a named, ordered list of candidate ModelRefs.
type Policy struct {
	Name       string
	Candidates []chat.ModelRef
}

// PolicyRouter implements chat.Router by matching req.Policy (or a default)
// against a set of named policies. It filters candidates using a capability
// fingerprint so the Harness never dispatches to a model that cannot serve
// the request.
//
// PolicyRouter is safe for concurrent use after construction.
type PolicyRouter struct {
	policies map[string]*Policy
	def      string
	catalog  *chat.Catalog
}

// NewPolicyRouter constructs a router from a list of Policies and the
// provided catalog. def is the default policy name used when req.Policy is
// empty. Returns an error if def is set but missing from policies, or if
// any policy is empty.
func NewPolicyRouter(catalog *chat.Catalog, def string, policies []Policy) (*PolicyRouter, error) {
	r := &PolicyRouter{policies: map[string]*Policy{}, catalog: catalog, def: def}
	for i := range policies {
		p := policies[i]
		if p.Name == "" {
			return nil, fmt.Errorf("policy %d has empty name", i)
		}
		if len(p.Candidates) == 0 {
			return nil, fmt.Errorf("policy %q has no candidates", p.Name)
		}
		r.policies[p.Name] = &p
	}
	if def != "" {
		if _, ok := r.policies[def]; !ok {
			return nil, fmt.Errorf("default policy %q not registered", def)
		}
	}
	return r, nil
}

// Pick implements chat.Router.
func (r *PolicyRouter) Pick(ctx context.Context, req chat.Request) ([]chat.ModelRef, error) {
	// Explicit model → single candidate, no fallback.
	if req.Model != "" {
		ref, err := chat.ParseModelRef(req.Model)
		if err != nil {
			return nil, err
		}
		return []chat.ModelRef{ref}, nil
	}

	name := req.Policy
	if name == "" {
		name = r.def
	}
	p, ok := r.policies[name]
	if !ok {
		return nil, fmt.Errorf("policy %q not found", name)
	}

	rc := Required(req)

	out := make([]chat.ModelRef, 0, len(p.Candidates))
	for _, cand := range p.Candidates {
		if r.catalog != nil {
			if info, ok := r.catalog.Lookup(cand); ok {
				if !rc.Satisfies(info) {
					continue
				}
			}
			// Unknown models are included (config authority wins over catalog absence).
		}
		out = append(out, cand)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("policy %q: no candidate satisfies request capabilities", name)
	}
	return out, nil
}
