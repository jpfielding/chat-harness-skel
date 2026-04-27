package anthropic

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

// Stream issues a streaming request to /v1/messages?stream=true and returns
// a StreamReader that yields normalized StreamEvents.
func (p *Provider) Stream(ctx context.Context, req chat.Request) (chat.StreamReader, error) {
	ref, err := chat.ParseModelRef(req.Model)
	if err != nil {
		return nil, p.wrapErr(chat.ErrKindInvalidRequest, 0, "", false, err)
	}
	body, err := buildMessagesRequest(req, ref)
	if err != nil {
		return nil, p.wrapErr(chat.ErrKindInvalidRequest, 0, ref.Model, false, err)
	}
	body.Stream = true
	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, p.wrapErr(chat.ErrKindInvalidRequest, 0, ref.Model, false, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return nil, p.wrapErr(chat.ErrKindUnknown, 0, ref.Model, false, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", p.cfg.APIVersion)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, p.wrapErr(classifyTransportError(ctx, err), 0, ref.Model, false, err)
	}
	if resp.StatusCode >= 400 {
		// Drain & close; synthesize error.
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &chat.ProviderError{
			Kind:       classifyStatus(resp.StatusCode, raw),
			Provider:   providerName,
			Model:      ref.Model,
			StatusCode: resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("retry-after")),
			RequestID:  resp.Header.Get("request-id"),
			Message:    snippet(raw),
		}
	}

	return newStreamReader(ctx, ref, resp), nil
}

// streamReader translates Anthropic SSE events into normalized StreamEvents.
type streamReader struct {
	ctx   context.Context
	ref   chat.ModelRef
	resp  *http.Response
	sseR  *sse.Reader

	mu    sync.Mutex
	state chat.StreamState
	queue []chat.StreamEvent
	done  bool
	err   error

	closed atomic.Bool
}

func newStreamReader(ctx context.Context, ref chat.ModelRef, resp *http.Response) *streamReader {
	return &streamReader{
		ctx:   ctx,
		ref:   ref,
		resp:  resp,
		sseR:  sse.NewReader(resp.Body),
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

		// Check for cancel before blocking on the network.
		if err := s.ctx.Err(); err != nil {
			s.markCanceled(err)
			continue
		}

		rawEv, err := s.sseR.Next()
		if err != nil {
			if err == io.EOF {
				s.mu.Lock()
				s.done = true
				if s.state != chat.StreamCompleted && s.state != chat.StreamCanceled {
					s.state = chat.StreamCompleted
				}
				s.mu.Unlock()
				continue
			}
			if s.ctx.Err() != nil {
				s.markCanceled(s.ctx.Err())
				continue
			}
			s.markFailed(err)
			continue
		}
		s.processEvent(rawEv)
	}
}

func (s *streamReader) markCanceled(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.state = chat.StreamCanceled
	s.done = true
	s.err = &chat.ProviderError{
		Kind:        chat.ErrKindCanceled,
		Provider:    providerName,
		Model:       s.ref.Model,
		AfterOutput: s.state == chat.StreamPartialOutput,
		Err:         err,
	}
}

func (s *streamReader) markFailed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.state = chat.StreamFailed
	s.done = true
	s.err = &chat.ProviderError{
		Kind:        chat.ErrKindServerError,
		Provider:    providerName,
		Model:       s.ref.Model,
		AfterOutput: s.state == chat.StreamPartialOutput,
		Err:         err,
	}
}

// anthropicEvent is the generic shape of an Anthropic stream event's JSON body.
type anthropicEvent struct {
	Type         string           `json:"type"`
	Index        int              `json:"index"`
	Message      *anthropicMsg    `json:"message,omitempty"`       // message_start
	ContentBlock *wireBlock       `json:"content_block,omitempty"` // content_block_start
	Delta        *anthropicDelta  `json:"delta,omitempty"`         // content_block_delta, message_delta
	Usage        *wireUsage       `json:"usage,omitempty"`         // message_delta
	Error        *anthropicErrObj `json:"error,omitempty"`         // error
}

type anthropicMsg struct {
	ID    string    `json:"id"`
	Usage wireUsage `json:"usage"`
}

type anthropicDelta struct {
	Type        string `json:"type,omitempty"` // text_delta | input_json_delta | thinking_delta
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"` // on message_delta
}

type anthropicErrObj struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (s *streamReader) processEvent(raw sse.Event) {
	// Anthropic attaches the event name both in the "event:" line and the
	// "type" field within the JSON body. We trust the body.
	if len(raw.Data) == 0 {
		return
	}
	var ev anthropicEvent
	if err := json.Unmarshal(raw.Data, &ev); err != nil {
		s.markFailed(fmt.Errorf("decode event: %w", err))
		return
	}
	switch ev.Type {
	case "message_start":
		s.enqueue(chat.StreamEvent{Kind: chat.EvMessageStart})
		s.setState(chat.StreamProviderStarted)
	case "ping":
		s.enqueue(chat.StreamEvent{Kind: chat.EvPing})
	case "content_block_start":
		skeleton, err := wireToBlock(*ev.ContentBlock)
		if err != nil {
			s.markFailed(err)
			return
		}
		s.enqueue(chat.StreamEvent{
			Kind:  chat.EvBlockStart,
			Index: ev.Index,
			Block: &skeleton,
		})
	case "content_block_delta":
		if ev.Delta == nil {
			return
		}
		e := chat.StreamEvent{Kind: chat.EvBlockDelta, Index: ev.Index}
		switch ev.Delta.Type {
		case "text_delta":
			e.TextDelta = ev.Delta.Text
		case "input_json_delta":
			e.RawInputDelta = ev.Delta.PartialJSON
		case "thinking_delta":
			e.TextDelta = ev.Delta.Thinking // surface thinking deltas as text for now
		}
		s.enqueue(e)
		s.setState(chat.StreamPartialOutput)
	case "content_block_stop":
		s.enqueue(chat.StreamEvent{Kind: chat.EvBlockStop, Index: ev.Index})
	case "message_delta":
		e := chat.StreamEvent{Kind: chat.EvMessageDelta}
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			e.StopReason = mapStopReason(ev.Delta.StopReason)
		}
		if ev.Usage != nil {
			e.Usage = &chat.Usage{
				InputTokens:      ev.Usage.InputTokens,
				OutputTokens:     ev.Usage.OutputTokens,
				CacheReadTokens:  ev.Usage.CacheReadInputTokens,
				CacheWriteTokens: ev.Usage.CacheCreationInputTokens,
			}
		}
		s.enqueue(e)
	case "message_stop":
		s.enqueue(chat.StreamEvent{Kind: chat.EvMessageStop})
		s.setState(chat.StreamCompleted)
	case "error":
		msg := "unknown"
		if ev.Error != nil {
			msg = ev.Error.Message
		}
		s.markFailed(errors.New(msg))
	}
}

func (s *streamReader) enqueue(ev chat.StreamEvent) {
	s.mu.Lock()
	s.queue = append(s.queue, ev)
	s.mu.Unlock()
}

func (s *streamReader) setState(st chat.StreamState) {
	s.mu.Lock()
	// Only move forward through the lifecycle (never regress).
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
	case chat.StreamCompleted:
		return 4
	case chat.StreamCanceled:
		return 5
	case chat.StreamFailed:
		return 5
	}
	return 0
}
