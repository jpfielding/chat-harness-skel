package router

import (
	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// RequestCapabilities enumerates features a request requires from a model.
// Required is computed from the shape of the request; Satisfies checks a
// ModelInfo against it.
type RequestCapabilities struct {
	NeedsTools         bool
	NeedsParallelTools bool
	NeedsVision        bool
	NeedsJSONSchema    bool
	NeedsThinking      bool
	HasImages          bool
	HasToolResults     bool
	// EstimatedInputTokens is a rough heuristic (len(text)/4). Used only for
	// context-length comparisons; never for billing.
	EstimatedInputTokens  int
	RequestedOutputTokens int
}

// Required derives a fingerprint from req by inspecting messages and tools.
func Required(req chat.Request) RequestCapabilities {
	var rc RequestCapabilities
	rc.NeedsTools = len(req.Tools) > 0
	if req.Params.Thinking != nil && req.Params.Thinking.Enabled {
		rc.NeedsThinking = true
	}
	rc.RequestedOutputTokens = req.Params.MaxTokens

	parallelToolUses := 0
	for _, m := range req.Messages {
		perMsgToolUses := 0
		for _, b := range m.Content {
			switch b.Kind {
			case chat.BlockImage:
				rc.NeedsVision = true
				rc.HasImages = true
			case chat.BlockToolUse:
				perMsgToolUses++
			case chat.BlockToolResult:
				rc.HasToolResults = true
			case chat.BlockText:
				rc.EstimatedInputTokens += len(b.Text) / 4
			}
		}
		if perMsgToolUses > parallelToolUses {
			parallelToolUses = perMsgToolUses
		}
	}
	if parallelToolUses >= 2 {
		rc.NeedsParallelTools = true
		rc.NeedsTools = true
	}
	return rc
}

// Satisfies reports whether m's Capabilities and ContextTokens are
// sufficient for rc.
func (rc RequestCapabilities) Satisfies(m chat.ModelInfo) bool {
	if rc.NeedsTools && !m.Capabilities.Tools {
		return false
	}
	if rc.NeedsParallelTools && !m.Capabilities.ParallelTools {
		return false
	}
	if rc.NeedsVision && !m.Capabilities.Vision {
		return false
	}
	if rc.NeedsJSONSchema && !m.Capabilities.JSONSchemaMode {
		return false
	}
	if rc.NeedsThinking && !m.Capabilities.Thinking {
		return false
	}
	// Context-length check: assume ~500 tokens of overhead for system+format.
	need := rc.EstimatedInputTokens + 500
	if rc.RequestedOutputTokens > 0 {
		need += rc.RequestedOutputTokens
	}
	if m.ContextTokens > 0 && need > m.ContextTokens {
		return false
	}
	return true
}
