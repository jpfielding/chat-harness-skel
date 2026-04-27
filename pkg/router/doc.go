// Package router implements chat.Router: a declarative, TOML-driven policy
// matcher that selects an ordered list of candidate ModelRefs for a Request.
// Candidates are filtered by a capability fingerprint so that the harness's
// fallback executor never tries a model that cannot serve the request.
//
// Status: experimental.
package router
