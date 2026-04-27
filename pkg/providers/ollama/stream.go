package ollama

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
)

// Ollama streaming: newline-delimited JSON chunks, each of the form
//   {"model":"...","message":{"role":"assistant","content":"<chunk>"},
//    "done":false}
// The final chunk has done=true and eval counts.
//
// We emit a single synthetic text block (index 0) for all content chunks.
// Tool calls (when supported by the server) appear only in the final chunk
// with done=true; we emit a synthetic BlockStart/BlockDelta/BlockStop for
// each tool call with its full arguments in one shot.

type streamReader struct {
	ctx  context.Context
	ref  chat.ModelRef
	resp *http.Response
	rd   *bufio.Scanner

	mu    sync.Mutex
	state chat.StreamState
	queue []chat.StreamEvent
	done  bool
	err   error

	// lazy state
	messageStarted bool
	textOpen       bool
	nextBlockIndex int

	closed atomic.Bool
}

func newStreamReader(ctx context.Context, ref chat.ModelRef, resp *http.Response) *streamReader {
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &streamReader{
		ctx:   ctx,
		ref:   ref,
		resp:  resp,
		rd:    sc,
		state: chat.StreamOpened,
	}
}

func (s *streamReader) State() chat.StreamState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *streamReader) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	return s.resp.Body.Close()
}

func (s *streamReader) Next() (chat.StreamEvent, error) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			ev := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			return ev, nil
		}
		if s.done {
			err := s.err
			s.mu.Unlock()
			if err != nil {
				return chat.StreamEvent{}, err
			}
			return chat.StreamEvent{}, io.EOF
		}
		s.mu.Unlock()

		if err := s.ctx.Err(); err != nil {
			s.markCanceled(err)
			continue
		}

		if !s.rd.Scan() {
			if err := s.rd.Err(); err != nil {
				if s.ctx.Err() != nil {
					s.markCanceled(s.ctx.Err())
					continue
				}
				s.markFailed(err)
				continue
			}
			// EOF.
			s.finish()
			continue
		}
		line := s.rd.Bytes()
		if len(line) == 0 {
			continue
		}
		s.processLine(line)
	}
}

type wireStreamChunk struct {
	Model           string      `json:"model"`
	Message         wireMessage `json:"message"`
	Done            bool        `json:"done"`
	DoneReason      string      `json:"done_reason,omitempty"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	EvalCount       int         `json:"eval_count"`
	Error           string      `json:"error,omitempty"`
}

func (s *streamReader) processLine(line []byte) {
	var ch wireStreamChunk
	if err := json.Unmarshal(line, &ch); err != nil {
		s.markFailed(fmt.Errorf("decode chunk: %w", err))
		return
	}
	if ch.Error != "" {
		s.markFailed(fmt.Errorf("%s", ch.Error))
		return
	}
	if !s.messageStarted {
		s.enqueue(chat.StreamEvent{Kind: chat.EvMessageStart})
		s.setState(chat.StreamProviderStarted)
		s.messageStarted = true
	}
	if ch.Message.Content != "" {
		if !s.textOpen {
			s.textOpen = true
			skeleton := chat.ContentBlock{Kind: chat.BlockText}
			s.enqueue(chat.StreamEvent{Kind: chat.EvBlockStart, Index: 0, Block: &skeleton})
			s.nextBlockIndex = 1
		}
		s.enqueue(chat.StreamEvent{Kind: chat.EvBlockDelta, Index: 0, TextDelta: ch.Message.Content})
		s.setState(chat.StreamPartialOutput)
	}
	if ch.Done {
		if s.textOpen {
			s.enqueue(chat.StreamEvent{Kind: chat.EvBlockStop, Index: 0})
			s.textOpen = false
		}
		// Synthesize tool-use blocks for any final tool_calls[].
		for i, tc := range ch.Message.ToolCalls {
			idx := s.nextBlockIndex
			s.nextBlockIndex++
			enc, _ := json.Marshal(tc.Function.Arguments)
			skeleton := chat.ContentBlock{
				Kind:    chat.BlockToolUse,
				ToolUse: &chat.ToolUse{ID: fmt.Sprintf("ollama_call_%d", i), Name: tc.Function.Name},
			}
			s.enqueue(chat.StreamEvent{Kind: chat.EvBlockStart, Index: idx, Block: &skeleton})
			s.enqueue(chat.StreamEvent{Kind: chat.EvBlockDelta, Index: idx, RawInputDelta: string(enc)})
			s.enqueue(chat.StreamEvent{Kind: chat.EvBlockStop, Index: idx})
		}
		stop := mapDoneReason(ch.DoneReason, len(ch.Message.ToolCalls) > 0)
		s.enqueue(chat.StreamEvent{
			Kind:       chat.EvMessageDelta,
			StopReason: stop,
			Usage:      &chat.Usage{InputTokens: ch.PromptEvalCount, OutputTokens: ch.EvalCount},
		})
		s.enqueue(chat.StreamEvent{Kind: chat.EvMessageStop})
		s.setState(chat.StreamCompleted)
	}
}

func (s *streamReader) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.done = true
	if s.state != chat.StreamCompleted && s.state != chat.StreamCanceled && s.state != chat.StreamFailed {
		s.state = chat.StreamCompleted
	}
}

func (s *streamReader) markCanceled(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	afterOutput := s.state == chat.StreamPartialOutput
	s.state = chat.StreamCanceled
	s.done = true
	s.err = &chat.ProviderError{Kind: chat.ErrKindCanceled, Provider: providerName, Model: s.ref.Model, AfterOutput: afterOutput, Err: err}
}

func (s *streamReader) markFailed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	afterOutput := s.state == chat.StreamPartialOutput
	s.state = chat.StreamFailed
	s.done = true
	s.err = &chat.ProviderError{Kind: chat.ErrKindServerError, Provider: providerName, Model: s.ref.Model, AfterOutput: afterOutput, Err: err}
}

func (s *streamReader) enqueue(ev chat.StreamEvent) {
	s.mu.Lock()
	s.queue = append(s.queue, ev)
	s.mu.Unlock()
}

func (s *streamReader) setState(st chat.StreamState) {
	s.mu.Lock()
	if stateRank(st) > stateRank(s.state) {
		s.state = st
	}
	s.mu.Unlock()
}

func stateRank(s chat.StreamState) int {
	switch s {
	case chat.StreamOpened:
		return 1
	case chat.StreamProviderStarted:
		return 2
	case chat.StreamPartialOutput:
		return 3
	case chat.StreamCompleted, chat.StreamCanceled, chat.StreamFailed:
		return 4
	}
	return 0
}
