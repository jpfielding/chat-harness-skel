// Package sessiontest exposes a shared compliance harness for Store
// implementations. Every implementation (MemoryStore, and later SQLite,
// Redis) runs against the same test battery.
package sessiontest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
	"github.com/jpfielding/chat-harness-skel/pkg/session"
)

// Factory returns a fresh, empty Store. Each subtest invokes it so tests
// are isolated.
type Factory func() session.Store

// TestStore runs the full compliance battery against factory.
func TestStore(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("GetMissing", func(t *testing.T) { testGetMissing(t, factory) })
	t.Run("SaveGetRoundTrip", func(t *testing.T) { testSaveGet(t, factory) })
	t.Run("AppendConditionalHappyPath", func(t *testing.T) { testAppendHappy(t, factory) })
	t.Run("AppendConditionalVersionConflict", func(t *testing.T) { testAppendConflict(t, factory) })
	t.Run("AppendConditionalMissing", func(t *testing.T) { testAppendMissing(t, factory) })
	t.Run("DeepCopy", func(t *testing.T) { testDeepCopy(t, factory) })
	t.Run("ConcurrentAppendSerializes", func(t *testing.T) { testConcurrentAppend(t, factory) })
	t.Run("DeleteIsIdempotent", func(t *testing.T) { testDeleteIdempotent(t, factory) })
	t.Run("ListIsStable", func(t *testing.T) { testList(t, factory) })
}

func testGetMissing(t *testing.T, factory Factory) {
	s := factory()
	_, err := s.Get(context.Background(), "absent")
	if !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func testSaveGet(t *testing.T, factory Factory) {
	s := factory()
	sess := &session.Session{ID: "s1", Messages: []chat.Message{chat.UserText("hello")}}
	if err := s.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 {
		t.Errorf("version=%d", got.Version)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content[0].Text != "hello" {
		t.Errorf("bad messages: %+v", got.Messages)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt unset")
	}
}

func testAppendHappy(t *testing.T, factory Factory) {
	s := factory()
	if err := s.Save(context.Background(), &session.Session{ID: "s1"}); err != nil {
		t.Fatal(err)
	}
	newV, err := s.AppendConditional(context.Background(), "s1", 1, chat.UserText("ping"))
	if err != nil {
		t.Fatal(err)
	}
	if newV != 2 {
		t.Errorf("newV=%d", newV)
	}
	got, _ := s.Get(context.Background(), "s1")
	if len(got.Messages) != 1 {
		t.Errorf("messages=%d", len(got.Messages))
	}
}

func testAppendConflict(t *testing.T, factory Factory) {
	s := factory()
	if err := s.Save(context.Background(), &session.Session{ID: "s1"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.AppendConditional(context.Background(), "s1", 99 /* wrong */, chat.UserText("x"))
	if !errors.Is(err, chat.ErrVersionConflict) {
		t.Fatalf("want ErrVersionConflict, got %v", err)
	}
}

func testAppendMissing(t *testing.T, factory Factory) {
	s := factory()
	_, err := s.AppendConditional(context.Background(), "ghost", 0, chat.UserText("x"))
	if !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func testDeepCopy(t *testing.T, factory Factory) {
	s := factory()
	meta := map[string]string{"tenant": "a"}
	msg := chat.Message{Role: chat.RoleUser, Content: []chat.ContentBlock{{
		Kind: chat.BlockText, Text: "hi",
		ProviderMetadata: map[string]any{"k": "v"},
	}}}
	sess := &session.Session{ID: "s1", Metadata: meta, Messages: []chat.Message{msg}}
	if err := s.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	// Mutate caller-side inputs after save; store must not see changes.
	meta["tenant"] = "b"
	msg.Content[0].Text = "MUTATED"
	msg.Content[0].ProviderMetadata["k"] = "MUTATED"

	got, _ := s.Get(context.Background(), "s1")
	if got.Metadata["tenant"] != "a" {
		t.Errorf("metadata leaked: %v", got.Metadata)
	}
	if got.Messages[0].Content[0].Text != "hi" {
		t.Errorf("text leaked: %q", got.Messages[0].Content[0].Text)
	}
	if v := got.Messages[0].Content[0].ProviderMetadata["k"]; v != "v" {
		t.Errorf("provider_metadata leaked: %v", v)
	}
}

func testConcurrentAppend(t *testing.T, factory Factory) {
	s := factory()
	if err := s.Save(context.Background(), &session.Session{ID: "s1"}); err != nil {
		t.Fatal(err)
	}

	// N goroutines each read the current version and attempt to append. With
	// optimistic concurrency, at most one wins per version; losers retry.
	const N = 8
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			for {
				cur, err := s.Get(context.Background(), "s1")
				if err != nil {
					errs <- err
					return
				}
				_, err = s.AppendConditional(context.Background(), "s1", cur.Version,
					chat.UserText("m"+string(rune('a'+i))))
				if err == nil {
					return
				}
				if !errors.Is(err, chat.ErrVersionConflict) {
					errs <- err
					return
				}
				// conflict → retry
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("unexpected err: %v", err)
	}
	got, _ := s.Get(context.Background(), "s1")
	if len(got.Messages) != N {
		t.Errorf("want %d msgs, got %d", N, len(got.Messages))
	}
	if got.Version != int64(N+1) { // Save=1 + N appends
		t.Errorf("version=%d want %d", got.Version, N+1)
	}
}

func testDeleteIdempotent(t *testing.T, factory Factory) {
	s := factory()
	if err := s.Delete(context.Background(), "never-existed"); err != nil {
		t.Errorf("delete-missing should be nil: %v", err)
	}
	_ = s.Save(context.Background(), &session.Session{ID: "a"})
	if err := s.Delete(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "a"); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func testList(t *testing.T, factory Factory) {
	s := factory()
	for _, id := range []string{"c", "a", "b"} {
		_ = s.Save(context.Background(), &session.Session{ID: id})
	}
	got, err := s.List(context.Background(), session.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d", len(got))
	}
	// Stable order (the shared contract says "stable", not "sorted", but
	// any impl should return consistent order for the same state).
	orderA := concatIDs(got)
	got, _ = s.List(context.Background(), session.ListOptions{})
	if orderA != concatIDs(got) {
		t.Errorf("List order not stable: %q vs %q", orderA, concatIDs(got))
	}
}

func concatIDs(sessions []session.Session) string {
	parts := make([]string, len(sessions))
	for i, s := range sessions {
		parts[i] = s.ID
	}
	return strings.Join(parts, ",")
}
