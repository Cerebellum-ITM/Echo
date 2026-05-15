package repl

import "strings"

const lastOutputCap = 5000

// lastOutputBuffer holds the lines printed during the last executed
// command. `copy-last` reads from it. The buffer is reset by the
// dispatcher at the start of every non-meta command.
type lastOutputBuffer struct {
	lines     []Line
	truncated bool
}

func newLastOutputBuffer() *lastOutputBuffer {
	return &lastOutputBuffer{lines: make([]Line, 0, 64)}
}

func (b *lastOutputBuffer) Reset() {
	b.lines = b.lines[:0]
	b.truncated = false
}

func (b *lastOutputBuffer) Add(l Line) {
	if len(b.lines) >= lastOutputCap {
		b.lines = b.lines[1:]
		b.truncated = true
	}
	b.lines = append(b.lines, l)
}

// Filtered returns the lines whose Kind is in the allow-list. nil
// allow-list returns every line.
func (b *lastOutputBuffer) Filtered(kinds map[string]bool) []Line {
	if kinds == nil {
		out := make([]Line, len(b.lines))
		copy(out, b.lines)
		return out
	}
	out := make([]Line, 0, len(b.lines))
	for _, l := range b.lines {
		if kinds[l.Kind] {
			out = append(out, l)
		}
	}
	return out
}

// Plain renders the buffer as plain text (one Text per line), with a
// trailing newline. Kinds are dropped; lipgloss styles never made it
// into the Text field in the first place.
func (b *lastOutputBuffer) Plain() string {
	return linesToPlain(b.lines, b.truncated)
}

// PlainFiltered renders only the lines whose Kind is in allow-list.
func (b *lastOutputBuffer) PlainFiltered(kinds map[string]bool) string {
	return linesToPlain(b.Filtered(kinds), b.truncated)
}

func linesToPlain(lines []Line, truncated bool) string {
	var sb strings.Builder
	if truncated {
		sb.WriteString("… (output truncated, oldest lines dropped) …\n")
	}
	for _, l := range lines {
		sb.WriteString(l.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// Len returns the current number of buffered lines.
func (b *lastOutputBuffer) Len() int { return len(b.lines) }

// IsEmpty is true when no lines have been buffered since the last reset.
func (b *lastOutputBuffer) IsEmpty() bool { return len(b.lines) == 0 }
