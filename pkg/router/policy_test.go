package router

import (
	"context"
	"testing"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

func TestPolicyRouter_ExplicitModelBypassesPolicy(t *testing.T) {
	r, err := NewPolicyRouter(chat.NewCatalog(), "fast", []Policy{
		{Name: "fast", Candidates: []chat.ModelRef{{Provider: "openai", Model: "gpt-5-mini"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := r.Pick(context.Background(), chat.Request{Model: "anthropic:claude-opus-4-5"})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Provider != "anthropic" {
		t.Errorf("unexpected: %+v", refs)
	}
}

func TestPolicyRouter_FiltersByCapabilities(t *testing.T) {
	cat := chat.NewCatalog()
	cat.Register(chat.ModelInfo{
		Ref:           chat.ModelRef{Provider: "ollama", Model: "llama-noop"},
		ContextTokens: 100000,
		Capabilities:  chat.Capabilities{}, // no tools
	})
	cat.Register(chat.ModelInfo{
		Ref:           chat.ModelRef{Provider: "openai", Model: "gpt-5"},
		ContextTokens: 400000,
		Capabilities:  chat.Capabilities{Tools: true, ParallelTools: true},
	})

	r, err := NewPolicyRouter(cat, "fast", []Policy{
		{Name: "fast", Candidates: []chat.ModelRef{
			{Provider: "ollama", Model: "llama-noop"},
			{Provider: "openai", Model: "gpt-5"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Request with tools should filter out the no-tools model.
	refs, err := r.Pick(context.Background(), chat.Request{
		Policy:   "fast",
		Messages: []chat.Message{chat.UserText("x")},
		Tools:    []chat.ToolSpec{{Name: "t"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Provider != "openai" {
		t.Errorf("capability filtering failed: %+v", refs)
	}
}

func TestPolicyRouter_DefaultPolicy(t *testing.T) {
	r, _ := NewPolicyRouter(chat.NewCatalog(), "fast", []Policy{
		{Name: "fast", Candidates: []chat.ModelRef{{Provider: "o", Model: "m"}}},
	})
	refs, err := r.Pick(context.Background(), chat.Request{
		Messages: []chat.Message{chat.UserText("x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Errorf("got %d refs", len(refs))
	}
}

func TestPolicyRouter_UnknownPolicy(t *testing.T) {
	r, _ := NewPolicyRouter(chat.NewCatalog(), "", []Policy{
		{Name: "fast", Candidates: []chat.ModelRef{{Provider: "o", Model: "m"}}},
	})
	_, err := r.Pick(context.Background(), chat.Request{Policy: "ghost"})
	if err == nil {
		t.Fatal("expected error")
	}
}
