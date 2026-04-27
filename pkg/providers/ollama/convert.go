package ollama

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// wireChatRequest mirrors Ollama's /api/chat body.
type wireChatRequest struct {
	Model    string         `json:"model"`
	Messages []wireMessage  `json:"messages"`
	Tools    []wireToolSpec `json:"tools,omitempty"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

type wireMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Images    []string       `json:"images,omitempty"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
}

type wireToolSpec struct {
	Type     string           `json:"type"` // "function"
	Function wireFunctionSpec `json:"function"`
}

type wireFunctionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type wireToolCall struct {
	Function wireFunctionCall `json:"function"`
}

type wireFunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type wireChatResponse struct {
	Model           string      `json:"model"`
	CreatedAt       string      `json:"created_at"`
	Message         wireMessage `json:"message"`
	Done            bool        `json:"done"`
	DoneReason      string      `json:"done_reason,omitempty"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	EvalCount       int         `json:"eval_count"`
}

// buildChatRequest maps a normalized Request to the Ollama wire shape. The
// big simplification vs. OpenAI/Anthropic: Ollama's content is a plain
// string (no multimodal content blocks per message), with images in a
// separate `images: ["base64..."]` field.
func buildChatRequest(req chat.Request, ref chat.ModelRef, stream bool) (*wireChatRequest, error) {
	out := &wireChatRequest{Model: ref.Model, Stream: stream}

	if req.System != "" {
		out.Messages = append(out.Messages, wireMessage{Role: "system", Content: req.System})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case chat.RoleSystem:
			out.Messages = append(out.Messages, wireMessage{Role: "system", Content: collectText(m.Content)})
		case chat.RoleUser:
			wm, err := buildUserMessage(m.Content)
			if err != nil {
				return nil, err
			}
			out.Messages = append(out.Messages, wm)
		case chat.RoleAssistant:
			wm, err := buildAssistantMessage(m.Content)
			if err != nil {
				return nil, err
			}
			out.Messages = append(out.Messages, wm)
		case chat.RoleTool:
			// Ollama expresses tool results as role:"tool" with content as a string.
			// There is no tool_use_id field (Ollama does not round-trip ids).
			for _, b := range m.Content {
				if b.Kind != chat.BlockToolResult || b.ToolResult == nil {
					return nil, fmt.Errorf("tool-role message must contain only tool_result blocks")
				}
				out.Messages = append(out.Messages, wireMessage{
					Role:    "tool",
					Content: collectText(b.ToolResult.Content),
				})
			}
		default:
			return nil, fmt.Errorf("unsupported role %q", m.Role)
		}
	}

	for _, ts := range req.Tools {
		out.Tools = append(out.Tools, wireToolSpec{
			Type: "function",
			Function: wireFunctionSpec{
				Name:        ts.Name,
				Description: ts.Description,
				Parameters:  ts.InputSchema,
			},
		})
	}

	// Generation params go in `options`.
	if req.Params.Temperature != nil || req.Params.TopP != nil || len(req.Params.Stop) > 0 || req.Params.Seed != nil {
		out.Options = map[string]any{}
		if req.Params.Temperature != nil {
			out.Options["temperature"] = *req.Params.Temperature
		}
		if req.Params.TopP != nil {
			out.Options["top_p"] = *req.Params.TopP
		}
		if len(req.Params.Stop) > 0 {
			out.Options["stop"] = req.Params.Stop
		}
		if req.Params.Seed != nil {
			out.Options["seed"] = *req.Params.Seed
		}
	}
	if req.Params.MaxTokens > 0 {
		if out.Options == nil {
			out.Options = map[string]any{}
		}
		out.Options["num_predict"] = req.Params.MaxTokens
	}

	return out, nil
}

func buildUserMessage(blocks []chat.ContentBlock) (wireMessage, error) {
	wm := wireMessage{Role: "user"}
	var text strings.Builder
	for _, b := range blocks {
		switch b.Kind {
		case chat.BlockText:
			text.WriteString(b.Text)
		case chat.BlockImage:
			if b.Image == nil || b.Image.Base64 == "" {
				// Ollama accepts only base64 images, no URLs.
				return wm, fmt.Errorf("ollama: images must be base64-encoded")
			}
			wm.Images = append(wm.Images, b.Image.Base64)
		default:
			return wm, fmt.Errorf("ollama: user message cannot carry block kind %q", b.Kind)
		}
	}
	wm.Content = text.String()
	return wm, nil
}

func buildAssistantMessage(blocks []chat.ContentBlock) (wireMessage, error) {
	wm := wireMessage{Role: "assistant"}
	var text strings.Builder
	for _, b := range blocks {
		switch b.Kind {
		case chat.BlockText:
			text.WriteString(b.Text)
		case chat.BlockThinking:
			continue // not sent to Ollama
		case chat.BlockToolUse:
			if b.ToolUse == nil {
				return wm, fmt.Errorf("tool_use block missing payload")
			}
			args := b.ToolUse.ParsedInput
			if args == nil && b.ToolUse.RawInput != "" {
				if err := json.Unmarshal([]byte(b.ToolUse.RawInput), &args); err != nil {
					return wm, fmt.Errorf("tool_use RawInput not JSON: %w", err)
				}
			}
			if args == nil {
				args = map[string]any{}
			}
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				Function: wireFunctionCall{Name: b.ToolUse.Name, Arguments: args},
			})
		default:
			return wm, fmt.Errorf("ollama: assistant message cannot carry block kind %q", b.Kind)
		}
	}
	wm.Content = text.String()
	return wm, nil
}

func collectText(blocks []chat.ContentBlock) string {
	var out strings.Builder
	for _, b := range blocks {
		if b.Kind == chat.BlockText {
			out.WriteString(b.Text)
		}
	}
	return out.String()
}

// convertResponse maps an Ollama non-streaming response into a normalized one.
func convertResponse(ref chat.ModelRef, w wireChatResponse) chat.Response {
	assistant := chat.Message{Role: chat.RoleAssistant}
	if w.Message.Content != "" {
		assistant.Content = append(assistant.Content, chat.TextBlock(w.Message.Content))
	}
	for i, tc := range w.Message.ToolCalls {
		enc, _ := json.Marshal(tc.Function.Arguments)
		assistant.Content = append(assistant.Content, chat.ContentBlock{
			Kind: chat.BlockToolUse,
			ToolUse: &chat.ToolUse{
				// Ollama doesn't emit ids; synthesize one so downstream can
				// pair tool_use with tool_result within a conversation.
				ID:          fmt.Sprintf("ollama_call_%d", i),
				Name:        tc.Function.Name,
				RawInput:    string(enc),
				ParsedInput: tc.Function.Arguments,
			},
		})
	}
	return chat.Response{
		Ref:        ref,
		Message:    assistant,
		StopReason: mapDoneReason(w.DoneReason, len(w.Message.ToolCalls) > 0),
		Usage: chat.Usage{
			InputTokens:  w.PromptEvalCount,
			OutputTokens: w.EvalCount,
		},
	}
}

func mapDoneReason(s string, hasToolCalls bool) chat.StopReason {
	if hasToolCalls {
		return chat.StopToolUse
	}
	switch s {
	case "stop", "":
		return chat.StopEnd
	case "length":
		return chat.StopMaxTokens
	}
	return chat.StopEnd
}
