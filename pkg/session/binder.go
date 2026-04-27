package session

import (
	"context"
	"errors"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// Binder adapts a Store to the narrow chat.SessionBinder interface the
// harness expects. The adapter sits in pkg/session so that pkg/chat does
// not need to depend on pkg/session (keeping the dependency direction
// clean).
type Binder struct {
	Store Store
}

// NewBinder wraps s.
func NewBinder(s Store) *Binder { return &Binder{Store: s} }

// Load implements chat.SessionBinder. A missing session is NOT an error
// here — it returns zero values with a nil error, signaling "empty history".
// This matches how REST clients expect PUT-then-POST patterns to behave.
func (b *Binder) Load(ctx context.Context, id string) (string, []chat.Message, int64, error) {
	s, err := b.Store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, chat.ErrNotFound) {
			return "", nil, 0, nil
		}
		return "", nil, 0, err
	}
	return s.System, s.Messages, s.Version, nil
}

// Append implements chat.SessionBinder. If the session does not exist yet
// (expectedVersion==0), it is created; otherwise AppendConditional is used
// for optimistic-concurrency enforcement.
func (b *Binder) Append(ctx context.Context, id string, expectedVersion int64, msgs ...chat.Message) (int64, error) {
	if expectedVersion == 0 {
		if _, err := b.Store.Get(ctx, id); errors.Is(err, chat.ErrNotFound) {
			// First append: create the session with these messages.
			if err := b.Store.Save(ctx, &Session{ID: id, Messages: msgs}); err != nil {
				return 0, err
			}
			// Save sets Version to 1; no further append needed.
			return 1, nil
		}
	}
	return b.Store.AppendConditional(ctx, id, expectedVersion, msgs...)
}
