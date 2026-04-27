package chat

import (
	"errors"
	"strings"
	"testing"
)

func TestValidate_OK(t *testing.T) {
	req := Request{
		Model:    "anthropic:claude-opus-4-5",
		Messages: []Message{UserText("hello")},
	}
	if err := Validate(req); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestValidate_EmptyMessagesAndSystem(t *testing.T) {
	req := Request{Model: "openai:gpt-5"}
	err := Validate(req)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "no messages") {
		t.Errorf("unexpected message: %v", err)
	}
}

func TestValidate_BadImageBlock(t *testing.T) {
	req := Request{
		Messages: []Message{{
			Role: RoleUser,
			Content: []ContentBlock{
				{Kind: BlockImage, Image: &ImageSource{MediaType: "image/png"}},
			},
		}},
	}
	err := Validate(req)
	if err == nil {
		t.Fatal("expected validation error for image without url/base64")
	}
}

func TestValidate_ToolResultWithoutToolUse(t *testing.T) {
	req := Request{
		Messages: []Message{{
			Role: RoleTool,
			Content: []ContentBlock{
				{Kind: BlockToolResult, ToolResult: &ToolResult{ToolUseID: "toolu_missing"}},
			},
		}},
	}
	err := Validate(req)
	if err == nil {
		t.Fatal("expected validation error for orphan tool_result")
	}
}

func TestValidate_ToolResultWithPriorToolUse(t *testing.T) {
	req := Request{
		Messages: []Message{
			{
				Role: RoleAssistant,
				Content: []ContentBlock{
					{Kind: BlockToolUse, ToolUse: &ToolUse{ID: "toolu_1", Name: "lookup"}},
				},
			},
			{
				Role: RoleTool,
				Content: []ContentBlock{
					{Kind: BlockToolResult, ToolResult: &ToolResult{ToolUseID: "toolu_1", Content: []ContentBlock{TextBlock("ok")}}},
				},
			},
		},
	}
	if err := Validate(req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_ToolChoiceMissingName(t *testing.T) {
	req := Request{
		Messages:   []Message{UserText("hi")},
		ToolChoice: &ToolChoice{Mode: ToolChoiceTool},
	}
	err := Validate(req)
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestProviderError_Is(t *testing.T) {
	e := &ProviderError{Kind: ErrKindRateLimit, Provider: "openai"}
	if !errors.Is(e, &ProviderError{Kind: ErrKindRateLimit}) {
		t.Errorf("errors.Is(rate_limit) failed")
	}
	if errors.Is(e, &ProviderError{Kind: ErrKindTimeout}) {
		t.Errorf("errors.Is(timeout) should be false")
	}
	// An empty-Kind target matches any ProviderError.
	if !errors.Is(e, &ProviderError{}) {
		t.Errorf("errors.Is(any-provider-error) should match")
	}
}
