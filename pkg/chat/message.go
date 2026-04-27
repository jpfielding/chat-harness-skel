package chat

// Role identifies who produced a Message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// RoleTool carries ToolResult content blocks only. Adapters translate
	// to/from provider-native shapes (OpenAI role:"tool", Anthropic user
	// message with tool_result blocks) at the adapter boundary.
	RoleTool Role = "tool"
)

// BlockKind is the discriminator for ContentBlock.
type BlockKind string

const (
	BlockText       BlockKind = "text"
	BlockImage      BlockKind = "image"
	BlockToolUse    BlockKind = "tool_use"
	BlockToolResult BlockKind = "tool_result"
	BlockThinking   BlockKind = "thinking"
)

// Message is one turn in a conversation.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
	// Name is optional per-message metadata (e.g., OpenAI's `name` field).
	Name string `json:"name,omitempty"`
}

// ContentBlock is a tagged union of content kinds. Exactly one of the
// kind-specific fields is populated depending on Kind.
type ContentBlock struct {
	Kind BlockKind `json:"type"`

	Text       string       `json:"text,omitempty"`
	Image      *ImageSource `json:"image,omitempty"`
	ToolUse    *ToolUse     `json:"tool_use,omitempty"`
	ToolResult *ToolResult  `json:"tool_result,omitempty"`
	Thinking   string       `json:"thinking,omitempty"`

	// ProviderMetadata carries provider-specific fields that do not portably
	// round-trip (Anthropic cache_control, OpenAI response_format hints, etc.).
	// The router and fallback logic do NOT preserve semantics that depend on
	// fields here. Callers who read keys from ProviderMetadata are outside the
	// normalized contract.
	ProviderMetadata map[string]any `json:"provider_metadata,omitempty"`
}

// ImageSource describes an inline or referenced image.
type ImageSource struct {
	MediaType string `json:"media_type"`

	// Exactly one of URL or Base64 should be set.
	URL    string `json:"url,omitempty"`
	Base64 string `json:"base64,omitempty"`
}

// TextBlock returns a ContentBlock of kind text.
func TextBlock(s string) ContentBlock {
	return ContentBlock{Kind: BlockText, Text: s}
}

// UserText is a convenience constructor for a user message containing one
// text block.
func UserText(s string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{TextBlock(s)}}
}

// AssistantText is a convenience constructor for an assistant message
// containing one text block.
func AssistantText(s string) Message {
	return Message{Role: RoleAssistant, Content: []ContentBlock{TextBlock(s)}}
}
