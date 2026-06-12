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
