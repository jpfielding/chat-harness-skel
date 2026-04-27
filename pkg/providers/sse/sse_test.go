package sse

import (
	"io"
	"strings"
	"testing"
)

func TestReader_SimpleEvents(t *testing.T) {
	input := "event: message_start\ndata: {\"a\":1}\n\nevent: message_stop\ndata: {}\n\n"
	r := NewReader(strings.NewReader(input))

	ev, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Name != "message_start" || string(ev.Data) != `{"a":1}` {
		t.Errorf("got %+v", ev)
	}

	ev, err = r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Name != "message_stop" || string(ev.Data) != `{}` {
		t.Errorf("got %+v", ev)
	}

	if _, err := r.Next(); err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestReader_MultilineData(t *testing.T) {
	input := "data: line1\ndata: line2\n\n"
	r := NewReader(strings.NewReader(input))
	ev, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if string(ev.Data) != "line1\nline2" {
		t.Errorf("data=%q", string(ev.Data))
	}
}

func TestReader_CommentsAndPings(t *testing.T) {
	input := ": keep-alive\n\ndata: hi\n\n: another\n\n"
	r := NewReader(strings.NewReader(input))
	ev, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if string(ev.Data) != "hi" {
		t.Errorf("data=%q", string(ev.Data))
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestReader_OpenAIStyleDataOnly(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: [DONE]\n\n"
	r := NewReader(strings.NewReader(input))
	ev, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Name != "" {
		t.Errorf("expected empty event name, got %q", ev.Name)
	}
	if !strings.Contains(string(ev.Data), `"Hi"`) {
		t.Errorf("data=%q", ev.Data)
	}
	ev, err = r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if string(ev.Data) != "[DONE]" {
		t.Errorf("data=%q", ev.Data)
	}
}

func TestReader_NoTrailingBlank(t *testing.T) {
	input := "data: last"
	r := NewReader(strings.NewReader(input))
	ev, err := r.Next()
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if string(ev.Data) != "last" {
		t.Errorf("data=%q", ev.Data)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}
