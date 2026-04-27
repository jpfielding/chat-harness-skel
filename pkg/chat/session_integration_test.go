package chat_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
	"github.com/jpfielding/chat-harness-skel/pkg/session"
)

// echoProvider echoes the last user text back and records the full
// messages slice it saw on the most recent call.
type echoProvider struct {
	last []chat.Message
}

func (p *echoProvider) Name() string             { return "echo" }
func (p *echoProvider) Models() []chat.ModelInfo { return nil }
func (p *echoProvider) Send(ctx context.Context, req chat.Request) (chat.Response, error) {
	p.last = append([]chat.Message{}, req.Messages...)
	// Find the last user text.
	var reply string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == chat.RoleUser {
			for _, b := range req.Messages[i].Content {
				if b.Kind == chat.BlockText {
					reply = "echo: " + b.Text
					break
				}
			}
			break
		}
	}
	return chat.Response{
		ID:         "r1",
		Ref:        chat.ModelRef{Provider: "echo", Model: "m1"},
		Message:    chat.AssistantText(reply),
		StopReason: chat.StopEnd,
	}, nil
}
func (p *echoProvider) Stream(context.Context, chat.Request) (chat.StreamReader, error) {
	return nil, errors.New("not used")
}

func TestSessions_MultiTurnAccumulates(t *testing.T) {
	store := session.NewMemoryStore(session.MaxMessagesCap)
	prov := &echoProvider{}
	h := chat.New(chat.WithProvider(prov), chat.WithSessions(session.NewBinder(store)))

	// Turn 1: brand new session (Save via Binder.Append with version=0).
	resp, err := h.Send(context.Background(), chat.Request{
		Model:     "echo:m1",
		SessionID: "convo-1",
		Messages:  []chat.Message{chat.UserText("first")},
	})
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if resp.Message.Content[0].Text != "echo: first" {
		t.Errorf("turn 1 reply=%q", resp.Message.Content[0].Text)
	}
	if len(prov.last) != 1 {
		t.Errorf("turn 1 provider saw %d msgs", len(prov.last))
	}

	// Turn 2: harness must prepend prior history so provider sees 3 msgs
	// (user1, assistant1, user2).
	_, err = h.Send(context.Background(), chat.Request{
		Model:     "echo:m1",
		SessionID: "convo-1",
		Messages:  []chat.Message{chat.UserText("second")},
	})
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if len(prov.last) != 3 {
		t.Fatalf("turn 2 provider saw %d msgs, want 3: %+v", len(prov.last), prov.last)
	}
	if prov.last[0].Content[0].Text != "first" ||
		prov.last[1].Content[0].Text != "echo: first" ||
		prov.last[2].Content[0].Text != "second" {
		t.Errorf("unexpected prior-history merge: %+v", prov.last)
	}

	// Session state should have 4 msgs now (u1, a1, u2, a2) and version 2:
	// Turn 1 writes via Save (v0 -> v1); turn 2 appends (v1 -> v2).
	got, err := store.Get(context.Background(), "convo-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 4 {
		t.Errorf("stored messages=%d", len(got.Messages))
	}
	if got.Version != 2 {
		t.Errorf("version=%d", got.Version)
	}
}

func TestSessions_RejectsWithoutStore(t *testing.T) {
	prov := &echoProvider{}
	h := chat.New(chat.WithProvider(prov)) // no session store
	_, err := h.Send(context.Background(), chat.Request{
		Model:     "echo:m1",
		SessionID: "x",
		Messages:  []chat.Message{chat.UserText("hi")},
	})
	if err == nil || !containsStr(err.Error(), "no session store") {
		t.Errorf("expected session-store error, got %v", err)
	}
}

func containsStr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
