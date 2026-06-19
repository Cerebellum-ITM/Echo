package banner

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

// TestBannerLinesFitWidth guards the header box: a banner rendered for a given
// column width must never produce a line wider than that width, or it would
// overflow the left column and break the right border. Covers both styles,
// with and without ripple, across every stage.
func TestBannerLinesFitWidth(t *testing.T) {
	for _, st := range []theme.Stage{theme.StageDev, theme.StageStaging, theme.StageProd} {
		// Soundwave must fit in the narrowest possible column (gate is shadowWidth).
		for _, line := range renderEchoBanner(theme.Charm, st, styleSoundwave, false) {
			if w := lipgloss.Width(line); w > shadowWidth {
				t.Errorf("stage=%s soundwave line width %d exceeds %d: %q", st, w, shadowWidth, line)
			}
		}
		// Shadow without ripple must fit in shadowWidth; with ripple in shadowRippleWidth.
		for _, line := range renderEchoBanner(theme.Charm, st, styleShadow, false) {
			if w := lipgloss.Width(line); w > shadowWidth {
				t.Errorf("stage=%s shadow(no ripple) line width %d exceeds %d: %q", st, w, shadowWidth, line)
			}
		}
		for _, line := range renderEchoBanner(theme.Charm, st, styleShadow, true) {
			if w := lipgloss.Width(line); w > shadowRippleWidth {
				t.Errorf("stage=%s shadow(ripple) line width %d exceeds %d: %q", st, w, shadowRippleWidth, line)
			}
		}
	}
}

func TestResolveBannerStyle(t *testing.T) {
	tcoin := func() bool { return true }  // would pick shadow
	fcoin := func() bool { return false } // would pick soundwave

	cases := []struct {
		name       string
		mode       string
		shadowFits bool
		coin       func() bool
		want       bannerStyle
	}{
		{"explicit soundwave", "soundwave", true, tcoin, styleSoundwave},
		{"explicit shadow fits", "shadow", true, fcoin, styleShadow},
		{"explicit shadow too narrow falls back", "shadow", false, tcoin, styleSoundwave},
		{"auto coin shadow fits", "auto", true, tcoin, styleShadow},
		{"auto coin soundwave", "auto", true, fcoin, styleSoundwave},
		{"auto coin shadow too narrow falls back", "auto", false, tcoin, styleSoundwave},
		{"empty mode treated as auto", "", true, tcoin, styleShadow},
		{"unknown mode treated as auto", "weird", true, fcoin, styleSoundwave},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveBannerStyle(c.mode, c.shadowFits, c.coin); got != c.want {
				t.Errorf("resolveBannerStyle(%q, %v) = %d, want %d", c.mode, c.shadowFits, got, c.want)
			}
		})
	}
}
