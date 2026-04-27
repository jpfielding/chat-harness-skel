package chat_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// scriptedProvider returns a preset error OR response on each call. Used
// to exercise the Harness's fallback executor.
type scriptedFBProvider struct {
	name  string
	resp  chat.Response
	err   error
	calls int
}

func (p *scriptedFBProvider) Name() string             { return p.name }
func (p *scriptedFBProvider) Models() []chat.ModelInfo { return nil }
func (p *scriptedFBProvider) Send(ctx context.Context, req chat.Request) (chat.Response, error) {
	p.calls++
	if p.err != nil {
		return chat.Response{}, p.err
	}
	return p.resp, nil
}
func (p *scriptedFBProvider) Stream(context.Context, chat.Request) (chat.StreamReader, error) {
	return nil, errors.New("not used")
}

// staticRouter returns a fixed candidate list.
type staticRouter struct{ refs []chat.ModelRef }

func (r *staticRouter) Pick(context.Context, chat.Request) ([]chat.ModelRef, error) {
	return r.refs, nil
}

func TestFallback_TriesNextOnRateLimit(t *testing.T) {
	rateLimited := &scriptedFBProvider{
		name: "a",
		err: &chat.ProviderError{
			Kind: chat.ErrKindRateLimit, Provider: "a", Model: "m1",
		},
	}
	happy := &scriptedFBProvider{
		name: "b",
		resp: chat.Response{
			Ref:        chat.ModelRef{Provider: "b", Model: "m2"},
			Message:    chat.AssistantText("ok"),
			StopReason: chat.StopEnd,
		},
	}
	h := chat.New(
		chat.WithProvider(rateLimited),
		chat.WithProvider(happy),
		chat.WithRouter(&staticRouter{refs: []chat.ModelRef{
			{Provider: "a", Model: "m1"},
			{Provider: "b", Model: "m2"},
		}}),
		chat.WithFallback(chat.FallbackPolicy{
			FallbackOnKinds: map[chat.ErrorKind]bool{chat.ErrKindRateLimit: true},
		}),
	)
	resp, err := h.Send(context.Background(), chat.Request{
		Messages: []chat.Message{chat.UserText("hi")},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Ref.Provider != "b" {
		t.Errorf("expected fallback to b, got %+v", resp.Ref)
	}
	if rateLimited.calls != 1 || happy.calls != 1 {
		t.Errorf("call counts a=%d b=%d", rateLimited.calls, happy.calls)
	}
	if len(resp.Attempts) != 2 {
		t.Errorf("expected 2 attempts, got %d", len(resp.Attempts))
	}
	if resp.Attempts[0].Error == nil || resp.Attempts[0].Error.Kind != chat.ErrKindRateLimit {
		t.Errorf("attempt[0] error=%+v", resp.Attempts[0].Error)
	}
}

func TestFallback_DoesNotFallBackOnAfterOutput(t *testing.T) {
	// Simulate an error that landed after bytes were emitted.
	midstream := &scriptedFBProvider{
		name: "a",
		err: &chat.ProviderError{
			Kind: chat.ErrKindRateLimit, Provider: "a", Model: "m1", AfterOutput: true,
		},
	}
	other := &scriptedFBProvider{name: "b", resp: chat.Response{Message: chat.AssistantText("never")}}
	h := chat.New(
		chat.WithProvider(midstream),
		chat.WithProvider(other),
		chat.WithRouter(&staticRouter{refs: []chat.ModelRef{
			{Provider: "a", Model: "m1"},
			{Provider: "b", Model: "m2"},
		}}),
		chat.WithFallback(chat.FallbackPolicy{
			FallbackOnKinds: map[chat.ErrorKind]bool{chat.ErrKindRateLimit: true},
		}),
	)
	_, err := h.Send(context.Background(), chat.Request{
		Messages: []chat.Message{chat.UserText("hi")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if other.calls != 0 {
		t.Errorf("provider b should not be called after AfterOutput: calls=%d", other.calls)
	}
}

func TestFallback_DoesNotFallBackOnNonListedKind(t *testing.T) {
	bad := &scriptedFBProvider{
		name: "a",
		err: &chat.ProviderError{
			Kind: chat.ErrKindAuthFailed, Provider: "a", Model: "m1",
		},
	}
	other := &scriptedFBProvider{name: "b"}
	h := chat.New(
		chat.WithProvider(bad),
		chat.WithProvider(other),
		chat.WithRouter(&staticRouter{refs: []chat.ModelRef{
			{Provider: "a", Model: "m1"},
			{Provider: "b", Model: "m2"},
		}}),
		chat.WithFallback(chat.FallbackPolicy{
			FallbackOnKinds: map[chat.ErrorKind]bool{chat.ErrKindRateLimit: true},
			// AuthFailed is NOT in the list.
		}),
	)
	_, err := h.Send(context.Background(), chat.Request{
		Messages: []chat.Message{chat.UserText("hi")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if other.calls != 0 {
		t.Errorf("non-listed kind shouldn't fall back: b.calls=%d", other.calls)
	}
}

func TestFallback_ContextLengthOnlyToLargerWindow(t *testing.T) {
	tooSmall := &scriptedFBProvider{
		name: "a",
		err: &chat.ProviderError{
			Kind: chat.ErrKindContextLength, Provider: "a", Model: "small",
		},
	}
	alsoSmall := &scriptedFBProvider{name: "b"}
	big := &scriptedFBProvider{
		name: "c",
		resp: chat.Response{Ref: chat.ModelRef{Provider: "c", Model: "big"}, Message: chat.AssistantText("ok")},
	}
	cat := chat.NewCatalog()
	cat.Register(chat.ModelInfo{Ref: chat.ModelRef{Provider: "a", Model: "small"}, ContextTokens: 8000})
	cat.Register(chat.ModelInfo{Ref: chat.ModelRef{Provider: "b", Model: "alsosmall"}, ContextTokens: 8000})
	cat.Register(chat.ModelInfo{Ref: chat.ModelRef{Provider: "c", Model: "big"}, ContextTokens: 400000})

	h := chat.New(
		chat.WithProvider(tooSmall),
		chat.WithProvider(alsoSmall),
		chat.WithProvider(big),
		chat.WithCatalog(cat),
		chat.WithRouter(&staticRouter{refs: []chat.ModelRef{
			{Provider: "a", Model: "small"},
			{Provider: "b", Model: "alsosmall"},
			{Provider: "c", Model: "big"},
		}}),
		chat.WithFallback(chat.FallbackPolicy{
			FallbackOnKinds: map[chat.ErrorKind]bool{chat.ErrKindContextLength: true},
		}),
	)
	resp, err := h.Send(context.Background(), chat.Request{
		Messages: []chat.Message{chat.UserText("huge prompt")},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if alsoSmall.calls != 0 {
		t.Errorf("should skip same-size fallback: b.calls=%d", alsoSmall.calls)
	}
	if big.calls != 1 {
		t.Errorf("should call bigger model: c.calls=%d", big.calls)
	}
	if resp.Ref.Provider != "c" {
		t.Errorf("resp.ref=%+v", resp.Ref)
	}
}
