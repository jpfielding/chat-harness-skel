package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
	"github.com/jpfielding/chat-harness-skel/pkg/providers/sse"
)

// Stream issues a streaming request to /v1/chat/completions and synthesizes
// normalized StreamEvents from OpenAI's flat delta chunks.
//
// Key synthesizing responsibilities:
//   - OpenAI emits `choices[0].delta.content` (text) and/or `delta.tool_calls[]`
//     (with per-tool_call `index`). This adapter maps each distinct provider
//     index onto a stable, sequential normalized block Index.
//   - BlockStart is emitted lazily on first delta for each block.
//   - `finish_reason` triggers BlockStops for all open blocks, then
//     MessageDelta (with stop_reason + usage if present), then MessageStop.
//   - `data: [DONE]` terminates the stream (io.EOF from Next).
func (p *Provider) Stream(ctx context.Context, req chat.Request) (chat.StreamReader, error) {
	ref, err := chat.ParseModelRef(req.Model)
	if err != nil {
		return nil, p.wrapErr(chat.ErrKindInvalidRequest, 0, "", false, err)
	}
	body, err := buildChatRequest(req, ref)
	if err != nil {
		return nil, p.wrapErr(chat.ErrKindInvalidRequest, 0, ref.Model, false, err)
	}
	body.Stream = true
	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, p.wrapErr(chat.ErrKindInvalidRequest, 0, ref.Model, false, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, p.wrapErr(chat.ErrKindUnknown, 0, ref.Model, false, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.cfg.OrgID != "" {
		httpReq.Header.Set("OpenAI-Organization", p.cfg.OrgID)
	}

	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, p.wrapErr(classifyTransport(ctx, err), 0, ref.Model, false, err)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &chat.ProviderError{
			Kind:       classifyStatus(resp.StatusCode, raw),
			Provider:   providerName,
			Model:      ref.Model,
			StatusCode: resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("retry-after")),
			RequestID:  resp.Header.Get("x-request-id"),
			Message:    snippet(raw),
		}
	}

	return newStreamReader(ctx, ref, resp), nil
}

type streamReader struct {
	ctx  context.Context
	ref  chat.ModelRef
	resp *http.Response
	sseR *sse.Reader

	mu    sync.Mutex
	state chat.StreamState
	queue []chat.StreamEvent
	done  bool
	err   error

	// blockAcc tracks normalized block index assignments for this turn.
	blockAcc struct {
		textIndex      int  // normalized index of the text block (if any)
		textOpen       bool // has BlockStart(text) been emitted?
		textSeen       bool
		// For tool calls: map OpenAI's delta.tool_calls[].index → our block index.
		toolCallBlock map[int]int
		toolCallOpen  map[int]bool
		nextIndex     int
		started       bool // message_start already emitted?
	}

	closed atomic.Bool
}

func newStreamReader(ctx context.Context, ref chat.ModelRef, resp *http.Response) *streamReader {
	return &streamReader{
		ctx:   ctx,
		ref:   ref,
		resp:  resp,
		sseR:  sse.NewReader(resp.Body),
		state: chat.StreamOpened,
		blockAcc: struct {
			textIndex     int
			textOpen      bool
			textSeen      bool
			toolCallBlock map[int]int
			toolCallOpen  map[int]bool
			nextIndex     int
			started       bool
		}{
			toolCallBlock: map[int]int{},
			toolCallOpen:  map[int]bool{},
		},
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

		raw, err := s.sseR.Next()
		if err != nil {
			if err == io.EOF {
				s.finishStream()
				continue
			}
			if s.ctx.Err() != nil {
				s.markCanceled(s.ctx.Err())
				continue
			}
			s.markFailed(err)
			continue
		}
		if bytes.Equal(bytes.TrimSpace(raw.Data), []byte("[DONE]")) {
			s.finishStream()
			continue
		}
		s.processChunk(raw.Data)
	}
}

// openaiChunk is the shape of one streaming JSON chunk.
type openaiChunk struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Model   string                 `json:"model"`
	Choices []openaiStreamingChoice `json:"choices"`
	Usage   *wireUsage             `json:"usage,omitempty"`
	Error   *openaiErrObj          `json:"error,omitempty"`
}

type openaiStreamingChoice struct {
	Index        int              `json:"index"`
	Delta        openaiStreamDelta `json:"delta"`
	FinishReason string           `json:"finish_reason,omitempty"`
}

type openaiStreamDelta struct {
	Role      string                  `json:"role,omitempty"`
	Content   string                  `json:"content,omitempty"`
	ToolCalls []openaiStreamToolCall  `json:"tool_calls,omitempty"`
}

type openaiStreamToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function *openaiStreamFn  `json:"function,omitempty"`
}

