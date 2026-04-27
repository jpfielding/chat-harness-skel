package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

func newServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestSend_SimpleText(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"llama3.1:8b","message":{"role":"assistant","content":"Hello!"},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":3}`))
	})
	no := false
	p, _ := New(Config{BaseURL: srv.URL, SupportsTools: &no})
	resp, err := p.Send(context.Background(), chat.Request{
		Model:    "ollama:llama3.1:8b",
		Messages: []chat.Message{chat.UserText("Hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content[0].Text != "Hello!" {
		t.Errorf("text=%q", resp.Message.Content[0].Text)
	}
	if resp.StopReason != chat.StopEnd {
		t.Errorf("stop=%s", resp.StopReason)
	}
	if resp.Usage.InputTokens != 5 || resp.Usage.OutputTokens != 3 {
		t.Errorf("usage=%+v", resp.Usage)
	}
}

func TestSend_ToolsRefusedWhenUnsupported(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Ollama should not be called when tools are requested and unsupported")
	})
	no := false
	p, _ := New(Config{BaseURL: srv.URL, SupportsTools: &no})
	_, err := p.Send(context.Background(), chat.Request{
		Model:    "ollama:llama3.1:8b",
		Messages: []chat.Message{chat.UserText("x")},
		Tools:    []chat.ToolSpec{{Name: "t"}},
	})
	var pe *chat.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ProviderError, got %v", err)
	}
	if pe.Kind != chat.ErrKindToolsUnsupported {
		t.Errorf("kind=%s", pe.Kind)
	}
}

func TestSend_ToolCallsSynthesizeIDs(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"lookup","arguments":{"q":"x"}}},{"function":{"name":"lookup","arguments":{"q":"y"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":4}`))
	})
	yes := true
	p, _ := New(Config{BaseURL: srv.URL, SupportsTools: &yes})
	resp, err := p.Send(context.Background(), chat.Request{
		Model:    "ollama:llama3.1:70b",
		Messages: []chat.Message{chat.UserText("x")},
		Tools:    []chat.ToolSpec{{Name: "lookup"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, b := range resp.Message.Content {
		if b.Kind == chat.BlockToolUse {
			ids = append(ids, b.ToolUse.ID)
			if b.ToolUse.ParsedInput == nil {
				t.Errorf("ParsedInput empty for %s", b.ToolUse.Name)
			}
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Errorf("ids=%v", ids)
	}
	if resp.StopReason != chat.StopToolUse {
		t.Errorf("stop=%s", resp.StopReason)
	}
}

func TestStream_TextDeltas(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl := w.(http.Flusher)
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"Hel"},"done":false}` + "\n"))
		fl.Flush()
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"lo!"},"done":false}` + "\n"))
		fl.Flush()
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":2}` + "\n"))
	})
	no := false
	p, _ := New(Config{BaseURL: srv.URL, SupportsTools: &no})
	r, err := p.Stream(context.Background(), chat.Request{
		Model:    "ollama:llama3.1:8b",
		Messages: []chat.Message{chat.UserText("Hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var text string
	var kinds []chat.EventKind
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, ev.Kind)
		text += ev.TextDelta
	}
	if text != "Hello!" {
		t.Errorf("text=%q", text)
	}
	// Expect: message_start, block_start, block_delta, block_delta, block_stop, message_delta, message_stop
	if len(kinds) != 7 {
		t.Errorf("kinds=%v", kinds)
	}
}

func TestBuildChatRequest_SystemAndToolResult(t *testing.T) {
	req := chat.Request{
		System: "be terse",
		Messages: []chat.Message{
			{Role: chat.RoleAssistant, Content: []chat.ContentBlock{{
				Kind: chat.BlockToolUse, ToolUse: &chat.ToolUse{
					ID: "x", Name: "lookup", ParsedInput: map[string]any{"q": "a"},
				},
			}}},
			{Role: chat.RoleTool, Content: []chat.ContentBlock{{
				Kind: chat.BlockToolResult, ToolResult: &chat.ToolResult{
					ToolUseID: "x", Content: []chat.ContentBlock{chat.TextBlock("42")},
				},
			}}},
		},
	}
	w, err := buildChatRequest(req, chat.ModelRef{Provider: "ollama", Model: "llama3.1:70b"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.Messages[0].Role != "system" || w.Messages[0].Content != "be terse" {
		t.Errorf("bad system msg: %+v", w.Messages[0])
	}
	if w.Messages[1].Role != "assistant" || len(w.Messages[1].ToolCalls) != 1 {
		t.Errorf("bad assistant msg: %+v", w.Messages[1])
	}
	if w.Messages[2].Role != "tool" || w.Messages[2].Content != "42" {
		t.Errorf("bad tool msg: %+v", w.Messages[2])
	}
	// Spot-check that arguments is an object, not a string.
	enc, _ := json.Marshal(w.Messages[1].ToolCalls[0].Function.Arguments)
	if !strings.HasPrefix(string(enc), "{") {
		t.Errorf("arguments should serialize as object: %s", enc)
	}
}

func TestVersionProbe(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_, _ = io.WriteString(w, `{"version":"0.5.1"}`)
		}
	})
	p, err := New(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !p.SupportsTools() {
		t.Error("expected SupportsTools=true for 0.5.1")
	}

	srv2 := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"version":"0.2.4"}`)
	})
	p2, _ := New(Config{BaseURL: srv2.URL})
	if p2.SupportsTools() {
		t.Error("expected SupportsTools=false for 0.2.4")
	}
}

func TestVersionGTE(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"0.3.0", true},
		{"0.3.5", true},
		{"0.4.0", true},
		{"1.0.0", true},
		{"0.2.9", false},
		{"0.2.12", false},
		{"garbage", false},
		{"0.3.0-rc1", true},
	}
	for _, tc := range cases {
		got := versionGTE(tc.v, 0, 3, 0)
		if got != tc.want {
			t.Errorf("versionGTE(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
}
