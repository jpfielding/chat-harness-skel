package chat

// EventKind tags a StreamEvent.
type EventKind string

const (
	EvMessageStart EventKind = "message_start"
	EvBlockStart   EventKind = "block_start"
	EvBlockDelta   EventKind = "block_delta"
	EvBlockStop    EventKind = "block_stop"
	EvMessageDelta EventKind = "message_delta"
	EvMessageStop  EventKind = "message_stop"
	EvError        EventKind = "error"
	EvPing         EventKind = "ping"
)

// StreamEvent is one unit of a normalized streaming response. Consumers can
// accumulate TextDelta for text blocks and RawInputDelta for tool_use blocks
// between BlockStart and BlockStop events at the same Index.
//
// By BlockStop, the accumulated RawInputDelta for a tool_use block is
// whatever the model emitted — it is NOT guaranteed to be valid JSON.
// Callers wanting strict validity must parse and check; the adapter exposes
// ParseError on the final ToolUse when assembled.
type StreamEvent struct {
	Kind EventKind `json:"type"`

	// Index identifies the content block within the current message. Stable
	// across Start/Delta/Stop for the same block. 0 for non-block events.
	Index int `json:"index,omitempty"`

	// Block is populated on BlockStart as a skeleton of the incoming block
	// (Kind, initial metadata). Delta events carry only deltas.
	Block *ContentBlock `json:"block,omitempty"`

	TextDelta     string `json:"text_delta,omitempty"`
	RawInputDelta string `json:"raw_input_delta,omitempty"`

	StopReason StopReason     `json:"stop_reason,omitempty"`
	Usage      *Usage         `json:"usage,omitempty"`
	Err        *ProviderError `json:"error,omitempty"`
}

// StreamState is the lifecycle state of a StreamReader. A well-behaved
// reader transitions: Opened -> ProviderStarted -> PartialOutput -> Completed
// (or Canceled / Failed). StreamReader.State() returns the latest.
type StreamState string

const (
	StreamOpened          StreamState = "opened"
	StreamProviderStarted StreamState = "provider_started"
	StreamPartialOutput   StreamState = "partial_output"
	StreamCompleted       StreamState = "completed"
	StreamCanceled        StreamState = "canceled"
	StreamFailed          StreamState = "failed"
)

// StreamReader delivers StreamEvents from a provider, one at a time. Callers
// must drain until io.EOF (or a non-nil error) and then call Close.
//
// State returns the current lifecycle state and is safe to call concurrently
// with Next.
type StreamReader interface {
	Next() (StreamEvent, error)
	State() StreamState
	Close() error
}
