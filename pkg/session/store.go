// Package session defines the Store interface for multi-turn conversation
// state and provides an in-memory implementation. Phase 3 scope.
//
// Status: experimental. AppendConditional uses optimistic concurrency: the
// caller passes the version it observed on Get; if another write has bumped
// the version since then, AppendConditional returns ErrVersionConflict.
package session

import (
	"context"
	"time"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// Session is one multi-turn conversation. Version is incremented on every
// successful write.
type Session struct {
	ID        string            `json:"id"`
	Version   int64             `json:"version"`
	System    string            `json:"system,omitempty"`
	Messages  []chat.Message    `json:"messages"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// ListOptions controls pagination in List.
type ListOptions struct {
	Limit  int // 0 = no limit
	Offset int
}

// Store is the persistence contract for sessions. All implementations must
// satisfy sessiontest.TestStore in pkg/session/sessiontest.
type Store interface {
	// Get returns the current state of the session. Returns chat.ErrNotFound
	// if absent.
	Get(ctx context.Context, id string) (*Session, error)

	// Save creates or replaces the session unconditionally. Sets Version to
	// s.Version+1 and UpdatedAt to now. If the session did not exist, Set
	// sets CreatedAt too.
	Save(ctx context.Context, s *Session) error

	// AppendConditional atomically appends msgs to the session identified by
	// id, but ONLY if the session's current Version equals expectedVersion.
	// On success, returns the new Version. On mismatch, returns
	// chat.ErrVersionConflict. If the session does not exist, returns
	// chat.ErrNotFound.
	AppendConditional(ctx context.Context, id string, expectedVersion int64, msgs ...chat.Message) (int64, error)

	// Delete removes the session. A Delete on a missing id returns nil.
	Delete(ctx context.Context, id string) error

	// List returns sessions in an unspecified but stable order.
	List(ctx context.Context, opts ListOptions) ([]Session, error)
}

// MaxMessagesCap is the default per-session message cap. Stores MAY enforce
// a smaller cap but must not silently drop messages without returning an
// error. Callers hitting the cap should start a new session.
const MaxMessagesCap = 1000

// ErrMessagesExceeded is returned when AppendConditional would push a
// session above its message cap.
type messagesExceededError struct{}

func (messagesExceededError) Error() string {
	return "session: messages cap exceeded"
}

// ErrMessagesExceeded is the sentinel for capacity enforcement.
var ErrMessagesExceeded error = messagesExceededError{}
