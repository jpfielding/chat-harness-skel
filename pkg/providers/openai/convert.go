package openai

import (
	"encoding/json"
	"fmt"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// wireChatRequest mirrors the OpenAI /v1/chat/completions request body.
type wireChatRequest struct {
	Model       string          `json:"model"`
	Messages    []wireMessage   `json:"messages"`
	Tools       []wireTool      `json:"tools,omitempty"`
	ToolChoice  any             `json:"tool_choice,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
	Seed        *int64          `json:"seed,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Name       string         `json:"name,omitempty"`
	Content    any            `json:"content,omitempty"` // string | []wirePart | null
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// wirePart is a multimodal part used when Content is an array.
type wirePart struct {
	Type     string        `json:"type"` // "text" | "image_url"
	Text     string        `json:"text,omitempty"`
	ImageURL *wireImageURL `json:"image_url,omitempty"`
}

type wireImageURL struct {
	URL string `json:"url"`
}

type wireTool struct {
	Type     string           `json:"type"` // always "function" for now
	Function wireFunctionSpec `json:"function"`
}

type wireFunctionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // "function"
	Function wireFunctionCall `json:"function"`
}

type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded string
}

type wireChatCompletionResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Model   string       `json:"model"`
	Choices []wireChoice `json:"choices"`
	Usage   wireUsage    `json:"usage"`
}

type wireChoice struct {
	Index        int         `json:"index"`
	Message      wireMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// buildChatRequest converts a normalized chat.Request into an OpenAI wire
// request. System prompts are modeled as role:"system" messages. RoleTool
// messages with BlockToolResult content are converted to role:"tool"
// messages with their tool_call_id.
func buildChatRequest(req chat.Request, ref chat.ModelRef) (*wireChatRequest, error) {
	out := &wireChatRequest{Model: ref.Model}

	if req.System != "" {
		out.Messages = append(out.Messages, wireMessage{
			Role: "system", Content: req.System,
		})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case chat.RoleSystem:
			text := collectText(m.Content)
			out.Messages = append(out.Messages, wireMessage{Role: "system", Content: text})
		case chat.RoleUser:
			content, err := userContent(m.Content)
			if err != nil {
				return nil, err
			}
			out.Messages = append(out.Messages, wireMessage{Role: "user", Content: content, Name: m.Name})
		case chat.RoleAssistant:
			wm, err := assistantContent(m.Content)
			if err != nil {
				return nil, err
			}
			out.Messages = append(out.Messages, wm)
		case chat.RoleTool:
			// One role:"tool" message per ToolResult block.
			for _, b := range m.Content {
				if b.Kind != chat.BlockToolResult || b.ToolResult == nil {
					return nil, fmt.Errorf("tool-role message must contain only tool_result blocks")
				}
				text := collectText(b.ToolResult.Content)
				out.Messages = append(out.Messages, wireMessage{
					Role:       "tool",
					ToolCallID: b.ToolResult.ToolUseID,
					Content:    text,
				})
			}
		default:
			return nil, fmt.Errorf("unsupported role %q", m.Role)
		}
	}

	for _, ts := range req.Tools {
		out.Tools = append(out.Tools, wireTool{
			Type: "function",
			Function: wireFunctionSpec{
				Name:        ts.Name,
				Description: ts.Description,
				Parameters:  ts.InputSchema,
			},
		})
	}

	if req.ToolChoice != nil {
		out.ToolChoice = convertToolChoice(req.ToolChoice)
	}

	out.MaxTokens = req.Params.MaxTokens
	if req.Params.Temperature != nil {
		t := *req.Params.Temperature
		out.Temperature = &t
	}
	if req.Params.TopP != nil {
		t := *req.Params.TopP
		out.TopP = &t
	}
	if len(req.Params.Stop) > 0 {
		out.Stop = req.Params.Stop
	}
	if req.Params.Seed != nil {
		s := *req.Params.Seed
		out.Seed = &s
	}
	return out, nil
}

// userContent returns either a plain string (simple text-only) or an array
// of parts (when images are present). This matches OpenAI's shape.
func userContent(blocks []chat.ContentBlock) (any, error) {
	// Fast path: single text block → plain string.
	if len(blocks) == 1 && blocks[0].Kind == chat.BlockText {
		return blocks[0].Text, nil
	}
	parts := make([]wirePart, 0, len(blocks))
	for _, b := range blocks {
		switch b.Kind {
		case chat.BlockText:
			parts = append(parts, wirePart{Type: "text", Text: b.Text})
		case chat.BlockImage:
			if b.Image == nil {
				return nil, fmt.Errorf("image block missing image")
			}
			var url string
			if b.Image.URL != "" {
				url = b.Image.URL
			} else if b.Image.Base64 != "" {
				url = fmt.Sprintf("data:%s;base64,%s", b.Image.MediaType, b.Image.Base64)
			} else {
				return nil, fmt.Errorf("image block has neither url nor base64")
			}
			parts = append(parts, wirePart{Type: "image_url", ImageURL: &wireImageURL{URL: url}})
		default:
			return nil, fmt.Errorf("user message cannot carry block kind %q", b.Kind)
		}
	}
	return parts, nil
}

