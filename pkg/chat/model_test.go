package chat

import "testing"

func TestParseModelRef(t *testing.T) {
	cases := []struct {
		in    string
		want  ModelRef
		isErr bool
	}{
		{"anthropic:claude-opus-4-5", ModelRef{"anthropic", "claude-opus-4-5"}, false},
		{"openai:gpt-5", ModelRef{"openai", "gpt-5"}, false},
		{"ollama:llama3.1:8b", ModelRef{"ollama", "llama3.1:8b"}, false},
		{"", ModelRef{}, true},
		{":gpt-5", ModelRef{}, true},
		{"openai:", ModelRef{}, true},
		{"no-colon", ModelRef{}, true},
	}
	for _, tc := range cases {
		got, err := ParseModelRef(tc.in)
		if tc.isErr {
			if err == nil {
				t.Errorf("ParseModelRef(%q): want error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseModelRef(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseModelRef(%q): got %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestModelRefString(t *testing.T) {
	ref := ModelRef{Provider: "anthropic", Model: "claude-opus-4-5"}
	if got := ref.String(); got != "anthropic:claude-opus-4-5" {
		t.Errorf("String() = %q", got)
	}
}

func TestCatalogRegisterLookupList(t *testing.T) {
	c := NewCatalog()
	m := ModelInfo{
		Ref:           ModelRef{"openai", "gpt-5"},
		ContextTokens: 400000,
		Capabilities:  Capabilities{Tools: true, Streaming: true},
	}
	c.Register(m)

	got, ok := c.Lookup(m.Ref)
	if !ok {
		t.Fatalf("Lookup failed")
	}
	if got.ContextTokens != 400000 {
		t.Errorf("got ContextTokens=%d", got.ContextTokens)
	}

	if list := c.List(); len(list) != 1 {
		t.Errorf("List() len=%d", len(list))
	}

	_, ok = c.Lookup(ModelRef{"openai", "gpt-9999"})
	if ok {
		t.Errorf("expected miss on unregistered ref")
	}
}

func TestModelInfoNative(t *testing.T) {
	m := ModelInfo{Ref: ModelRef{"bedrock", "claude-opus"}, NativeID: "anthropic.claude-opus-v1:0"}
	if m.Native() != "anthropic.claude-opus-v1:0" {
		t.Errorf("Native() = %q", m.Native())
	}
	m2 := ModelInfo{Ref: ModelRef{"openai", "gpt-5"}}
	if m2.Native() != "gpt-5" {
		t.Errorf("Native() fallback = %q", m2.Native())
	}
}
