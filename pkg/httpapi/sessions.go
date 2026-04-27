package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
	"github.com/jpfielding/chat-harness-skel/pkg/session"
)

// SessionsHandler serves CRUD over /api/sessions. The `store` parameter is
// the authoritative Store (for list/create/delete); the Harness wraps a
// separate Binder for in-request session loading.
func SessionsHandler(store session.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions", handleCreateSession(store))
	mux.HandleFunc("GET /api/sessions", handleListSessions(store))
	mux.HandleFunc("GET /api/sessions/{id}", handleGetSession(store))
	mux.HandleFunc("POST /api/sessions/{id}/messages", handleAppendMessages(store))
	mux.HandleFunc("DELETE /api/sessions/{id}", handleDeleteSession(store))
	return mux
}

type createSessionBody struct {
	ID       string            `json:"id,omitempty"`
	System   string            `json:"system,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func handleCreateSession(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body createSessionBody
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON: " + err.Error()})
				return
			}
		}
		id := body.ID
		if id == "" {
			id = randomID()
		}
		s := &session.Session{ID: id, System: body.System, Metadata: body.Metadata}
		if err := store.Save(r.Context(), s); err != nil {
			writeError(w, err)
			return
		}
		got, err := store.Get(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, got)
	}
}

func handleListSessions(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts := session.ListOptions{}
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				opts.Limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				opts.Offset = n
			}
		}
		list, err := store.List(r.Context(), opts)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
	}
}

func handleGetSession(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		s, err := store.Get(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	}
}

type appendMessagesBody struct {
	ExpectedVersion int64          `json:"expected_version"`
	Messages        []chat.Message `json:"messages"`
}

func handleAppendMessages(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body appendMessagesBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON: " + err.Error()})
			return
		}
		newV, err := store.AppendConditional(r.Context(), id, body.ExpectedVersion, body.Messages...)
		if err != nil {
			if errors.Is(err, chat.ErrVersionConflict) {
				writeJSON(w, http.StatusConflict, errorResponse{
					Error: "version conflict",
					Kind:  "VersionConflict",
				})
				return
			}
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"version": newV})
	}
}

func handleDeleteSession(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := store.Delete(r.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// randomID returns a URL-safe random session id.
func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "sess_" + hex.EncodeToString(b[:])
}

// Silence unused-import in case ctx plumbing drops.
var _ = context.Background
