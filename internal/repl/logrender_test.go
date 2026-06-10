package repl

import (
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/theme"
)

func TestStripANSISeq(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"\x1b[1;34mINFO\x1b[0m", "INFO"},
		{"a\x1b[32mb\x1b[0mc", "abc"},
		{"2026-06-10 55 \x1b[1;34mINFO\x1b[0m ? odoo: hi",
			"2026-06-10 55 INFO ? odoo: hi"},
	}
	for _, c := range cases {
		if got := stripANSISeq(c.in); got != c.want {
			t.Errorf("stripANSISeq(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Under `shell` (docker exec -t) Odoo colors its own logs; the transform must
// strip that ANSI so the line matches and gets Echo's styling instead.
func TestRenderLogLineStripsOdooColor(t *testing.T) {
	p := theme.PaletteByName("")
	s := theme.New(p, theme.StageFromString("dev"))
	colored := "2026-06-10 02:22:50,219 55 \x1b[1;34mINFO\x1b[0m ? odoo: Odoo version 18.0"

	// As-is, Odoo's SGR codes break the plain log-line regex.
	if _, ok := renderLogLine(colored, s, p); ok {
		t.Fatalf("colored line should not match before stripping")
	}
	// After stripping, it matches and renders.
	clean := stripANSISeq(colored)
	if strings.Contains(clean, "\x1b") {
		t.Fatalf("clean still has escapes: %q", clean)
	}
	if _, ok := renderLogLine(clean, s, p); !ok {
		t.Fatalf("clean line should match after stripping: %q", clean)
	}
}
