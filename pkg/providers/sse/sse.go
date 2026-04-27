// Package sse parses Server-Sent Events as used by the Anthropic and OpenAI
// streaming APIs. It handles the subset of the SSE spec both providers
// actually emit: optional "event: <name>" lines, one-or-more "data: <body>"
// lines concatenated with newlines, blank line as terminator, ":" lines as
// comments/pings.
package sse

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// Event is one decoded SSE event.
type Event struct {
	// Name is the contents of the "event:" line, or empty when absent.
	Name string
	// Data is the concatenated "data:" lines (separated by '\n'), with the
	// leading space after "data:" trimmed.
	Data []byte
}

// Reader parses SSE events from an underlying byte stream. Reader is NOT
// safe for concurrent use.
type Reader struct {
	br *bufio.Reader
}

// NewReader wraps r.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, 32*1024)}
}

// Next returns the next event. Returns io.EOF at end of stream. Comment
// lines (":...") are silently skipped. Empty events (blank line with no
// preceding data or event lines) are skipped.
func (r *Reader) Next() (Event, error) {
	var ev Event
	var data []byte
	for {
		line, err := r.readLine()
		if err != nil {
			if err == io.EOF && (len(data) > 0 || ev.Name != "") {
				// Server ended without a trailing blank line; emit what we have.
				ev.Data = data
				return ev, nil
			}
			return Event{}, err
		}
		// Blank line terminates the event.
		if len(line) == 0 {
			if len(data) == 0 && ev.Name == "" {
				// Empty event; skip and continue.
				continue
			}
			ev.Data = data
			return ev, nil
		}
		// Comment / keep-alive.
		if line[0] == ':' {
			continue
		}
		field, value := splitField(line)
		switch field {
		case "event":
			ev.Name = value
		case "data":
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, value...)
		case "id", "retry":
			// Silently ignore — neither provider uses these for chat streams.
		}
	}
}

// readLine returns one line with the trailing CR/LF stripped. Returns
// io.EOF at end-of-stream.
func (r *Reader) readLine() (string, error) {
	var buf bytes.Buffer
	for {
		chunk, isPrefix, err := r.br.ReadLine()
		if err != nil {
			if err == io.EOF && buf.Len() > 0 {
				return buf.String(), nil
			}
			return "", err
		}
		buf.Write(chunk)
		if !isPrefix {
			return buf.String(), nil
		}
	}
}

func splitField(line string) (field, value string) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return line, ""
	}
	field = line[:i]
	value = line[i+1:]
	// Per spec, a single leading space after ':' is trimmed.
	if strings.HasPrefix(value, " ") {
		value = value[1:]
	}
	return field, value
}

// ErrMalformed is returned for bytes that cannot be parsed as SSE.
var ErrMalformed = errors.New("sse: malformed event")