// assistantContent builds a role:"assistant" wireMessage from normalized
// content blocks. Text blocks collapse into Content; BlockToolUse blocks
// map onto ToolCalls.
func assistantContent(blocks []chat.ContentBlock) (wireMessage, error) {
	wm := wireMessage{Role: "assistant"}
	var textBuf string
	for _, b := range blocks {
		switch b.Kind {
		case chat.BlockText:
			textBuf += b.Text
		case chat.BlockThinking:
			// OpenAI doesn't accept assistant thinking blocks on input.
			// Drop silently (mirrors how we'd drop Anthropic-specific hints
			// for OpenAI).
			continue
		case chat.BlockToolUse:
			if b.ToolUse == nil {
				return wm, fmt.Errorf("tool_use block missing payload")
			}
			args := b.ToolUse.RawInput
			if args == "" && b.ToolUse.ParsedInput != nil {
				enc, err := json.Marshal(b.ToolUse.ParsedInput)
				if err != nil {
					return wm, err
				}
				args = string(enc)
			}
			if args == "" {
				args = "{}"
			}
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   b.ToolUse.ID,
				Type: "function",
				Function: wireFunctionCall{
					Name:      b.ToolUse.Name,
					Arguments: args,
				},
			})
		default:
			return wm, fmt.Errorf("assistant message cannot carry block kind %q", b.Kind)
		}
	}
	if textBuf != "" {
		wm.Content = textBuf
	} else if len(wm.ToolCalls) == 0 {
		// assistant with no content and no tool calls — pass empty string so
		// the API accepts the message.
		wm.Content = ""
	}
	return wm, nil
}

func collectText(blocks []chat.ContentBlock) string {
	var out string
	for _, b := range blocks {
		if b.Kind == chat.BlockText {
			out += b.Text
		}
	}
	return out
}

// wireToolChoice converts our ToolChoice into OpenAI's polymorphic shape.
// "auto" and "none" are bare strings; "tool" is an object.
func convertToolChoice(tc *chat.ToolChoice) any {
	switch tc.Mode {
	case chat.ToolChoiceAuto:
		return "auto"
	case chat.ToolChoiceNone:
		return "none"
	case chat.ToolChoiceAny:
		return "required"
	case chat.ToolChoiceTool:
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.ToolName},
		}
	}
	return nil
}

// convertResponse converts an OpenAI chat completion response into normalized.
// Parallel tool_calls[] become multiple BlockToolUse blocks in the assistant
// message.
func convertResponse(ref chat.ModelRef, wire wireChatCompletionResponse) chat.Response {
	out := chat.Response{
		ID:  wire.ID,
		Ref: ref,
		Usage: chat.Usage{
			InputTokens:  wire.Usage.PromptTokens,
			OutputTokens: wire.Usage.CompletionTokens,
		},
	}
	if len(wire.Choices) == 0 {
		out.StopReason = chat.StopError
		out.Message = chat.Message{Role: chat.RoleAssistant}
		return out
	}
	ch := wire.Choices[0]

	assistant := chat.Message{Role: chat.RoleAssistant}

	// Content field: may be a string, an array of parts, or null.
	switch v := ch.Message.Content.(type) {
	case string:
		if v != "" {
			assistant.Content = append(assistant.Content, chat.TextBlock(v))
		}
	case []any:
		for _, part := range v {
			if m, ok := part.(map[string]any); ok {
				if s, _ := m["text"].(string); s != "" {
					assistant.Content = append(assistant.Content, chat.TextBlock(s))
				}
			}
		}
	}

	for _, tc := range ch.Message.ToolCalls {
		tu := &chat.ToolUse{
			ID:       tc.ID,
			Name:     tc.Function.Name,
			RawInput: tc.Function.Arguments,
		}
		if tc.Function.Arguments != "" {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsed); err == nil {
				tu.ParsedInput = parsed
			} else {
				tu.ParseError = err.Error()
			}
		}
		assistant.Content = append(assistant.Content, chat.ContentBlock{
			Kind:    chat.BlockToolUse,
			ToolUse: tu,
		})
	}

	out.Message = assistant
	out.StopReason = mapFinishReason(ch.FinishReason)
	return out
}

func mapFinishReason(s string) chat.StopReason {
	switch s {
	case "stop":
		return chat.StopEnd
	case "length":
		return chat.StopMaxTokens
	case "tool_calls":
		return chat.StopToolUse
	case "content_filter":
		return chat.StopError
	case "":
		return chat.StopEnd
	default:
		return chat.StopEnd
	}
}
