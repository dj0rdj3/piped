package execution

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"

	"piped/internal/secrets"
)

// secretWriter masks exact values before output is persisted. Only a suffix
// that could still complete a secret is held, so unrelated prompts remain live.
// CRLF variants cover the PTY's normal newline conversion. Encoded or otherwise
// transformed values cannot be reliably recognized by exact-value masking.
type secretWriter struct {
	mu       sync.Mutex
	output   io.Writer
	patterns [][]byte
	matcher  *regexp.Regexp
	pending  []byte
}

func newSecretWriter(output io.Writer, variables []secrets.Variable) (*secretWriter, error) {
	writer := &secretWriter{output: output}
	seen := map[string]bool{}
	expressions := []string{}
	for _, variable := range variables {
		for _, value := range [][]byte{variable.Value, bytes.ReplaceAll(variable.Value, []byte("\n"), []byte("\r\n"))} {
			if len(value) == 0 || seen[string(value)] {
				continue
			}
			seen[string(value)] = true
			writer.patterns = append(writer.patterns, bytes.Clone(value))
			expressions = append(expressions, regexp.QuoteMeta(string(value)))
		}
	}
	if len(expressions) > 0 {
		matcher, err := regexp.Compile(strings.Join(expressions, "|"))
		if err != nil {
			return nil, errors.New("cannot prepare job secret masking")
		}
		matcher.Longest()
		writer.matcher = matcher
	}
	return writer, nil
}

func (w *secretWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.matcher == nil {
		return w.output.Write(data)
	}
	total := len(data)
	for len(data) > 0 {
		count := min(len(data), 4096)
		w.pending = append(w.pending, data[:count]...)
		data = data[count:]
		if err := w.emit(false); err != nil {
			return 0, err
		}
	}
	return total, nil
}

func (w *secretWriter) partialStart() int {
	start := len(w.pending)
	for _, pattern := range w.patterns {
		for size := min(len(w.pending), len(pattern)-1); size > 0; size-- {
			if bytes.Equal(w.pending[len(w.pending)-size:], pattern[:size]) {
				start = min(start, len(w.pending)-size)
				break
			}
		}
	}
	return start
}

func (w *secretWriter) emit(final bool) error {
	for len(w.pending) > 0 {
		match := w.matcher.FindIndex(w.pending)
		partial := len(w.pending)
		if !final {
			partial = w.partialStart()
		}
		if match == nil || partial <= match[0] {
			if partial == 0 {
				return nil
			}
			if _, err := w.output.Write(w.pending[:partial]); err != nil {
				return err
			}
			w.pending = w.pending[partial:]
			if !final {
				return nil
			}
			continue
		}
		if _, err := w.output.Write(w.pending[:match[0]]); err != nil {
			return err
		}
		if _, err := io.WriteString(w.output, "[REDACTED]"); err != nil {
			return err
		}
		w.pending = w.pending[match[1]:]
	}
	return nil
}

func (w *secretWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer func() {
		clear(w.pending)
		for _, pattern := range w.patterns {
			clear(pattern)
		}
	}()
	if w.matcher == nil {
		return nil
	}
	return w.emit(true)
}
