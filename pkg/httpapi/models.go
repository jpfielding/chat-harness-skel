package httpapi

import (
	"net/http"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// ModelsHandler returns an http.Handler that serves GET /api/models, listing
// all registered ModelInfo entries.
func ModelsHandler(h *chat.Harness) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"models":    h.Catalog().List(),
			"providers": h.Providers(),
		})
	})
}
