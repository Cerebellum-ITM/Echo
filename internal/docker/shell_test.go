package docker

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// upper is a stand-in transform: it "restyles" any line that looks like an
// Odoo log (starts with a 4-digit year) by upper-casing it, and passes
// everything else through verbatim.
func upper(line string) (string, bool) {
	if len(line) >= 4 && line[:4] == "2026" {
		return strings.ToUpper(line), true
	}
	return "", false
}

func TestEmitCompleteLines(t *testing.T) {
	var out, capture bytes.Buffer
	in := []byte("2026-06-10 INFO odoo: hi\r\nplain line\nIn [1]: ")

	rem := emitCompleteLines(&out, &capture, in, upper)

	// The unterminated "In [1]: " prompt stays in the remainder.
	if string(rem) != "In [1]: " {
		t.Fatalf("remainder = %q, want %q", rem, "In [1]: ")
	}
	// Capture keeps the raw bytes of the two complete lines, no styling.
	wantCap := "2026-06-10 INFO odoo: hi\r\nplain line\n"
	if capture.String() != wantCap {
		t.Fatalf("capture = %q, want %q", capture.String(), wantCap)
	}
	// Out: the log line is transformed (preserving its \r\n), the plain line
	// passes through verbatim.
	wantOut := "2026-06-10 INFO ODOO: HI\r\nplain line\n"
	if out.String() != wantOut {
		t.Fatalf("out = %q, want %q", out.String(), wantOut)
	}
}

func TestCopyWithLineTransformFlushesPartialPrompt(t *testing.T) {
	pr, pw := io.Pipe()
	var out, capture bytes.Buffer
	done := make(chan struct{})
	go func() {
		copyWithLineTransform(&out, &capture, pr, upper)
		close(done)
	}()

	// A complete log line, then an unterminated interactive prompt.
	io.WriteString(pw, "2026-06-10 INFO odoo: ready\r\n")
	io.WriteString(pw, "In [1]: ")
	pw.Close()
	<-done

	if !strings.Contains(out.String(), "2026-06-10 INFO ODOO: READY") {
		t.Fatalf("out missing styled log line: %q", out.String())
	}
	// The prompt (no newline) must still reach the user.
	if !strings.HasSuffix(out.String(), "In [1]: ") {
		t.Fatalf("out missing flushed prompt: %q", out.String())
	}
	// Capture holds the raw, un-styled text of both.
	if capture.String() != "2026-06-10 INFO odoo: ready\r\nIn [1]: " {
		t.Fatalf("capture = %q", capture.String())
	}
}
