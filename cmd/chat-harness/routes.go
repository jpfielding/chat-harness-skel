package main

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jpfielding/chat-harness-skel/pkg/core"
	"github.com/jpfielding/chat-harness-skel/pkg/httpapi"
)

// newMux builds the HTTP mux with middleware. Wires every route the
// Service currently supports.
func newMux(logger *slog.Logger, token string, svc *core.Service) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"version": GitSHA,
		})
	})

	mux.Handle("GET /api/models", httpapi.ModelsHandler(svc.Harness))
	mux.Handle("POST /api/chat", httpapi.ChatHandler(svc.Harness))
	mux.Handle("POST /api/chat/stream", httpapi.StreamHandler(svc.Harness))

	// Session CRUD is served by its own sub-mux; attach each verb+path
	// explicitly so the outer mux's method routing still works.
	sess := httpapi.SessionsHandler(svc.Sessions)
	mux.Handle("POST /api/sessions", sess)
	mux.Handle("GET /api/sessions", sess)
	mux.Handle("GET /api/sessions/{id}", sess)
	mux.Handle("POST /api/sessions/{id}/messages", sess)
	mux.Handle("DELETE /api/sessions/{id}", sess)

	return withCORS(withRequestLogging(logger, withAuth(token, mux)))
}
