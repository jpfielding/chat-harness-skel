package session

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// MemoryStore is a concurrent in-memory Store. Good for development and
// tests; not suitable for production (no persistence, no eviction).
type MemoryStore struct {
	mu       sync.RWMutex
	byID     map[string]*Session
	cap      int
	nowFn    func() time.Time
}

// NewMemoryStore returns an empty store. maxMessages clamps how many messages
// each session may hold; use MaxMessagesCap for the default.
func NewMemoryStore(maxMessages int) *MemoryStore {
	if maxMessages <= 0 {
		maxMessages = MaxMessagesCap
	}
	return &MemoryStore{
		byID:  map[string]*Session{},
		cap:   maxMessages,
		nowFn: time.Now,
	}
}

// SetClock overrides the clock for deterministic tests.
func (m *MemoryStore) SetClock(fn func() time.Time) { m.nowFn = fn }

// Get implements Store.
func (m *MemoryStore) Get(_ context.Context, id string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.byID[id]
	if !ok {
		return nil, chat.ErrNotFound
	}
	return deepCopySession(s), nil
}

// Save implements Store.
func (m *MemoryStore) Save(_ context.Context, s *Session) error {
	if s == nil || s.ID == "" {
		return &chat.ValidationError{Issues: []string{"session id required"}}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.nowFn()
	cp := deepCopySession(s)
	if existing, ok := m.byID[s.ID]; ok {
		cp.CreatedAt = existing.CreatedAt
	} else if cp.CreatedAt.IsZero() {
		cp.CreatedAt = now
	}
	cp.UpdatedAt = now
	cp.Version = s.Version + 1
	m.byID[s.ID] = cp
	return nil
}

// AppendConditional implements Store.
func (m *MemoryStore) AppendConditional(_ context.Context, id string, expectedVersion int64, msgs ...chat.Message) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[id]
	if !ok {
		return 0, chat.ErrNotFound
	}
	if s.Version != expectedVersion {
		return 0, chat.ErrVersionConflict
	}
	if len(s.Messages)+len(msgs) > m.cap {
		return 0, ErrMessagesExceeded
	}
	cp := deepCopySession(s)
	for _, m := range msgs {
		cp.Messages = append(cp.Messages, deepCopyMessage(m))
	}
	cp.Version = s.Version + 1
	cp.UpdatedAt = m.nowFn()
	m.byID[id] = cp
	return cp.Version, nil
}

// Delete implements Store.
func (m *MemoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byID, id)
	return nil
}

// List implements Store.
func (m *MemoryStore) List(_ context.Context, opts ListOptions) ([]Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Session, 0, len(m.byID))
	for _, s := range m.byID {
		out = append(out, *deepCopySession(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if opts.Offset >= len(out) {
		return nil, nil
	}
	out = out[opts.Offset:]
	if opts.Limit > 0 && opts.Limit < len(out) {
		out = out[:opts.Limit]
	}
	return out, nil
}

// deepCopySession returns a fully-independent copy of s. The MemoryStore
// deep-copies on every boundary (Save/Get/Append) so that callers mutating
// their inputs or outputs cannot poison stored state.
func deepCopySession(s *Session) *Session {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Messages != nil {
		cp.Messages = make([]chat.Message, len(s.Messages))
		for i, m := range s.Messages {
			cp.Messages[i] = deepCopyMessage(m)
		}
	}
	if s.Metadata != nil {
		cp.Metadata = make(map[string]string, len(s.Metadata))
		for k, v := range s.Metadata {
			cp.Metadata[k] = v
		}
	}
	return &cp
}

func deepCopyMessage(m chat.Message) chat.Message {
	out := m
	if m.Content == nil {
		return out
	}
	out.Content = make([]chat.ContentBlock, len(m.Content))
	for i, b := range m.Content {
		out.Content[i] = deepCopyBlock(b)
	}
	return out
}

func deepCopyBlock(b chat.ContentBlock) chat.ContentBlock {
	out := b
	// Pointer-valued fields cloned shallowly for now; ToolUse.ParsedInput
	// may alias, so deep-clone via JSON round-trip when present.
	if b.ToolUse != nil {
		tu := *b.ToolUse
		if b.ToolUse.ParsedInput != nil {
			if buf, err := json.Marshal(b.ToolUse.ParsedInput); err == nil {
				var m map[string]any
				_ = json.Unmarshal(buf, &m)
				tu.ParsedInput = m
			}
		}
		out.ToolUse = &tu
	}
	if b.ToolResult != nil {
		tr := *b.ToolResult
		if b.ToolResult.Content != nil {
			tr.Content = make([]chat.ContentBlock, len(b.ToolResult.Content))
			for i, c := range b.ToolResult.Content {
				tr.Content[i] = deepCopyBlock(c)
			}
		}
		out.ToolResult = &tr
	}
	if b.Image != nil {
		img := *b.Image
		out.Image = &img
	}
	if b.ProviderMetadata != nil {
		m := make(map[string]any, len(b.ProviderMetadata))
		for k, v := range b.ProviderMetadata {
			m[k] = v
		}
		out.ProviderMetadata = m
	}
	return out
}
