package chat

import (
	"errors"
	"fmt"
	"time"
)

// ErrorKind is the normalized category of a provider error. Adapters MUST set
// Kind to the closest match; router fallback decisions key off Kind.
type ErrorKind string

const (
	ErrKindUnknown            ErrorKind = "Unknown"
	ErrKindRateLimit          ErrorKind = "RateLimit"
	ErrKindTimeout            ErrorKind = "Timeout"
	ErrKindContextLength      ErrorKind = "ContextLength"
	ErrKindOverloaded         ErrorKind = "Overloaded"
	ErrKindServerError        ErrorKind = "ServerError"
	ErrKindToolsUnsupported   ErrorKind = "ToolsUnsupported"
	ErrKindUnsupportedContent ErrorKind = "UnsupportedContent"
	ErrKindCanceled           ErrorKind = "Canceled"
	ErrKindAuthFailed         ErrorKind = "AuthFailed"
	ErrKindNotFound           ErrorKind = "NotFound"
	ErrKindInvalidRequest     ErrorKind = "InvalidRequest"
)

// ProviderError is the structured error type returned by all Provider adapters.
// It carries everything the router needs to decide whether, and to where, to
// fall back.
type ProviderError struct {
	Kind        ErrorKind     // normalized category
	Provider    string        // provider name (e.g. "anthropic")
	Model       string        // provider-native model id
	StatusCode  int           // HTTP status if applicable, else 0
	RetryAfter  time.Duration // honored when set
	RequestID   string        // provider-supplied request id, if any
	AfterOutput bool          // true if any bytes were emitted before failure
	Message     string        // human-readable detail
	Err         error         // wrapped underlying error, if any
}

// Error implements error.
func (e *ProviderError) Error() string {
	base := fmt.Sprintf("%s: %s", e.Provider, e.Kind)
	if e.Message != "" {
		base = fmt.Sprintf("%s: %s", base, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", base, e.Err)
	}
	return base
}

// Unwrap supports errors.Is/As chaining.
func (e *ProviderError) Unwrap() error { return e.Err }

// Is allows errors.Is(err, target) to match on Kind when target is a *ProviderError
// with only Kind set.
func (e *ProviderError) Is(target error) bool {
	t, ok := target.(*ProviderError)
	if !ok {
		return false
	}
	if t.Kind == "" {
		return true
	}
	return t.Kind == e.Kind
}

// AsProviderError extracts a *ProviderError from err, if present.
func AsProviderError(err error) (*ProviderError, bool) {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// ErrVersionConflict is returned by session stores when a conditional append
// sees a version mismatch.
var ErrVersionConflict = errors.New("session: version conflict")

// ErrNotFound is returned when a session or other keyed resource is missing.
var ErrNotFound = errors.New("not found")
