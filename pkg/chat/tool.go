package chat

// ToolSpec describes a tool the model may call. InputSchema is a JSON Schema
// document; adapters may translate it to provider-specific shapes.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// ToolUse is an assistant-produced tool invocation. IDs are provider-scoped:
// an ID produced by provider A is not meaningful when sent to provider B.
type ToolUse struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// RawInput is the raw accumulated argument text emitted by the model. It
	// is always populated by the adapter (even if empty).
	RawInput string `json:"raw_input,omitempty"`

	// ParsedInput is the result of json.Unmarshal(RawInput). nil if parsing
	// failed, in which case ParseError describes why. Callers who require
	// strict-valid JSON should check ParseError.
	ParsedInput map[string]any `json:"parsed_input,omitempty"`

	// ParseError is the parse failure message, empty if RawInput parsed.
	ParseError string `json:"parse_error,omitempty"`
}

// ToolResult is the output of tool execution, returned to the model in a
// subsequent turn.
type ToolResult struct {
	ToolUseID string         `json:"tool_use_id"`
	Content   []ContentBlock `json:"content"`
	IsError   bool           `json:"is_error,omitempty"`
}

// ToolChoiceMode controls how the model decides whether to call a tool.
type ToolChoiceMode string

const (
	ToolChoiceAuto ToolChoiceMode = "auto" // let the model decide (default)
	ToolChoiceAny  ToolChoiceMode = "any"  // force use of some tool
	ToolChoiceNone ToolChoiceMode = "none" // forbid tool use
	ToolChoiceTool ToolChoiceMode = "tool" // require a specific tool
)

// ToolChoice expresses a policy for tool selection.
type ToolChoice struct {
	Mode     ToolChoiceMode `json:"mode"`
	ToolName string         `json:"tool_name,omitempty"` // when Mode == ToolChoiceTool
}