type openaiStreamFn struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openaiErrObj struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (s *streamReader) processChunk(data []byte) {
	var ch openaiChunk
	if err := json.Unmarshal(data, &ch); err != nil {
		s.markFailed(fmt.Errorf("decode chunk: %w", err))
		return
	}
	if ch.Error != nil {
		s.markFailed(errors.New(ch.Error.Message))
		return
	}

	// Emit MessageStart once, on first chunk.
	if !s.blockAcc.started {
		s.enqueue(chat.StreamEvent{Kind: chat.EvMessageStart})
		s.setState(chat.StreamProviderStarted)
		s.blockAcc.started = true
	}

	// Multiple choices are unusual for chat streaming; consume [0].
	if len(ch.Choices) == 0 {
		// A chunk with only usage (final chunk from some providers).
		if ch.Usage != nil {
			s.enqueue(chat.StreamEvent{
				Kind:  chat.EvMessageDelta,
				Usage: &chat.Usage{InputTokens: ch.Usage.PromptTokens, OutputTokens: ch.Usage.CompletionTokens},
			})
		}
		return
	}
	c := ch.Choices[0]

	// Text deltas.
	if c.Delta.Content != "" {
		if !s.blockAcc.textSeen {
			idx := s.blockAcc.nextIndex
			s.blockAcc.nextIndex++
			s.blockAcc.textIndex = idx
			s.blockAcc.textSeen = true
			s.blockAcc.textOpen = true
			skeleton := chat.ContentBlock{Kind: chat.BlockText}
			s.enqueue(chat.StreamEvent{
				Kind:  chat.EvBlockStart,
				Index: idx,
				Block: &skeleton,
			})
		}
		s.enqueue(chat.StreamEvent{
			Kind:      chat.EvBlockDelta,
			Index:     s.blockAcc.textIndex,
			TextDelta: c.Delta.Content,
		})
		s.setState(chat.StreamPartialOutput)
	}

	// Tool-call deltas. OpenAI streams them interleaved with an explicit
	// tool_calls[].index that is stable per call within the assistant turn.
	for _, tc := range c.Delta.ToolCalls {
		blkIdx, known := s.blockAcc.toolCallBlock[tc.Index]
		if !known {
			blkIdx = s.blockAcc.nextIndex
			s.blockAcc.nextIndex++
			s.blockAcc.toolCallBlock[tc.Index] = blkIdx
			s.blockAcc.toolCallOpen[tc.Index] = true
			// Emit BlockStart with whatever id/name we have now (may be empty
			// and filled in later by subsequent deltas).
			skeleton := chat.ContentBlock{
				Kind: chat.BlockToolUse,
				ToolUse: &chat.ToolUse{
					ID: tc.ID,
					Name: func() string {
						if tc.Function != nil {
							return tc.Function.Name
						}
						return ""
					}(),
				},
			}
			s.enqueue(chat.StreamEvent{
				Kind:  chat.EvBlockStart,
				Index: blkIdx,
				Block: &skeleton,
			})
		}
		// Emit deltas for any fresh id/name/arguments bytes.
		if tc.ID != "" || (tc.Function != nil && (tc.Function.Name != "" || tc.Function.Arguments != "")) {
			var ev chat.StreamEvent
			ev.Kind = chat.EvBlockDelta
			ev.Index = blkIdx
			if tc.Function != nil {
				if tc.Function.Arguments != "" {
					ev.RawInputDelta = tc.Function.Arguments
				}
			}
			// We don't currently deliver id/name updates as separate events
			// (our schema's Block skeleton carries the initial values). Late
			// id/name arrivals are applied to the skeleton in a future refactor.
			if ev.RawInputDelta != "" {
				s.enqueue(ev)
				s.setState(chat.StreamPartialOutput)
			}
		}
	}

	if c.FinishReason != "" {
		// Stops for any open blocks.
		if s.blockAcc.textOpen {
			s.enqueue(chat.StreamEvent{Kind: chat.EvBlockStop, Index: s.blockAcc.textIndex})
			s.blockAcc.textOpen = false
		}
		for provIdx, normIdx := range s.blockAcc.toolCallBlock {
			if s.blockAcc.toolCallOpen[provIdx] {
				s.enqueue(chat.StreamEvent{Kind: chat.EvBlockStop, Index: normIdx})
				s.blockAcc.toolCallOpen[provIdx] = false
			}
		}
		mde := chat.StreamEvent{
			Kind:       chat.EvMessageDelta,
			StopReason: mapFinishReason(c.FinishReason),
		}
		if ch.Usage != nil {
			mde.Usage = &chat.Usage{InputTokens: ch.Usage.PromptTokens, OutputTokens: ch.Usage.CompletionTokens}
		}
		s.enqueue(mde)
		s.enqueue(chat.StreamEvent{Kind: chat.EvMessageStop})
		s.setState(chat.StreamCompleted)
	}
}

func (s *streamReader) finishStream() {
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
	s.err = &chat.ProviderError{
		Kind:        chat.ErrKindCanceled,
		Provider:    providerName,
		Model:       s.ref.Model,
		AfterOutput: afterOutput,
		Err:         err,
	}
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
	s.err = &chat.ProviderError{
		Kind:        chat.ErrKindServerError,
		Provider:    providerName,
		Model:       s.ref.Model,
		AfterOutput: afterOutput,
		Err:         err,
	}
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
