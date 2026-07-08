package repl

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

func newRenderSession() *session {
	p := theme.PaletteByName("")
	// Icons forced on so glyph-dependent render assertions are deterministic
	// regardless of the test process's TTY/TERM.
	return &session{palette: p, styles: theme.New(p, theme.StageDev), cfg: &config.Config{Icons: "on"}}
}

// Every module gets exactly one package glyph prefix, and no name is dropped.
func TestRenderModuleListIconPerItem(t *testing.T) {
	sess := newRenderSession()
	names := []string{"base", "sale", "account", "stock", "mrp"}

	out := strings.Join(sess.renderModuleList(names), "\n")

	if got := strings.Count(out, modIcon); got != len(names) {
		t.Fatalf("expected %d %q glyphs, got %d\n%q", len(names), modIcon, got, out)
	}
	for _, n := range names {
		if !strings.Contains(out, n) {
			t.Fatalf("module %q missing from render:\n%q", n, out)
		}
	}
}

// The list wraps to terminal width: no rendered row exceeds the fallback
// width (80), and a long list spans more than one row.
func TestRenderModuleListWraps(t *testing.T) {
	sess := newRenderSession()
	var names []string
	for i := 0; i < 40; i++ {
		names = append(names, "module_with_a_longish_name")
	}

	lines := sess.renderModuleList(names)

	if len(lines) < 2 {
		t.Fatalf("expected the list to wrap into multiple rows, got %d", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 80 {
			t.Fatalf("row %d visible width %d exceeds 80:\n%q", i, w, l)
		}
	}
}

// Empty input yields no rows (the caller emits the "no modules" line instead).
func TestRenderModuleListEmpty(t *testing.T) {
	sess := newRenderSession()
	if got := sess.renderModuleList(nil); len(got) != 0 {
		t.Fatalf("expected no rows for empty input, got %v", got)
	}
}
