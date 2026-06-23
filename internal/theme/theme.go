package theme

import (
	"fmt"
	"strconv"

	"github.com/charmbracelet/lipgloss"
)

// MiddleTruncate shortens s to at most max display runes, keeping its head
// and tail around a single … in the middle — so a long value (e.g. a
// database name in a log line) doesn't push the rest of the line into a
// wrap. s already within max, or max <= 1, is returned unchanged. Rune-aware.
func MiddleTruncate(s string, max int) string {
	r := []rune(s)
	if max <= 1 || len(r) <= max {
		return s
	}
	keep := max - 1 // runes kept besides the …
	head := (keep + 1) / 2
	tail := keep - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

type Palette struct {
	Bg, Fg, Dim, Faint            lipgloss.Color
	Accent, Accent2               lipgloss.Color
	Success, Warning, Error, Info lipgloss.Color
}

type Stage string

const (
	StageDev     Stage = "dev"
	StageStaging Stage = "staging"
	StageProd    Stage = "prod"
)

func (p Palette) PromptColor(s Stage) lipgloss.Color {
	switch s {
	case StageProd:
		return p.Error
	case StageStaging:
		return p.Warning
	default:
		return p.Success
	}
}

var Charm = Palette{
	Bg:      "#13111c",
	Fg:      "#e8e3f5",
	Dim:     "#8b80a8",
	Faint:   "#5a5074",
	Accent:  "#b794f6",
	Accent2: "#f687b3",
	Success: "#68d391",
	Warning: "#fde047",
	Error:   "#fc8181",
	Info:    "#63b3ed",
}

var Hacker = Palette{
	Bg:      "#0a0e0a",
	Fg:      "#d4f4d4",
	Dim:     "#7a9a7a",
	Faint:   "#4a5a4a",
	Accent:  "#39ff14",
	Accent2: "#00d9ff",
	Success: "#39ff14",
	Warning: "#ffd700",
	Error:   "#ff4444",
	Info:    "#00d9ff",
}

var Odoo = Palette{
	Bg:      "#1a1322",
	Fg:      "#f0e9f5",
	Dim:     "#a094b3",
	Faint:   "#6b5e7d",
	Accent:  "#a47bc4",
	Accent2: "#e8a87c",
	Success: "#7bcf9f",
	Warning: "#e8a87c",
	Error:   "#e87878",
	Info:    "#7ba3c4",
}

var Tokyo = Palette{
	Bg:      "#1a1b26",
	Fg:      "#c0caf5",
	Dim:     "#7982a9",
	Faint:   "#565f89",
	Accent:  "#7aa2f7",
	Accent2: "#bb9af7",
	Success: "#9ece6a",
	Warning: "#e0af68",
	Error:   "#f7768e",
	Info:    "#7dcfff",
}

func PaletteByName(name string) Palette {
	switch name {
	case "hacker":
		return Hacker
	case "odoo":
		return Odoo
	case "tokyo":
		return Tokyo
	default:
		return Charm
	}
}

// Lighten returns c mixed toward white by t (t in [0,1]); t=0 returns c
// unchanged, t=1 returns white. Used to derive banner gradient/segment
// shades from an active-theme color instead of hardcoding hex (invariant 1).
func Lighten(c lipgloss.Color, t float64) lipgloss.Color {
	return mix(c, 255, 255, 255, t)
}

// Darken returns c mixed toward black by t (t in [0,1]); t=0 returns c
// unchanged, t=1 returns black.
func Darken(c lipgloss.Color, t float64) lipgloss.Color {
	return mix(c, 0, 0, 0, t)
}

// mix blends the #rrggbb color c toward (tr,tg,tb) by t, per channel. If c is
// not a parseable 6-digit hex it is returned unchanged.
func mix(c lipgloss.Color, tr, tg, tb int, t float64) lipgloss.Color {
	r, g, b, ok := parseHex(string(c))
	if !ok {
		return c
	}
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	lerp := func(from, to int) int { return int(float64(from) + (float64(to)-float64(from))*t + 0.5) }
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", lerp(r, tr), lerp(g, tg), lerp(b, tb)))
}

// parseHex parses a "#rrggbb" string into channel ints.
func parseHex(s string) (r, g, b int, ok bool) {
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff), true
}

func StageFromString(s string) Stage {
	switch s {
	case "staging":
		return StageStaging
	case "prod":
		return StageProd
	default:
		return StageDev
	}
}

type Styles struct {
	Out, Dim, Faint, Info, Ok, Warn, Err, Accent, Label lipgloss.Style
	Project, Bracket, Tilde, Dollar                     lipgloss.Style
}

func New(p Palette, stage Stage) Styles {
	pc := p.PromptColor(stage)
	return Styles{
		Out:    lipgloss.NewStyle().Foreground(p.Fg),
		Dim:    lipgloss.NewStyle().Foreground(p.Dim),
		Faint:  lipgloss.NewStyle().Foreground(p.Faint),
		Info:   lipgloss.NewStyle().Foreground(p.Info),
		Ok:     lipgloss.NewStyle().Foreground(p.Success),
		Warn:   lipgloss.NewStyle().Foreground(p.Warning),
		Err:    lipgloss.NewStyle().Foreground(p.Error),
		Accent: lipgloss.NewStyle().Foreground(p.Accent).Bold(true),
		Label:  lipgloss.NewStyle().Foreground(p.Warning).Bold(true),

		Project: lipgloss.NewStyle().Foreground(pc).Bold(true),
		Bracket: lipgloss.NewStyle().Foreground(pc).Bold(true),
		Tilde:   lipgloss.NewStyle().Foreground(p.Info),
		Dollar:  lipgloss.NewStyle().Foreground(p.Fg),
	}
}
