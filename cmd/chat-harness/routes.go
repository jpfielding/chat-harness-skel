package main

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
	"github.com/jpfielding/chat-harness-skel/pkg/httpapi"
)

// newMux builds the HTTP mux with middleware. Phase 1 wires /api/chat and
// /api/models. Streaming and sessions are Phase 2/3.
func newMux(logger *slog.Logger, token string, h *chat.Harness) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"version": GitSHA,
		})
	})

	mux.Handle("GET /api/models", httpapi.ModelsHandler(h))
	mux.Handle("POST /api/chat", httpapi.ChatHandler(h))

	notImplemented := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not implemented yet", http.StatusNotImplemented)
	}
	mux.HandleFunc("POST /api/chat/stream", notImplemented)
	mux.HandleFunc("POST /api/sessions", notImplemented)
	mux.HandleFunc("GET /api/sessions", notImplemented)
	mux.HandleFunc("GET /api/sessions/{id}", notImplemented)
	mux.HandleFunc("POST /api/sessions/{id}/messages", notImplemented)
	mux.HandleFunc("DELETE /api/sessions/{id}", notImplemented)

	return withCORS(withRequestLogging(logger, withAuth(token, mux)))
}
