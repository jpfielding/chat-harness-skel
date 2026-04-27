package chat

import (
	"fmt"
	"strings"
)

// ValidationError is returned by Validate when a request fails preflight.
// Multiple issues are joined into one error message.
type ValidationError struct {
	Issues []string
}

func (e *ValidationError) Error() string {
	return "invalid request: " + strings.Join(e.Issues, "; ")
}

// Validate performs provider-agnostic preflight checks on req. It catches
// shape errors (empty messages, missing required fields, mismatched tool
// results) before the router and adapters see the request.
func Validate(req Request) error {
	var issues []string

	if len(req.Messages) == 0 && req.System == "" {
		issues = append(issues, "request has no messages and no system prompt")
	}

	if req.Model == "" && req.Policy == "" {
		// Harness will fall back to default policy — not an error here.
	} else if req.Model != "" {
		if _, err := ParseModelRef(req.Model); err != nil {
			// Allow bare aliases; the catalog resolver handles those. We only
			// reject obviously malformed refs.
			if strings.ContainsRune(req.Model, ':') {
				issues = append(issues, err.Error())
			}
		}
	}

	if req.ToolChoice != nil {
		switch req.ToolChoice.Mode {
		case ToolChoiceAuto, ToolChoiceAny, ToolChoiceNone:
			// ok
		case ToolChoiceTool:
			if req.ToolChoice.ToolName == "" {
				issues = append(issues, "tool_choice mode=tool requires tool_name")
			}
		default:
			issues = append(issues, fmt.Sprintf("unknown tool_choice mode %q", req.ToolChoice.Mode))
		}
	}

	// Validate content blocks: exactly one populated field per Kind.
	for i, m := range req.Messages {
		for j, b := range m.Content {
			if err := validateBlock(b); err != nil {
				issues = append(issues, fmt.Sprintf("message[%d].content[%d]: %v", i, j, err))
			}
		}
	}

	// Cross-message check: every tool_result must reference a prior tool_use id.
	seenUses := map[string]bool{}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Kind == BlockToolUse && b.ToolUse != nil {
				seenUses[b.ToolUse.ID] = true
			}
			if b.Kind == BlockToolResult && b.ToolResult != nil {
				if !seenUses[b.ToolResult.ToolUseID] {
					issues = append(issues, fmt.Sprintf("tool_result references unknown tool_use id %q", b.ToolResult.ToolUseID))
				}
			}
		}
	}

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

func validateBlock(b ContentBlock) error {
	switch b.Kind {
	case BlockText:
		// Text may be empty; that's fine for streaming placeholders.
	case BlockImage:
		if b.Image == nil {
			return fmt.Errorf("image block missing image")
		}
		if b.Image.URL == "" && b.Image.Base64 == "" {
			return fmt.Errorf("image block has neither url nor base64")
		}
		if b.Image.URL != "" && b.Image.Base64 != "" {
			return fmt.Errorf("image block has both url and base64")
		}
		if b.Image.MediaType == "" {
			return fmt.Errorf("image block missing media_type")
		}
	case BlockToolUse:
		if b.ToolUse == nil {
			return fmt.Errorf("tool_use block missing tool_use")
		}
		if b.ToolUse.Name == "" {
			return fmt.Errorf("tool_use missing name")
		}
	case BlockToolResult:
		if b.ToolResult == nil {
			return fmt.Errorf("tool_result block missing tool_result")
		}
		if b.ToolResult.ToolUseID == "" {
			return fmt.Errorf("tool_result missing tool_use_id")
		}
	case BlockThinking:
		// thinking may be empty; providers that don't support it ignore.
	case "":
		return fmt.Errorf("block has empty kind")
	default:
		return fmt.Errorf("unknown block kind %q", b.Kind)
	}
	return nil
}
