package router

import (
	"testing"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

func TestRequired_DerivesCapabilities(t *testing.T) {
	req := chat.Request{
		Tools: []chat.ToolSpec{{Name: "t"}},
		Messages: []chat.Message{
			{Role: chat.RoleUser, Content: []chat.ContentBlock{
				chat.TextBlock("hello world"),
				{Kind: chat.BlockImage, Image: &chat.ImageSource{URL: "x", MediaType: "image/png"}},
			}},
			{Role: chat.RoleAssistant, Content: []chat.ContentBlock{
				{Kind: chat.BlockToolUse, ToolUse: &chat.ToolUse{Name: "a"}},
				{Kind: chat.BlockToolUse, ToolUse: &chat.ToolUse{Name: "b"}},
			}},
			{Role: chat.RoleTool, Content: []chat.ContentBlock{
				{Kind: chat.BlockToolResult, ToolResult: &chat.ToolResult{ToolUseID: "1"}},
			}},
		},
		Params: chat.GenerationParams{Thinking: &chat.ThinkingConfig{Enabled: true}},
	}
	rc := Required(req)
	if !rc.NeedsTools || !rc.NeedsParallelTools {
		t.Errorf("tools/parallel not set: %+v", rc)
	}
	if !rc.NeedsVision || !rc.HasImages {
		t.Errorf("vision not set: %+v", rc)
	}
	if !rc.HasToolResults {
		t.Errorf("tool_results not set: %+v", rc)
	}
	if !rc.NeedsThinking {
		t.Errorf("thinking not set: %+v", rc)
	}
}

func TestSatisfies_ToolsRequired(t *testing.T) {
	rc := RequestCapabilities{NeedsTools: true}
	noTools := chat.ModelInfo{ContextTokens: 100000, Capabilities: chat.Capabilities{Tools: false}}
	yesTools := chat.ModelInfo{ContextTokens: 100000, Capabilities: chat.Capabilities{Tools: true}}
	if rc.Satisfies(noTools) {
		t.Error("should reject tool-less model")
	}
	if !rc.Satisfies(yesTools) {
		t.Error("should accept tool model")
	}
}

func TestSatisfies_ContextLength(t *testing.T) {
	rc := RequestCapabilities{EstimatedInputTokens: 50000, RequestedOutputTokens: 1000}
	small := chat.ModelInfo{ContextTokens: 8000, Capabilities: chat.Capabilities{}}
	large := chat.ModelInfo{ContextTokens: 200000, Capabilities: chat.Capabilities{}}
	if rc.Satisfies(small) {
		t.Error("should reject small context model")
	}
	if !rc.Satisfies(large) {
		t.Error("should accept large context model")
	}
}
