package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// pingInterval controls the SSE keep-alive cadence.
var pingInterval = 15 * time.Second

// StreamHandler serves POST /api/chat/stream. It dispatches through the
// harness and writes StreamEvents as SSE `event: <kind>\ndata: <json>\n\n`
// frames. Writing stops when the client disconnects or the stream ends.
func StreamHandler(h *chat.Harness) http.Handler {
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

		ctx := r.Context()

		reader, err := h.Stream(ctx, req)
		if err != nil {
			writeError(w, err)
			return
		}
		defer reader.Close()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // disable nginx proxy buffering
		w.WriteHeader(http.StatusOK)

		rc := http.NewResponseController(w)
		_ = rc.Flush()

		pingTicker := time.NewTicker(pingInterval)
		defer pingTicker.Stop()

		events := make(chan streamOut, 8)
		go pumpEvents(reader, events)

		for {
			select {
			case <-ctx.Done():
				return
			case <-pingTicker.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				if err := rc.Flush(); err != nil {
					return
				}
			case o, ok := <-events:
				if !ok {
					return
				}
				if o.err != nil {
					// Terminal provider error: emit a final "error" event, then stop.
					writeSSEError(w, rc, o.err)
					return
				}
				if err := writeSSEEvent(w, rc, o.ev); err != nil {
					return
				}
			}
		}
	})
}

type streamOut struct {
	ev  chat.StreamEvent
	err error
}

// pumpEvents reads from a StreamReader and forwards events (or a terminal
// error) onto ch, closing ch at the end of stream.
func pumpEvents(reader chat.StreamReader, ch chan<- streamOut) {
	defer close(ch)
	for {
		ev, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			// Translate context-canceled into silent close (client went away).
			if errors.Is(err, context.Canceled) {
				return
			}
			var pe *chat.ProviderError
			if errors.As(err, &pe) && pe.Kind == chat.ErrKindCanceled {
				return
			}
			ch <- streamOut{err: err}
			return
		}
		ch <- streamOut{ev: ev}
	}
}

func writeSSEEvent(w http.ResponseWriter, rc *http.ResponseController, ev chat.StreamEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, payload); err != nil {
		return err
	}
	return rc.Flush()
}

func writeSSEError(w http.ResponseWriter, rc *http.ResponseController, err error) {
	payload := map[string]string{"error": err.Error()}
	var pe *chat.ProviderError
	if errors.As(err, &pe) {
		payload["kind"] = string(pe.Kind)
		payload["provider"] = pe.Provider
		payload["model"] = pe.Model
	}
	b, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", b)
	_ = rc.Flush()
}
