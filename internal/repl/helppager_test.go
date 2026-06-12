package repl

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/pascualchavez/echo/internal/theme"
)

func testHelpPager() helpPager {
	return helpPager{
		pages: []helpPage{
			{title: "One", lines: []string{"a", "b"}},
			{title: "Two", lines: []string{"c"}},
			{title: "Three", lines: []string{"d"}},
		},
		palette: theme.PaletteByName(""),
	}
}

func key(s string) tea.KeyMsg {
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	switch s {
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+x":
		return tea.KeyMsg{Type: tea.KeyCtrlX}
	}
	return tea.KeyMsg{}
}

func TestHelpPagerSectionNavigationWraps(t *testing.T) {
	m := testHelpPager()

	next, _ := m.Update(key("right"))
	m = next.(helpPager)
	if m.page != 1 {
		t.Fatalf("right: page = %d, want 1", m.page)
	}

	next, _ = m.Update(key("left"))
	m = next.(helpPager)
	if m.page != 0 {
		t.Fatalf("left: page = %d, want 0", m.page)
	}

	// Wrap backward from the first page to the last.
	next, _ = m.Update(key("left"))
	m = next.(helpPager)
	if m.page != 2 {
		t.Fatalf("left wrap: page = %d, want 2", m.page)
	}

	// Wrap forward from the last page to the first.
	next, _ = m.Update(key("right"))
	m = next.(helpPager)
	if m.page != 0 {
		t.Fatalf("right wrap: page = %d, want 0", m.page)
	}
}

func TestHelpPagerScrollResetsOnPageChange(t *testing.T) {
	m := testHelpPager()
	m.height = helpChromeLines + 3 // 3 body rows
	m.offset = 1

	next, _ := m.Update(key("right"))
	m = next.(helpPager)
	if m.offset != 0 {
		t.Fatalf("offset = %d after page change, want 0", m.offset)
	}
}

func TestHelpPagerCtrlXFlagsQuit(t *testing.T) {
	m := testHelpPager()
	next, _ := m.Update(key("ctrl+x"))
	if !next.(helpPager).quit {
		t.Fatal("ctrl+x should set quit")
	}
}

func TestHelpPagerEnabled(t *testing.T) {
	origIn, origOut := stdinIsTTY, stdoutIsTTY
	defer func() { stdinIsTTY, stdoutIsTTY = origIn, origOut }()

	tty := func(in, out bool) {
		stdinIsTTY = func() bool { return in }
		stdoutIsTTY = func() bool { return out }
	}

	cases := []struct {
		name        string
		interactive bool
		recipe      bool
		in, out     bool
		want        bool
	}{
		{"interactive REPL", true, false, false, false, true},
		{"one-shot on a TTY", false, false, true, true, true},
		{"one-shot piped stdout", false, false, true, false, false},
		{"one-shot piped stdin", false, false, false, true, false},
		{"recipe step on a TTY", false, true, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tty(tc.in, tc.out)
			sess := &session{interactive: tc.interactive, recipe: tc.recipe}
			if got := sess.helpPagerEnabled(); got != tc.want {
				t.Fatalf("helpPagerEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHelpPagerViewShowsActiveTab(t *testing.T) {
	m := testHelpPager()
	v := m.View()
	for _, title := range []string{"One", "Two", "Three"} {
		if !strings.Contains(v, title) {
			t.Fatalf("view missing tab %q", title)
		}
	}
	if !strings.Contains(v, "(1/3)") {
		t.Fatalf("view missing page counter: %q", v)
	}
}
