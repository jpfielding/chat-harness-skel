package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

func mustReadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join("testdata", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	return b
}

type capturedRequest struct {
	method, path, auth, org string
	body                    []byte
}

func newFake(t *testing.T, body []byte, status int, headers map[string]string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	cap := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.auth = r.Header.Get("Authorization")
		cap.org = r.Header.Get("OpenAI-Organization")
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			cap.body = b
		}
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func TestSend_SimpleText(t *testing.T) {
	srv, cap := newFake(t, mustReadTestdata(t, "simple_text.response.json"), 200, nil)
	p, err := New(Config{APIKey: "sk-test", BaseURL: srv.URL, OrgID: "org-test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Send(context.Background(), chat.Request{
		Model:    "openai:gpt-5",
		Messages: []chat.Message{chat.UserText("Hi")},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if cap.method != "POST" || cap.path != "/v1/chat/completions" {
		t.Errorf("bad route: %s %s", cap.method, cap.path)
	}
	if cap.auth != "Bearer sk-test" {
		t.Errorf("bad auth: %q", cap.auth)
	}
	if cap.org != "org-test" {
		t.Errorf("bad org: %q", cap.org)
	}
	if resp.Message.Content[0].Text != "Hi there! How can I help?" {
		t.Errorf("text=%q", resp.Message.Content[0].Text)
	}
	if resp.StopReason != chat.StopEnd {
		t.Errorf("stop=%s", resp.StopReason)
	}
	if resp.Usage.InputTokens != 8 || resp.Usage.OutputTokens != 9 {
		t.Errorf("usage=%+v", resp.Usage)
	}
}

func TestSend_ParallelTools(t *testing.T) {
	srv, _ := newFake(t, mustReadTestdata(t, "parallel_tools.response.json"), 200, nil)
	p, _ := New(Config{APIKey: "k", BaseURL: srv.URL})
	resp, err := p.Send(context.Background(), chat.Request{
		Model:    "openai:gpt-5",
		Messages: []chat.Message{chat.UserText("weather Portland and Seattle?")},
		Tools: []chat.ToolSpec{{
			Name: "lookup_weather",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.StopReason != chat.StopToolUse {
		t.Errorf("stop=%s", resp.StopReason)
	}
	var uses []chat.ToolUse
	for _, b := range resp.Message.Content {
		if b.Kind == chat.BlockToolUse && b.ToolUse != nil {
			uses = append(uses, *b.ToolUse)
		}
	}
	if len(uses) != 2 {
		t.Fatalf("want 2 tool_use blocks, got %d: %+v", len(uses), resp.Message.Content)
	}
	if uses[0].ID == uses[1].ID {
		t.Errorf("ids should differ: %+v", uses)
	}
	for _, u := range uses {
		if u.ParseError != "" {
			t.Errorf("parse error: %q", u.ParseError)
		}
		if u.ParsedInput["city"] == nil {
			t.Errorf("missing city: %+v", u.ParsedInput)
		}
	}
}

func TestSend_MalformedToolArgs_ExposedNotHidden(t *testing.T) {
	srv, _ := newFake(t, mustReadTestdata(t, "malformed_tool_args.response.json"), 200, nil)
	p, _ := New(Config{APIKey: "k", BaseURL: srv.URL})
	resp, err := p.Send(context.Background(), chat.Request{
		Model:    "openai:gpt-5",
		Messages: []chat.Message{chat.UserText("x")},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	var got *chat.ToolUse
	for _, b := range resp.Message.Content {
		if b.Kind == chat.BlockToolUse {
			got = b.ToolUse
			break
		}
	}
	if got == nil {
		t.Fatal("no tool_use block")
	}
	if got.RawInput == "" {
		t.Error("RawInput must be populated even for malformed JSON")
	}
	if got.ParseError == "" {
		t.Error("ParseError must be set for malformed JSON")
	}
	if got.ParsedInput != nil {
		t.Errorf("ParsedInput must be nil when ParseError set: %+v", got.ParsedInput)
	}
}

func TestSend_ContextLength(t *testing.T) {
	srv, _ := newFake(t, mustReadTestdata(t, "context_length.response.json"), 400, nil)
	p, _ := New(Config{APIKey: "k", BaseURL: srv.URL})
	_, err := p.Send(context.Background(), chat.Request{
		Model:    "openai:gpt-5",
		Messages: []chat.Message{chat.UserText("huge prompt")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *chat.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Kind != chat.ErrKindContextLength {
		t.Errorf("kind=%s", pe.Kind)
	}
}

func TestBuildChatRequest_ToolResultBecomesToolRole(t *testing.T) {
	req := chat.Request{
		Messages: []chat.Message{
			{Role: chat.RoleAssistant, Content: []chat.ContentBlock{{
				Kind: chat.BlockToolUse, ToolUse: &chat.ToolUse{
					ID: "call_1", Name: "lookup", ParsedInput: map[string]any{"q": "x"},
				},
			}}},
			{Role: chat.RoleTool, Content: []chat.ContentBlock{{
				Kind: chat.BlockToolResult, ToolResult: &chat.ToolResult{
					ToolUseID: "call_1", Content: []chat.ContentBlock{chat.TextBlock("ok")},
				},
			}}},
		},
	}
	w, err := buildChatRequest(req, chat.ModelRef{Provider: "openai", Model: "gpt-5"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(w.Messages) != 2 {
		t.Fatalf("want 2 messages, got %d", len(w.Messages))
	}
	if w.Messages[0].Role != "assistant" {
		t.Errorf("msg0 role=%q", w.Messages[0].Role)
	}
	if len(w.Messages[0].ToolCalls) != 1 || w.Messages[0].ToolCalls[0].ID != "call_1" {
		t.Errorf("tool call not preserved: %+v", w.Messages[0].ToolCalls)
	}
	if w.Messages[1].Role != "tool" {
		t.Errorf("msg1 role=%q (should be tool)", w.Messages[1].Role)
	}
	if w.Messages[1].ToolCallID != "call_1" {
		t.Errorf("tool_call_id=%q", w.Messages[1].ToolCallID)
	}
}

func TestBuildChatRequest_ImageURL(t *testing.T) {
	req := chat.Request{
		Messages: []chat.Message{{
			Role: chat.RoleUser,
			Content: []chat.ContentBlock{
				chat.TextBlock("what is this"),
				{Kind: chat.BlockImage, Image: &chat.ImageSource{MediaType: "image/png", URL: "https://example.com/x.png"}},
			},
		}},
	}
	w, err := buildChatRequest(req, chat.ModelRef{Provider: "openai", Model: "gpt-5"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	b, _ := json.Marshal(w.Messages[0].Content)
	if !bytesContains(b, []byte("image_url")) {
		t.Errorf("expected image_url part: %s", b)
	}
}

func bytesContains(haystack, needle []byte) bool {
	return len(haystack) >= len(needle) && json.Valid(haystack) && containsSubstr(string(haystack), string(needle))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
