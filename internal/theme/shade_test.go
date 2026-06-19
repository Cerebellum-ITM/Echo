package theme

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestLightenDarkenBounds(t *testing.T) {
	c := lipgloss.Color("#68d391")

	if got := Lighten(c, 0); got != c {
		t.Errorf("Lighten t=0 should be identity, got %q", got)
	}
	if got := Darken(c, 0); got != c {
		t.Errorf("Darken t=0 should be identity, got %q", got)
	}
	if got := Lighten(c, 1); got != lipgloss.Color("#ffffff") {
		t.Errorf("Lighten t=1 should be white, got %q", got)
	}
	if got := Darken(c, 1); got != lipgloss.Color("#000000") {
		t.Errorf("Darken t=1 should be black, got %q", got)
	}
}

func TestLightenMidpoint(t *testing.T) {
	// #808080 lightened halfway to white → #c0c0c0 (128 + (255-128)/2 = 191.5→192).
	if got := Lighten(lipgloss.Color("#808080"), 0.5); got != lipgloss.Color("#c0c0c0") {
		t.Errorf("midpoint lighten = %q, want #c0c0c0", got)
	}
}

func TestShadeNonHexUnchanged(t *testing.T) {
	c := lipgloss.Color("12") // ANSI 256 index, not #rrggbb
	if got := Lighten(c, 0.5); got != c {
		t.Errorf("non-hex color should be returned unchanged, got %q", got)
	}
}
