package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// newMux builds the HTTP mux with middleware. In Phase 0 every /api/* route
// returns 501 Not Implemented; Phase 1 wires real handlers.
func newMux(logger *slog.Logger, token string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"version": GitSHA,
		})
	})

	notImplemented := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not implemented in Phase 0", http.StatusNotImplemented)
	}
	mux.HandleFunc("GET /api/models", notImplemented)
	mux.HandleFunc("POST /api/chat", notImplemented)
	mux.HandleFunc("POST /api/chat/stream", notImplemented)
	mux.HandleFunc("POST /api/sessions", notImplemented)
	mux.HandleFunc("GET /api/sessions", notImplemented)
	mux.HandleFunc("GET /api/sessions/{id}", notImplemented)
	mux.HandleFunc("POST /api/sessions/{id}/messages", notImplemented)
	mux.HandleFunc("DELETE /api/sessions/{id}", notImplemented)

	return withCORS(withRequestLogging(logger, withAuth(token, mux)))
}
