package chat

import "time"

// StopReason is the normalized reason a turn ended.
type StopReason string

const (
	StopEnd       StopReason = "end_turn"
	StopMaxTokens StopReason = "max_tokens"
	StopStop      StopReason = "stop_sequence"
	StopToolUse   StopReason = "tool_use"
	StopError     StopReason = "error"
	StopCanceled  StopReason = "canceled"
)

// Usage reports token accounting. Not all providers report every field;
// unreported values are zero. Cross-provider comparisons are approximate.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// Attempt records one provider call made while serving a Request. When the
// router falls back, Response.Attempts contains one entry per candidate tried.
type Attempt struct {
	Provider  string         `json:"provider"`
	Model     string         `json:"model"`
	Error     *ProviderError `json:"error,omitempty"`
	Latency   time.Duration  `json:"latency_ns"`
	RequestID string         `json:"request_id,omitempty"`
}

// Response is the normalized reply from a single successful chat turn.
type Response struct {
	ID         string        `json:"id"`
	Ref        ModelRef      `json:"ref"`
	Message    Message       `json:"message"` // assistant-role, one or more content blocks
	StopReason StopReason    `json:"stop_reason"`
	Usage      Usage         `json:"usage"`
	Latency    time.Duration `json:"latency_ns"`
	Attempts   []Attempt     `json:"attempts,omitempty"`
}
