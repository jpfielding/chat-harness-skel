package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// scriptedReader returns a fixed sequence of StreamEvents, then io.EOF.
type scriptedReader struct {
	events []chat.StreamEvent
	i      int
	state  chat.StreamState
	closed atomic.Bool
	delay  time.Duration
}

func (s *scriptedReader) Next() (chat.StreamEvent, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.i >= len(s.events) {
		s.state = chat.StreamCompleted
		return chat.StreamEvent{}, io.EOF
	}
	ev := s.events[s.i]
	s.i++
	return ev, nil
}
func (s *scriptedReader) State() chat.StreamState { return s.state }
func (s *scriptedReader) Close() error            { s.closed.Store(true); return nil }

type scriptedProvider struct {
	name   string
	reader chat.StreamReader
}

func (p *scriptedProvider) Name() string                                 { return p.name }
func (p *scriptedProvider) Models() []chat.ModelInfo                     { return nil }
func (p *scriptedProvider) Send(context.Context, chat.Request) (chat.Response, error) {
	return chat.Response{}, errors.New("not used")
}
func (p *scriptedProvider) Stream(ctx context.Context, req chat.Request) (chat.StreamReader, error) {
	return p.reader, nil
}

func TestStreamHandler_EmitsSSE(t *testing.T) {
	reader := &scriptedReader{events: []chat.StreamEvent{
		{Kind: chat.EvMessageStart},
		{Kind: chat.EvBlockStart, Index: 0, Block: &chat.ContentBlock{Kind: chat.BlockText}},
		{Kind: chat.EvBlockDelta, Index: 0, TextDelta: "Hi"},
		{Kind: chat.EvBlockStop, Index: 0},
		{Kind: chat.EvMessageStop},
	}}
	h := chat.New(chat.WithProvider(&scriptedProvider{name: "fake", reader: reader}))

	srv := httptest.NewServer(StreamHandler(h))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"model":"fake:m1","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content-type=%q", resp.Header.Get("Content-Type"))
	}
	events := readSSE(t, resp.Body)
	if len(events) != 5 {
		t.Fatalf("got %d events: %v", len(events), events)
	}
	wantKinds := []string{"message_start", "block_start", "block_delta", "block_stop", "message_stop"}
	for i, ev := range events {
		if ev.name != wantKinds[i] {
			t.Errorf("event[%d]=%q want %q", i, ev.name, wantKinds[i])
		}
	}
	if !bytes.Contains([]byte(events[2].data), []byte(`"Hi"`)) {
		t.Errorf("delta event missing text: %q", events[2].data)
	}
	if !reader.closed.Load() {
		t.Error("reader was not closed")
	}
}

func TestStreamHandler_ClientDisconnect(t *testing.T) {
	// A reader that produces events slowly so we can cancel mid-stream.
	reader := &scriptedReader{
		events: []chat.StreamEvent{
			{Kind: chat.EvMessageStart},
			{Kind: chat.EvBlockStart, Index: 0, Block: &chat.ContentBlock{Kind: chat.BlockText}},
			{Kind: chat.EvBlockDelta, Index: 0, TextDelta: "one"},
			{Kind: chat.EvBlockDelta, Index: 0, TextDelta: "two"},
			{Kind: chat.EvBlockDelta, Index: 0, TextDelta: "three"},
			{Kind: chat.EvBlockStop, Index: 0},
			{Kind: chat.EvMessageStop},
		},
		delay: 100 * time.Millisecond,
	}
	h := chat.New(chat.WithProvider(&scriptedProvider{name: "fake", reader: reader}))
	srv := httptest.NewServer(StreamHandler(h))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL,
		strings.NewReader(`{"model":"fake:m1","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	// Read a couple of events, then cancel the client context.
	br := bufio.NewReader(resp.Body)
	for i := 0; i < 4; i++ {
		if _, err := readOneSSEEvent(br); err != nil {
			t.Fatalf("read: %v", err)
		}
	}
	cancel()
	_ = resp.Body.Close()

	// Give the server a moment to notice disconnect and close the reader.
	deadline := time.After(2 * time.Second)
	for !reader.closed.Load() {
		select {
		case <-deadline:
			t.Fatal("reader was not closed after client disconnect")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// sseEvent captures one parsed SSE event for test assertions.
type sseEvent struct {
	name string
	data string
}

func readSSE(t *testing.T, r io.Reader) []sseEvent {
	t.Helper()
	br := bufio.NewReader(r)
	var events []sseEvent
	for {
		ev, err := readOneSSEEvent(br)
		if err == io.EOF {
			return events
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		events = append(events, ev)
	}
}

func readOneSSEEvent(br *bufio.Reader) (sseEvent, error) {
	var ev sseEvent
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF && (ev.name != "" || ev.data != "") {
				return ev, nil
			}
			return sseEvent{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if ev.name == "" && ev.data == "" {
				continue // empty event filler
			}
			return ev, nil
		}
		if strings.HasPrefix(line, ":") {
			continue // ping
		}
		if strings.HasPrefix(line, "event:") {
			ev.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			if ev.data != "" {
				ev.data += "\n"
			}
			ev.data += strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
}

// Ensure mock reader types compile against the interface at test-time.
var _ chat.StreamReader = (*scriptedReader)(nil)
var _ = fmt.Sprintf
