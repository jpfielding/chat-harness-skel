package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// wireMessagesRequest mirrors the Anthropic /v1/messages request body shape.
type wireMessagesRequest struct {
	Model       string         `json:"model"`
	MaxTokens   int            `json:"max_tokens"`
	System      string         `json:"system,omitempty"`
	Messages    []wireMessage  `json:"messages"`
	Tools       []wireToolSpec `json:"tools,omitempty"`
	ToolChoice  *wireToolChoice `json:"tool_choice,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	TopP        *float64       `json:"top_p,omitempty"`
	StopSeq     []string       `json:"stop_sequences,omitempty"`
	Stream      bool           `json:"stream,omitempty"`
}

type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

type wireBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// image
	Source *wireImageSource `json:"source,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   []wireBlock `json:"content,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`
}

type wireImageSource struct {
	Type      string `json:"type"` // "base64" or "url"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type wireToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type wireToolChoice struct {
	Type string `json:"type"`           // "auto" | "any" | "none" | "tool"
	Name string `json:"name,omitempty"` // when Type == "tool"
}

type wireMessagesResponse struct {
	ID           string      `json:"id"`
	Type         string      `json:"type"`
	Role         string      `json:"role"`
	Model        string      `json:"model"`
	Content      []wireBlock `json:"content"`
	StopReason   string      `json:"stop_reason"`
	StopSequence string      `json:"stop_sequence,omitempty"`
	Usage        wireUsage   `json:"usage"`
}

type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// buildMessagesRequest converts a normalized chat.Request into an Anthropic
// wire request for the given model. System is merged with any RoleSystem
// messages; RoleTool messages become user messages carrying tool_result
// blocks (Anthropic's convention).
func buildMessagesRequest(req chat.Request, ref chat.ModelRef) (*wireMessagesRequest, error) {
	out := &wireMessagesRequest{
		Model:     ref.Model,
		MaxTokens: req.Params.MaxTokens,
	}
	if out.MaxTokens == 0 {
		out.MaxTokens = 1024
	}

	sys := req.System
	for _, m := range req.Messages {
		if m.Role == chat.RoleSystem {
			for _, b := range m.Content {
				if b.Kind == chat.BlockText {
					if sys != "" {
						sys += "\n"
					}
					sys += b.Text
				}
			}
		}
	}
	out.System = sys

	for _, m := range req.Messages {
		switch m.Role {
		case chat.RoleSystem:
			continue // merged into System above
		case chat.RoleUser, chat.RoleAssistant:
			wm, err := convertMessageToWire(string(m.Role), m.Content)
			if err != nil {
				return nil, err
			}
			out.Messages = append(out.Messages, wm)
		case chat.RoleTool:
			// Translate a RoleTool message into a user-role message carrying
			// tool_result blocks.
			wm, err := convertMessageToWire("user", m.Content)
			if err != nil {
				return nil, err
			}
			out.Messages = append(out.Messages, wm)
		default:
			return nil, fmt.Errorf("unsupported role %q", m.Role)
		}
	}

	for _, ts := range req.Tools {
		out.Tools = append(out.Tools, wireToolSpec{
			Name:        ts.Name,
			Description: ts.Description,
			InputSchema: ts.InputSchema,
		})
	}

	if req.ToolChoice != nil {
		out.ToolChoice = convertToolChoice(req.ToolChoice)
	}

	if req.Params.Temperature != nil {
		t := *req.Params.Temperature
		out.Temperature = &t
	}
	if req.Params.TopP != nil {
		t := *req.Params.TopP
		out.TopP = &t
	}
	if len(req.Params.Stop) > 0 {
		out.StopSeq = req.Params.Stop
	}
	return out, nil
}

func convertMessageToWire(role string, content []chat.ContentBlock) (wireMessage, error) {
	wm := wireMessage{Role: role}
	for _, b := range content {
		wb, err := blockToWire(b)
		if err != nil {
			return wm, err
		}
		wm.Content = append(wm.Content, wb)
	}
	return wm, nil
}

func blockToWire(b chat.ContentBlock) (wireBlock, error) {
	switch b.Kind {
	case chat.BlockText:
		return wireBlock{Type: "text", Text: b.Text}, nil
	case chat.BlockImage:
		if b.Image == nil {
			return wireBlock{}, fmt.Errorf("image block missing image")
		}
		src := &wireImageSource{MediaType: b.Image.MediaType}
		switch {
		case b.Image.Base64 != "":
			src.Type = "base64"
			src.Data = b.Image.Base64
		case b.Image.URL != "":
			src.Type = "url"
			src.URL = b.Image.URL
		default:
			return wireBlock{}, fmt.Errorf("image block missing url/base64")
		}
		return wireBlock{Type: "image", Source: src}, nil
	case chat.BlockToolUse:
		if b.ToolUse == nil {
			return wireBlock{}, fmt.Errorf("tool_use missing payload")
		}
		var input json.RawMessage
		if b.ToolUse.ParsedInput != nil {
			enc, err := json.Marshal(b.ToolUse.ParsedInput)
			if err != nil {
				return wireBlock{}, err
			}
			input = enc
		} else if b.ToolUse.RawInput != "" {
			input = json.RawMessage(b.ToolUse.RawInput)
		} else {
			input = json.RawMessage("{}")
		}
		return wireBlock{
			Type:  "tool_use",
			ID:    b.ToolUse.ID,
			Name:  b.ToolUse.Name,
			Input: input,
		}, nil
	case chat.BlockToolResult:
		if b.ToolResult == nil {
			return wireBlock{}, fmt.Errorf("tool_result missing payload")
		}
		inner := make([]wireBlock, 0, len(b.ToolResult.Content))
		for _, c := range b.ToolResult.Content {
			wb, err := blockToWire(c)
			if err != nil {
				return wireBlock{}, err
			}
			inner = append(inner, wb)
		}
		return wireBlock{
			Type:      "tool_result",
			ToolUseID: b.ToolResult.ToolUseID,
			Content:   inner,
			IsError:   b.ToolResult.IsError,
		}, nil
	case chat.BlockThinking:
		// Pass through as thinking if Raw carries it; otherwise skip.
		return wireBlock{Type: "thinking", Text: b.Thinking}, nil
	default:
		return wireBlock{}, fmt.Errorf("unsupported block kind %q", b.Kind)
	}
}

func convertToolChoice(tc *chat.ToolChoice) *wireToolChoice {
	switch tc.Mode {
	case chat.ToolChoiceAuto:
		return &wireToolChoice{Type: "auto"}
	case chat.ToolChoiceAny:
		return &wireToolChoice{Type: "any"}
	case chat.ToolChoiceNone:
		return &wireToolChoice{Type: "none"}
	case chat.ToolChoiceTool:
		return &wireToolChoice{Type: "tool", Name: tc.ToolName}
	}
	return nil
}

// convertResponse converts an Anthropic /v1/messages response to normalized.
func convertResponse(ref chat.ModelRef, wire wireMessagesResponse) chat.Response {
	assistant := chat.Message{Role: chat.RoleAssistant}
	for _, b := range wire.Content {
		nb, err := wireToBlock(b)
		if err != nil {
			// Fall back to carrying the raw as provider metadata so info is
			// not silently dropped.
			assistant.Content = append(assistant.Content, chat.ContentBlock{
				Kind:             chat.BlockText,
				Text:             "",
				ProviderMetadata: map[string]any{"anthropic_unknown_block": b},
			})
			continue
		}
		assistant.Content = append(assistant.Content, nb)
	}
	return chat.Response{
		ID:         wire.ID,
		Ref:        ref,
		Message:    assistant,
		StopReason: mapStopReason(wire.StopReason),
		Usage: chat.Usage{
			InputTokens:      wire.Usage.InputTokens,
			OutputTokens:     wire.Usage.OutputTokens,
			CacheReadTokens:  wire.Usage.CacheReadInputTokens,
			CacheWriteTokens: wire.Usage.CacheCreationInputTokens,
		},
	}
}

func wireToBlock(b wireBlock) (chat.ContentBlock, error) {
	switch b.Type {
	case "text":
		return chat.ContentBlock{Kind: chat.BlockText, Text: b.Text}, nil
	case "thinking":
		return chat.ContentBlock{Kind: chat.BlockThinking, Thinking: b.Text}, nil
	case "tool_use":
		tu := &chat.ToolUse{ID: b.ID, Name: b.Name}
		if len(b.Input) > 0 {
			tu.RawInput = string(b.Input)
			var parsed map[string]any
			if err := json.Unmarshal(b.Input, &parsed); err == nil {
				tu.ParsedInput = parsed
			} else {
				tu.ParseError = err.Error()
			}
		}
		return chat.ContentBlock{Kind: chat.BlockToolUse, ToolUse: tu}, nil
	case "image":
		// Assistant-side image responses are rare but handle defensively.
		if b.Source == nil {
			return chat.ContentBlock{}, fmt.Errorf("image block missing source")
		}
		img := &chat.ImageSource{MediaType: b.Source.MediaType}
		if b.Source.Type == "base64" {
			img.Base64 = b.Source.Data
		} else {
			img.URL = b.Source.URL
		}
		return chat.ContentBlock{Kind: chat.BlockImage, Image: img}, nil
	default:
		return chat.ContentBlock{}, fmt.Errorf("unknown block type %q", b.Type)
	}
}

func mapStopReason(s string) chat.StopReason {
	switch s {
	case "end_turn":
		return chat.StopEnd
	case "max_tokens":
		return chat.StopMaxTokens
	case "stop_sequence":
		return chat.StopStop
	case "tool_use":
		return chat.StopToolUse
	case "":
		return chat.StopEnd
	default:
		return chat.StopEnd
	}
}
