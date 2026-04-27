package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func newFake(t *testing.T, body []byte, status int, extraHeaders map[string]string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	cap := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.apiKey = r.Header.Get("x-api-key")
		cap.apiVersion = r.Header.Get("anthropic-version")
		if r.Body != nil {
			bs, _ := io.ReadAll(r.Body)
			cap.body = bs
		}
		for k, v := range extraHeaders {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

type capturedRequest struct {
	method, path, apiKey, apiVersion string
	body                             []byte
}

func TestSend_SimpleText(t *testing.T) {
	srv, cap := newFake(t, mustReadTestdata(t, "simple_text.response.json"), 200, nil)

	p, err := New(Config{APIKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Send(context.Background(), chat.Request{
		Model:    "anthropic:claude-opus-4-5",
		Messages: []chat.Message{chat.UserText("Hi")},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if cap.method != "POST" || cap.path != "/v1/messages" {
		t.Errorf("bad route: %s %s", cap.method, cap.path)
	}
	if cap.apiKey != "test-key" {
		t.Errorf("missing/wrong x-api-key: %q", cap.apiKey)
	}
	if cap.apiVersion != DefaultAPIVersion {
		t.Errorf("missing anthropic-version: %q", cap.apiVersion)
	}

	if resp.ID != "msg_01ABC" {
		t.Errorf("id=%q", resp.ID)
	}
	if len(resp.Message.Content) != 1 || resp.Message.Content[0].Kind != chat.BlockText {
		t.Fatalf("unexpected content: %+v", resp.Message.Content)
	}
	if resp.Message.Content[0].Text != "Hello! How can I help you today?" {
		t.Errorf("bad text: %q", resp.Message.Content[0].Text)
	}
	if resp.StopReason != chat.StopEnd {
		t.Errorf("stop=%q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 10 {
		t.Errorf("usage=%+v", resp.Usage)
	}
}

func TestSend_ParallelTools(t *testing.T) {
	srv, _ := newFake(t, mustReadTestdata(t, "parallel_tools.response.json"), 200, nil)

	p, err := New(Config{APIKey: "k", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Send(context.Background(), chat.Request{
		Model:    "anthropic:claude-opus-4-5",
		Messages: []chat.Message{chat.UserText("weather in Portland and Seattle?")},
		Tools: []chat.ToolSpec{{
			Name: "lookup_weather",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if resp.StopReason != chat.StopToolUse {
		t.Errorf("stop=%q", resp.StopReason)
	}

	var toolUses []chat.ToolUse
	for _, b := range resp.Message.Content {
		if b.Kind == chat.BlockToolUse && b.ToolUse != nil {
			toolUses = append(toolUses, *b.ToolUse)
		}
	}
	if len(toolUses) != 2 {
		t.Fatalf("expected 2 tool_use blocks, got %d", len(toolUses))
	}
	if toolUses[0].ID == toolUses[1].ID {
		t.Errorf("ids should differ: %v", toolUses)
	}
	for _, tu := range toolUses {
		if tu.Name != "lookup_weather" {
			t.Errorf("tool name=%q", tu.Name)
		}
		if tu.ParsedInput == nil {
			t.Errorf("expected ParsedInput, got ParseError=%q", tu.ParseError)
		}
		if _, ok := tu.ParsedInput["city"]; !ok {
			t.Errorf("expected city arg: %+v", tu.ParsedInput)
		}
	}
}

func TestSend_RateLimit(t *testing.T) {
	srv, _ := newFake(t, mustReadTestdata(t, "rate_limit.response.json"), 429, map[string]string{
		"retry-after": "30",
		"request-id":  "req_rl",
	})
	p, _ := New(Config{APIKey: "k", BaseURL: srv.URL})
	_, err := p.Send(context.Background(), chat.Request{
		Model:    "anthropic:claude-opus-4-5",
		Messages: []chat.Message{chat.UserText("hi")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *chat.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ProviderError, got %T: %v", err, err)
	}
	if pe.Kind != chat.ErrKindRateLimit {
		t.Errorf("kind=%s", pe.Kind)
	}
	if pe.StatusCode != 429 {
		t.Errorf("status=%d", pe.StatusCode)
	}
	if pe.RetryAfter.Seconds() != 30 {
		t.Errorf("retry_after=%v", pe.RetryAfter)
	}
	if pe.RequestID != "req_rl" {
		t.Errorf("request_id=%q", pe.RequestID)
	}
}

func TestBuildMessagesRequest_SystemMerged(t *testing.T) {
	req := chat.Request{
		System: "Be concise.",
		Messages: []chat.Message{
			{Role: chat.RoleSystem, Content: []chat.ContentBlock{chat.TextBlock("Answer in English.")}},
			chat.UserText("hi"),
		},
	}
	w, err := buildMessagesRequest(req, chat.ModelRef{Provider: "anthropic", Model: "claude-opus-4-5"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(w.System, "Be concise.") || !strings.Contains(w.System, "Answer in English.") {
		t.Errorf("system merge failed: %q", w.System)
	}
	if len(w.Messages) != 1 {
		t.Errorf("expected 1 non-system message, got %d", len(w.Messages))
	}
}

func TestBuildMessagesRequest_ToolResult(t *testing.T) {
	req := chat.Request{
		Messages: []chat.Message{
			{Role: chat.RoleAssistant, Content: []chat.ContentBlock{{
				Kind: chat.BlockToolUse, ToolUse: &chat.ToolUse{
					ID: "toolu_1", Name: "lookup", ParsedInput: map[string]any{"q": "x"},
				},
			}}},
			{Role: chat.RoleTool, Content: []chat.ContentBlock{{
				Kind: chat.BlockToolResult, ToolResult: &chat.ToolResult{
					ToolUseID: "toolu_1", Content: []chat.ContentBlock{chat.TextBlock("42")},
				},
			}}},
		},
	}
	w, err := buildMessagesRequest(req, chat.ModelRef{Provider: "anthropic", Model: "claude-opus-4-5"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Second message should be role=user with a tool_result block (Anthropic convention).
	if len(w.Messages) != 2 {
		t.Fatalf("want 2 msgs, got %d", len(w.Messages))
	}
	if w.Messages[1].Role != "user" {
		t.Errorf("tool role should become user, got %q", w.Messages[1].Role)
	}
	if w.Messages[1].Content[0].Type != "tool_result" {
		t.Errorf("expected tool_result block, got %q", w.Messages[1].Content[0].Type)
	}
	if w.Messages[1].Content[0].ToolUseID != "toolu_1" {
		t.Errorf("tool_use_id=%q", w.Messages[1].Content[0].ToolUseID)
	}
}

func TestSend_WireBodyShape(t *testing.T) {
	srv, cap := newFake(t, mustReadTestdata(t, "simple_text.response.json"), 200, nil)
	p, _ := New(Config{APIKey: "k", BaseURL: srv.URL})
	_, err := p.Send(context.Background(), chat.Request{
		Model:    "anthropic:claude-opus-4-5",
		System:   "Be helpful.",
		Messages: []chat.Message{chat.UserText("hello")},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(cap.body, &parsed); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if parsed["model"] != "claude-opus-4-5" {
		t.Errorf("bad model: %v", parsed["model"])
	}
	if parsed["system"] != "Be helpful." {
		t.Errorf("bad system: %v", parsed["system"])
	}
}
