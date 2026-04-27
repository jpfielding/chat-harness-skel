// Package openai implements chat.Provider against the OpenAI Chat Completions
// API.
//
// Status: experimental. Non-streaming Send is implemented in Phase 1; Stream
// is stubbed and filled in during Phase 2.
//
// The adapter converts between OpenAI's flat tool_calls[] / role:"tool" shape
// and the harness's Anthropic-style content blocks. Parallel tool calls from
// the model become multiple BlockToolUse entries within a single assistant
// Message. role:"tool" response messages are translated to RoleTool messages
// with BlockToolResult blocks at the adapter boundary.
package openai
