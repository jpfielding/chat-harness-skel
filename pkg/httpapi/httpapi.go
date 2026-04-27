// Package httpapi contains the HTTP handlers that wrap the chat.Harness.
//
// Handlers return JSON and never log request or response bodies. Provider
// API keys never travel over this API; they are loaded server-side at
// startup from standard credential locations.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errorResponse is the JSON body returned on any non-2xx response.
type errorResponse struct {
	Error string            `json:"error"`
	Kind  string            `json:"kind,omitempty"`
	Meta  map[string]string `json:"meta,omitempty"`
}

// writeError converts err into an appropriate HTTP response. ProviderError
// preserves Kind and status; ValidationError maps to 400; everything else
// 500.
func writeError(w http.ResponseWriter, err error) {
	var pe *chat.ProviderError
	if errors.As(err, &pe) {
		status := pe.StatusCode
		if status == 0 {
			status = mapKindToStatus(pe.Kind)
		}
		resp := errorResponse{Error: pe.Message, Kind: string(pe.Kind)}
		if resp.Error == "" {
			resp.Error = pe.Error()
		}
		resp.Meta = map[string]string{"provider": pe.Provider, "model": pe.Model}
		if pe.RequestID != "" {
			resp.Meta["request_id"] = pe.RequestID
		}
		writeJSON(w, status, resp)
		return
	}
	var ve *chat.ValidationError
	if errors.As(err, &ve) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: ve.Error(), Kind: "InvalidRequest"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error(), Kind: "Unknown"})
}

func mapKindToStatus(k chat.ErrorKind) int {
	switch k {
	case chat.ErrKindAuthFailed:
		return http.StatusUnauthorized
	case chat.ErrKindNotFound:
		return http.StatusNotFound
	case chat.ErrKindRateLimit:
		return http.StatusTooManyRequests
	case chat.ErrKindTimeout:
		return http.StatusGatewayTimeout
	case chat.ErrKindOverloaded:
		return http.StatusServiceUnavailable
	case chat.ErrKindContextLength, chat.ErrKindInvalidRequest, chat.ErrKindUnsupportedContent:
		return http.StatusBadRequest
	case chat.ErrKindCanceled:
		return 499 // client closed request (nginx convention)
	case chat.ErrKindServerError, chat.ErrKindUnknown, chat.ErrKindToolsUnsupported:
		return http.StatusBadGateway
	}
	return http.StatusInternalServerError
}
