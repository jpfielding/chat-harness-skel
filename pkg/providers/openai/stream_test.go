package openai

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
		Model:    "openai:gpt-5",
		Messages: []chat.Message{chat.UserText("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var kinds []chat.EventKind
	var text string
	var gotUsage *chat.Usage
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
		if ev.Usage != nil {
			gotUsage = ev.Usage
		}
	}
	want := []chat.EventKind{
		chat.EvMessageStart,
		chat.EvBlockStart, chat.EvBlockDelta, chat.EvBlockDelta, chat.EvBlockStop,
		chat.EvMessageDelta,
		chat.EvMessageStop,
	}
	if len(kinds) != len(want) {
		t.Fatalf("kinds mismatch: got %v want %v", kinds, want)
	}
	for i := range kinds {
		if kinds[i] != want[i] {
			t.Fatalf("kind[%d]=%q want %q (got=%v)", i, kinds[i], want[i], kinds)
		}
	}
	if text != "Hello!" {
		t.Errorf("text=%q", text)
	}
	if gotUsage == nil || gotUsage.InputTokens != 3 || gotUsage.OutputTokens != 2 {
		t.Errorf("usage=%+v", gotUsage)
	}
}

func TestStream_ParallelToolCalls_SyntheticBlocks(t *testing.T) {
	srv := newStreamFake(t, "stream_parallel_tools.sse")
	p, _ := New(Config{APIKey: "k", BaseURL: srv.URL})
	r, err := p.Stream(context.Background(), chat.Request{
		Model:    "openai:gpt-5",
		Messages: []chat.Message{chat.UserText("Portland + Seattle weather?")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Assertions:
	//   - Two BlockStart events for tool_use with distinct normalized Indexes.
	//   - RawInputDelta across each block accumulates to valid JSON.
	starts := []chat.StreamEvent{}
	rawByIndex := map[int]string{}
	var stopReason chat.StopReason
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		switch ev.Kind {
		case chat.EvBlockStart:
			if ev.Block != nil && ev.Block.Kind == chat.BlockToolUse {
				starts = append(starts, ev)
			}
		case chat.EvBlockDelta:
			rawByIndex[ev.Index] += ev.RawInputDelta
		case chat.EvMessageDelta:
			stopReason = ev.StopReason
		}
	}
	if len(starts) != 2 {
		t.Fatalf("want 2 tool_use BlockStarts, got %d", len(starts))
	}
	if starts[0].Index == starts[1].Index {
		t.Errorf("tool_use block indexes should differ: %v", starts)
	}
	for _, s := range starts {
		if s.Block.ToolUse.ID == "" || !strings.HasPrefix(s.Block.ToolUse.ID, "call_") {
			t.Errorf("missing/bad tool id: %+v", s.Block.ToolUse)
		}
		acc := rawByIndex[s.Index]
		if !strings.Contains(acc, `"city"`) {
			t.Errorf("tool block %d raw accum=%q", s.Index, acc)
		}
	}
	if stopReason != chat.StopToolUse {
		t.Errorf("stop_reason=%q", stopReason)
	}
}

func TestStream_ClientCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(`data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}` + "\n\n"))
		fl.Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	p, _ := New(Config{APIKey: "k", BaseURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	r, err := p.Stream(ctx, chat.Request{
		Model:    "openai:gpt-5",
		Messages: []chat.Message{chat.UserText("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if _, err := r.Next(); err != nil {
		t.Fatalf("first Next: %v", err)
	}
	cancel()

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
