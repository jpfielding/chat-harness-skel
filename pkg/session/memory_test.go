package session_test

import (
	"testing"

	"github.com/jpfielding/chat-harness-skel/pkg/session"
	"github.com/jpfielding/chat-harness-skel/pkg/session/sessiontest"
)

func TestMemoryStore(t *testing.T) {
	sessiontest.TestStore(t, func() session.Store {
		return session.NewMemoryStore(session.MaxMessagesCap)
	})
}
