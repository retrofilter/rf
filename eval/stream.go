package eval

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"
)

// Stream is a lazy, single-use sequence of Values — the value type behind
// cat/sh/fetch and lazy grep/take stages, so (pipe (sh "yes") (take 2))
// terminates instead of buffering forever.
type Stream struct {
	mu      sync.Mutex
	next    func() (Value, bool, error)
	closefn func() error
	lines   bool
	done    bool

	userCode bool
}

// ErrStreamConsumed is returned by Next on a stream that already ended or
// was closed. Materialize a stream (assign the printed result, *1) to reuse
// its values.
var ErrStreamConsumed = errors.New("stream already consumed — re-run the producer, or use the materialized result (*1)")

func newStream(next func() (Value, bool, error), closefn func() error) *Stream {
	s := &Stream{next: next, closefn: closefn}
	if closefn != nil {
		runtime.AddCleanup(s, func(fn func() error) { _ = fn() }, closefn)
	}
	return s
}

func newLineStream(next func() (Value, bool, error), closefn func() error) *Stream {
	s := newStream(next, closefn)
	s.lines = true
	return s
}

func streamFromReader(r io.Reader, closefn func() error) *Stream {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	next := func() (Value, bool, error) {
		if scanner.Scan() {
			return String(scanner.Text()), true, nil
		}
		if err := scanner.Err(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	return newLineStream(next, closefn)
}

func streamFromList(items []Value, lines bool) *Stream {
	i := 0
	next := func() (Value, bool, error) {
		if i >= len(items) {
			return nil, false, nil
		}
		v := items[i]
		i++
		return v, true, nil
	}
	s := newStream(next, nil)
	s.lines = lines
	return s
}

// Lines reports whether this is a line stream — the lines of a text
// source, rejoinable into a string — as opposed to a stream of rows.
func (s *Stream) Lines() bool { return s.lines }

// Next returns the next value. ok=false with a nil error is a clean end
// (resources already released); a spent stream errors ErrStreamConsumed.
func (s *Stream) Next() (Value, bool, error) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil, false, ErrStreamConsumed
	}
	next := s.next
	s.mu.Unlock()

	v, ok, err := next()

	s.mu.Lock()
	closedMeanwhile := s.done
	s.mu.Unlock()
	if closedMeanwhile {
		return nil, false, nil
	}
	if err != nil {
		_ = s.Close()
		return nil, false, err
	}
	if !ok {
		_ = s.Close() // normal end releases resources
		return nil, false, nil
	}
	return v, true, nil
}

// Close marks the stream done and releases upstream resources — killing a
// sh child process, closing a cat file handle. Idempotent; safe to call
// concurrently with a blocked Next.
func (s *Stream) Close() error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil
	}
	s.done = true
	fn := s.closefn
	s.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func lineText(v Value) string {
	if s, ok := v.(String); ok {
		return string(s)
	}
	return PrintValue(v)
}

// TextReader adapts a line stream to an io.Reader yielding its text, each
// line newline-terminated — the shape a child process's stdin expects.
func (s *Stream) TextReader() io.Reader { return &streamReader{s: s} }

type streamReader struct {
	s   *Stream
	buf []byte
}

func (r *streamReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		v, ok, err := r.s.Next()
		if err != nil {
			if errors.Is(err, ErrStreamConsumed) {
				return 0, io.EOF // closed from outside mid-pump
			}
			return 0, err
		}
		if !ok {
			return 0, io.EOF
		}
		r.buf = append(r.buf, lineText(v)...)
		r.buf = append(r.buf, '\n')
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// AsList returns a value as a list, draining a stream if given one.
// ok=false means the value has no list shape (coercion sites keep their
// own type-error messages).
func AsList(v Value) ([]Value, bool, error) {
	switch val := v.(type) {
	case []Value:
		return val, true, nil
	case *Pair:
		lst, err := pairToSlice(val)
		if err != nil {
			return nil, true, err
		}
		return lst, true, nil
	case *Stream:
		out := []Value{}
		for {
			if Interrupted() {
				_ = val.Close()
				return nil, true, ErrInterrupted
			}
			item, ok, err := val.Next()
			if err != nil {
				return nil, true, err
			}
			if !ok {
				return out, true, nil
			}
			out = append(out, item)
		}
	}
	return nil, false, nil
}

// AsString returns string content: a String as-is, a line stream drained
// and rejoined (each line newline-terminated, matching a text file's
// shape). Row streams and non-strings return ok=false.
func AsString(v Value) (string, bool, error) {
	switch val := v.(type) {
	case String:
		return string(val), true, nil
	case *MutableString:
		return string(val.runes), true, nil
	case *Stream:
		if !val.lines {
			return "", false, nil
		}
		var sb strings.Builder
		for {
			if Interrupted() {
				_ = val.Close()
				return "", true, ErrInterrupted
			}
			item, ok, err := val.Next()
			if err != nil {
				return "", true, err
			}
			if !ok {
				return sb.String(), true, nil
			}
			sb.WriteString(lineText(item))
			sb.WriteByte('\n')
		}
	}
	return "", false, nil
}

// Materialize drains a stream into its eager equivalent — a line stream to
// its text (a String, each line newline-terminated), any other stream to a
// list — and returns non-stream values unchanged.
func Materialize(v Value) (Value, error) {
	s, isStream := v.(*Stream)
	if !isStream {
		return v, nil
	}
	if s.lines {
		text, _, err := AsString(s)
		if err != nil {
			return nil, err
		}
		return String(text), nil
	}
	lst, _, err := AsList(s)
	if err != nil {
		return nil, err
	}
	return lst, nil
}

const streamTruncNotice = "\n[truncated — filter in Scheme, or page with take]"

func truncateRuneSafe(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// PrintValueCapped renders a value for an LLM tool result: streams drain and
// everything truncates at capBytes with a notice. A stream erroring mid-way
// returns the error, or the partial output with the error noted inline.
func PrintValueCapped(v Value, capBytes int) (string, error) {
	s, isStream := v.(*Stream)
	if !isStream {
		out := PrintValue(v)
		if len(out) > capBytes {
			return truncateRuneSafe(out, capBytes) + streamTruncNotice, nil
		}
		return out, nil
	}

	var sb strings.Builder
	truncated := false
	var streamErr error
	first := true
	for {
		if sb.Len() > capBytes {
			truncated = true
			_ = s.Close()
			break
		}
		item, ok, err := s.Next()
		if err != nil {
			streamErr = err
			break
		}
		if !ok {
			break
		}
		if s.lines {
			sb.WriteString(lineText(item))
			sb.WriteByte('\n')
		} else {
			if !first {
				sb.WriteByte(' ')
			}
			sb.WriteString(PrintValue(item))
		}
		first = false
	}
	if streamErr != nil && sb.Len() == 0 {
		return "", streamErr
	}
	out := sb.String()
	if len(out) > capBytes {
		out = truncateRuneSafe(out, capBytes)
		truncated = true
	}
	if s.lines {
		out = "\"" + out + "\""
	} else {
		out = "(" + out + ")"
	}
	if truncated {
		out += streamTruncNotice
	}
	if streamErr != nil {
		out += fmt.Sprintf("\n[stream error: %v]", streamErr)
	}
	return out, nil
}

// StreamFromReader is the exported spelling of streamFromReader for
// callers outside the package that hand text to the pipeline as lines
// (`docs --raw`). closefn may be nil.
func StreamFromReader(r io.Reader, closefn func() error) *Stream {
	return streamFromReader(r, closefn)
}
