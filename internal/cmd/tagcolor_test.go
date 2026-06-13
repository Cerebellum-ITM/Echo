package cmd

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

func TestTagRe(t *testing.T) {
	cases := []struct {
		in   string
		want string // "" = no match
	}{
		{"  [IMP] real_estate: label", "[IMP]"},
		{"  [ADD] mod: thing", "[ADD]"},
		{"  no tag here", ""},
		{"  [2024] year not a tag", ""}, // digits, not a tag
		{"  [#42] issue ref", ""},
	}
	for _, c := range cases {
		got := ""
		if loc := tagRe.FindStringIndex(c.in); loc != nil {
			got = c.in[loc[0]:loc[1]]
		}
		if got != c.want {
			t.Errorf("tagRe on %q = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTagFallbackColorStable(t *testing.T) {
	// An unknown tag always hashes to the same slot.
	if tagFallbackColor("ZZZ") != tagFallbackColor("zzz") {
		t.Error("tagFallbackColor must be case-insensitive and stable")
	}
	if tagFallbackColor("ZZZ") != tagFallbackColor("ZZZ") {
		t.Error("tagFallbackColor must be deterministic")
	}
}

func TestRenderTailWithTags(t *testing.T) {
	p := theme.Palette{Success: "#0f0", Dim: "#888"}
	dim := lipgloss.NewStyle().Foreground(p.Dim)

	// A tagged tail keeps the tag text and the surrounding words.
	out := renderTailWithTags("  [ADD] sale: new flow", dim, p)
	for _, want := range []string{"[ADD]", "sale: new flow"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered tail missing %q: %q", want, out)
		}
	}

	// A tag-less tail is returned through the dim style unchanged in content.
	plain := renderTailWithTags("  host:/srv/odoo", dim, p)
	if !strings.Contains(plain, "host:/srv/odoo") {
		t.Errorf("plain tail lost its content: %q", plain)
	}
}
