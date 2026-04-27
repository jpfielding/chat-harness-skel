package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

func newStreamFake(t *testing.T, sseName string) *httptest.Server {
	t.Helper()
	body := mustReadTestdata(t, sseName)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStream_SimpleText(t *testing.T) {
	srv := newStreamFake(t, "stream_text.sse")
	p, _ := New(Config{APIKey: "k", BaseURL: srv.URL})
	r, err := p.Stream(context.Background(), chat.Request{
		Model:    "anthropic:claude-opus-4-5",
		Messages: []chat.Message{chat.UserText("Hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	kinds, accumText := drain(t, r)
	want := []chat.EventKind{
		chat.EvMessageStart,
		chat.EvBlockStart, chat.EvBlockDelta, chat.EvBlockDelta, chat.EvBlockStop,
		chat.EvMessageDelta,
		chat.EvMessageStop,
	}
	eqKinds(t, kinds, want)
	if accumText != "Hello!" {
		t.Errorf("accumText=%q", accumText)
	}
	if r.State() != chat.StreamCompleted {
		t.Errorf("state=%q", r.State())
	}
}

func TestStream_ToolUseDeltas(t *testing.T) {
	srv := newStreamFake(t, "stream_tools.sse")
	p, _ := New(Config{APIKey: "k", BaseURL: srv.URL})
	r, err := p.Stream(context.Background(), chat.Request{
		Model:    "anthropic:claude-opus-4-5",
		Messages: []chat.Message{chat.UserText("weather?")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Collect RawInputDelta across tool_use block.
	var rawInputForIndex1 string
	var sawToolBlockStart bool
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == chat.EvBlockStart && ev.Block != nil && ev.Block.Kind == chat.BlockToolUse {
			sawToolBlockStart = true
			if ev.Block.ToolUse == nil || ev.Block.ToolUse.Name != "lookup_weather" {
				t.Errorf("unexpected tool use skeleton: %+v", ev.Block)
			}
		}
		if ev.Kind == chat.EvBlockDelta && ev.Index == 1 {
			rawInputForIndex1 += ev.RawInputDelta
		}
	}
	if !sawToolBlockStart {
		t.Error("no tool_use BlockStart emitted")
	}
	if !strings.Contains(rawInputForIndex1, `"city"`) || !strings.Contains(rawInputForIndex1, "Portland") {
		t.Errorf("accumulated tool args: %q", rawInputForIndex1)
	}
}

func TestStream_ClientCancel(t *testing.T) {
	// Server that streams slowly so cancellation reliably fires.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{}}}\n\n"))
		fl.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	p, _ := New(Config{APIKey: "k", BaseURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())

	r, err := p.Stream(ctx, chat.Request{
		Model:    "anthropic:claude-opus-4-5",
		Messages: []chat.Message{chat.UserText("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Pull the first event so the stream is in-flight.
	if _, err := r.Next(); err != nil {
		t.Fatalf("first Next: %v", err)
	}
	cancel()

	// The next Next should observe the cancel.
	_, err = r.Next()
	if err == nil {
		t.Fatal("expected error after cancel")
	}
	var pe *chat.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Kind != chat.ErrKindCanceled {
		t.Errorf("kind=%q", pe.Kind)
	}
	if r.State() != chat.StreamCanceled {
		t.Errorf("state=%q", r.State())
	}
}

func drain(t *testing.T, r chat.StreamReader) ([]chat.EventKind, string) {
	t.Helper()
	var kinds []chat.EventKind
	var text string
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		kinds = append(kinds, ev.Kind)
		text += ev.TextDelta
	}
	return kinds, text
}

func eqKinds(t *testing.T, got, want []chat.EventKind) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("kinds mismatch:\ngot:  %v\nwant: %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("kind[%d]=%q want %q (got=%v)", i, got[i], want[i], got)
		}
	}
}
