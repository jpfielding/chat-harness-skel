package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// ChatHandler returns an http.Handler that serves POST /api/chat. It decodes
// a chat.Request JSON body, dispatches through the Harness, and returns the
// normalized chat.Response as JSON.
func ChatHandler(h *chat.Harness) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "empty body"})
			return
		}
		defer r.Body.Close()

		var req chat.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body: " + err.Error()})
			return
		}
		resp, err := h.Send(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
}
