// Package anthropic implements chat.Provider against the Anthropic Messages API.
//
// Status: experimental. Non-streaming Send is implemented in Phase 1; Stream
// is stubbed to ErrNotImplemented and filled in during Phase 2.
//
// The adapter uses net/http directly rather than a vendor SDK so that
// test injection via Config.BaseURL is trivial and so that SDK version
// churn does not propagate into the harness.
package anthropic
