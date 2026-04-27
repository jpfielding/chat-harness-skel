package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// fakeProvider is a minimal chat.Provider for handler tests. It lets each
// test control the response (or error) returned from Send.
type fakeProvider struct {
	name string
	resp chat.Response
	err  error
	seen chat.Request
}

func (f *fakeProvider) Name() string                                 { return f.name }
func (f *fakeProvider) Models() []chat.ModelInfo                     { return nil }
func (f *fakeProvider) Stream(context.Context, chat.Request) (chat.StreamReader, error) {
	return nil, errors.New("stream not used in this test")
}
func (f *fakeProvider) Send(ctx context.Context, req chat.Request) (chat.Response, error) {
	f.seen = req
	if f.err != nil {
		return chat.Response{}, f.err
	}
	return f.resp, nil
}

func TestChatHandler_Success(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: chat.Response{
			ID:         "id-1",
			Ref:        chat.ModelRef{Provider: "fake", Model: "m1"},
			Message:    chat.AssistantText("pong"),
			StopReason: chat.StopEnd,
			Usage:      chat.Usage{InputTokens: 3, OutputTokens: 1},
			Latency:    5 * time.Millisecond,
		},
	}
	h := chat.New(chat.WithProvider(fp))

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(chat.Request{
		Model:    "fake:m1",
		Messages: []chat.Message{chat.UserText("ping")},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(body))
	ChatHandler(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out chat.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != "id-1" || out.Message.Content[0].Text != "pong" {
		t.Errorf("unexpected response: %+v", out)
	}
	if fp.seen.Model != "fake:m1" {
		t.Errorf("provider saw model=%q", fp.seen.Model)
	}
}

func TestChatHandler_ValidationError(t *testing.T) {
	fp := &fakeProvider{name: "fake"}
	h := chat.New(chat.WithProvider(fp))
	rec := httptest.NewRecorder()
	// Empty messages + empty system → ValidationError.
	body, _ := json.Marshal(chat.Request{Model: "fake:m1"})
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(body))
	ChatHandler(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
	var er errorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &er)
	if er.Kind != "InvalidRequest" {
		t.Errorf("kind=%q", er.Kind)
	}
}

func TestChatHandler_ProviderErrorMapped(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		err: &chat.ProviderError{
			Kind: chat.ErrKindRateLimit, Provider: "fake", Model: "m1", StatusCode: 429,
		},
	}
	h := chat.New(chat.WithProvider(fp))
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(chat.Request{
		Model:    "fake:m1",
		Messages: []chat.Message{chat.UserText("hi")},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(body))
	ChatHandler(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status=%d", rec.Code)
	}
	var er errorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &er)
	if er.Kind != "RateLimit" {
		t.Errorf("kind=%q", er.Kind)
	}
	if er.Meta["provider"] != "fake" {
		t.Errorf("meta.provider=%q", er.Meta["provider"])
	}
}

func TestChatHandler_MalformedJSON(t *testing.T) {
	fp := &fakeProvider{name: "fake"}
	h := chat.New(chat.WithProvider(fp))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader([]byte("{not json")))
	ChatHandler(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d", rec.Code)
	}
}

func TestModelsHandler(t *testing.T) {
	h := chat.New()
	h.Catalog().Register(chat.ModelInfo{
		Ref: chat.ModelRef{Provider: "fake", Model: "m1"}, ContextTokens: 1000,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	ModelsHandler(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	b, _ := io.ReadAll(rec.Body)
	if !bytes.Contains(b, []byte(`"m1"`)) {
		t.Errorf("expected m1 in body: %s", b)
	}
}
