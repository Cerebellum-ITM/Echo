package banner

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

// bannerStyle selects which figlet rendering of the `echo` wordmark the
// header draws. Style is chosen at startup (random by default) and colored
// by the active stage — see resolveBannerStyle / renderEchoBanner.
type bannerStyle int

const (
	styleSoundwave bannerStyle = iota // B — Calvin S + soundwave underline
	styleShadow                       // D — ANSI Shadow with a vertical gradient
)

// echoSoundwave is the Calvin S "echo" wordmark (double-stroke) plus the
// soundwave line rendered just below it.
var echoSoundwave = []string{
	"╔═╗╔═╗╦ ╦╔═╗",
	"║╣ ║  ╠═╣║ ║",
	"╚═╝╚═╝╩ ╩╚═╝",
}

const echoSoundwaveLine = "▁▂▃▅▇▅▃▂▁▂▃▁"

// echoShadow is the ANSI Shadow "ECHO" wordmark; each row is tinted with a
// different gradient step (light at the top, dark at the bottom).
var echoShadow = []string{
	"███████╗ ██████╗██╗  ██╗ ██████╗",
	"██╔════╝██╔════╝██║  ██║██╔═══██╗",
	"█████╗  ██║     ███████║██║   ██║",
	"██╔══╝  ██║     ██╔══██║██║   ██║",
	"███████╗╚██████╗██║  ██║╚██████╔╝",
	"╚══════╝ ╚═════╝╚═╝  ╚═╝ ╚═════╝",
}

// echoShadowRipple is drawn to the right of the shadow wordmark (top 3 rows).
var echoShadowRipple = []string{"·", ")))", " ·"}

// Width thresholds for style D, computed from the art so they stay correct if
// the wordmark/ripple change. The left column must be at least this wide or the
// banner would overflow and break the right border:
//   - shadowWidth: minimum to draw the gradient wordmark (style D shows at all)
//   - shadowRippleWidth: extra room needed to also draw the ")))" ripple
// Below shadowWidth we fall back to style B (soundwave).
var (
	shadowWidth       = computeShadowWidth(false)
	shadowRippleWidth = computeShadowWidth(true)
)

func computeShadowWidth(withRipple bool) int {
	maxW := 0
	for i, l := range echoShadow {
		w := 1 + lipgloss.Width(l) // indent + wordmark
		if withRipple && i < len(echoShadowRipple) {
			w += 1 + lipgloss.Width(echoShadowRipple[i]) // space + ripple
		}
		if w > maxW {
			maxW = w
		}
	}
	return maxW
}

// resolveBannerStyle decides the banner style from the configured mode, whether
// the shadow style fits the available width, and an injected coin flip. mode is
// "auto" (default), "soundwave" or "shadow". When the resolved choice is shadow
// but it does not fit, it falls back to soundwave ("solo banners que quepan").
// coin returns true for shadow, false for soundwave.
func resolveBannerStyle(mode string, shadowFits bool, coin func() bool) bannerStyle {
	var want bannerStyle
	switch mode {
	case "soundwave":
		want = styleSoundwave
	case "shadow":
		want = styleShadow
	default: // "auto", "" or unknown
		if coin() {
			want = styleShadow
		} else {
			want = styleSoundwave
		}
	}
	if want == styleShadow && !shadowFits {
		return styleSoundwave
	}
	return want
}

// gradientFactors are applied to the stage base color, top row to bottom row,
// for the shadow wordmark: lighten the top, darken the bottom.
var gradientFactors = []float64{0.45, 0.22, 0.0, -0.12, -0.24, -0.36}

// rippleLighten / waveLighten control the accent shade of the ripple and the
// soundwave line relative to the stage base color.
const accentLighten = 0.35

// renderEchoBanner returns the styled lines of the `echo` banner for the given
// style, colored from the active theme's stage color. All hues are derived from
// palette.PromptColor(stage) via Lighten/Darken — no hardcoded hex (invariant 1).
// withRipple adds the ")))" flourish to style D (ignored by style B); the caller
// only enables it when the column is wide enough (see shadowRippleWidth).
func renderEchoBanner(p theme.Palette, stage theme.Stage, style bannerStyle, withRipple bool) []string {
	base := p.PromptColor(stage)

	if style == styleSoundwave {
		word := lipgloss.NewStyle().Foreground(base).Bold(true)
		wave := lipgloss.NewStyle().Foreground(theme.Lighten(base, accentLighten))
		lines := make([]string, 0, len(echoSoundwave)+1)
		for _, l := range echoSoundwave {
			lines = append(lines, "   "+word.Render(l))
		}
		return append(lines, "   "+wave.Render(echoSoundwaveLine))
	}

	// styleShadow: one gradient step per row, ripple joined on the first 3 rows.
	ripple := lipgloss.NewStyle().Foreground(theme.Lighten(base, accentLighten))
	lines := make([]string, 0, len(echoShadow))
	for i, l := range echoShadow {
		row := lipgloss.NewStyle().Foreground(shade(base, gradientFactors[i])).Bold(true).Render(l)
		if withRipple && i < len(echoShadowRipple) {
			row += " " + ripple.Render(echoShadowRipple[i])
		}
		lines = append(lines, " "+row)
	}
	return lines
}

// shade lightens (f>0) or darkens (f<0) c by |f|; f==0 returns c unchanged.
func shade(c lipgloss.Color, f float64) lipgloss.Color {
	switch {
	case f > 0:
		return theme.Lighten(c, f)
	case f < 0:
		return theme.Darken(c, -f)
	default:
		return c
	}
}
