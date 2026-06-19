package banner

import (
	"fmt"
	"math/rand"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
	"golang.org/x/term"
)

// Opts holds data needed to render the startup header.
type Opts struct {
	Version  string
	Username string
	Theme    string
	Stage    string
	Path     string
	// Banner selects the `echo` wordmark style: "auto" (random), "soundwave"
	// or "shadow". Empty is treated as "auto".
	Banner string
}

// Render returns the full startup header as a printable string.
func Render(s theme.Styles, p theme.Palette, opts Opts) string {
	w := termWidth()

	leftW := w * 2 / 5
	if leftW < 28 {
		leftW = 28
	}
	// row = │ space leftW space │ space rightW space │  → overhead = 7
	rightW := w - leftW - 7

	leftLines := buildLeft(s, p, opts, leftW)
	rightLines := buildRight(s, opts, rightW)

	for len(leftLines) < len(rightLines) {
		leftLines = append(leftLines, "")
	}
	for len(rightLines) < len(leftLines) {
		rightLines = append(rightLines, "")
	}

	ob := s.Faint.Render("│")
	sep := s.Faint.Render("│")

	rows := make([]string, 0, len(leftLines)+2)
	rows = append(rows, buildTopBar(s, opts.Version, w))

	for i := range leftLines {
		l := padRight(leftLines[i], leftW)
		r := padRight(rightLines[i], rightW)
		rows = append(rows, ob+" "+l+" "+sep+" "+r+" "+ob)
	}

	rows = append(rows, buildBottomBar(s, w))
	return strings.Join(rows, "\n")
}

func buildLeft(s theme.Styles, p theme.Palette, opts Opts, leftW int) []string {
	mode := opts.Banner
	shadowFits := leftW >= shadowWidth
	// ECHO_BANNER forces the style and bypasses the width guard — for previews,
	// demos and VHS recordings. On a terminal too narrow for the shadow banner
	// this may overflow the header box; that's the explicit opt-in price.
	if env := os.Getenv("ECHO_BANNER"); env == "soundwave" || env == "shadow" {
		mode, shadowFits = env, true
	}
	style := resolveBannerStyle(mode, shadowFits, func() bool { return rand.Intn(2) == 0 })
	logo := renderEchoBanner(p, theme.StageFromString(opts.Stage), style, leftW >= shadowRippleWidth)

	lines := []string{
		"",
		s.Out.Bold(true).Render(fmt.Sprintf("   Welcome back %s!", opts.Username)),
		"",
	}
	lines = append(lines, logo...)
	lines = append(lines,
		"",
		s.Dim.Render(fmt.Sprintf("   %s · %s", opts.Theme, opts.Stage)),
		s.Dim.Render("   "+shortPath(opts.Path)),
		"",
	)
	return lines
}

func buildRight(s theme.Styles, opts Opts, w int) []string {
	divider := s.Faint.Render(strings.Repeat("─", max(w-2, 4)))
	_ = opts
	return []string{
		"",
		s.Label.Render("Tips for getting started"),
		s.Out.Render("Run ") + s.Info.Render("help") + s.Out.Render(" to see all commands"),
		s.Out.Render("Type ") + s.Info.Render("exit") + s.Out.Render(" or Ctrl+D / Ctrl+X to quit"),
		divider,
		s.Label.Render("What's new"),
		s.Dim.Render("· Stage-colored startup banner"),
		s.Dim.Render("· Run ") + s.Info.Render("deploy") + s.Dim.Render(" to ship commits to a server"),
		s.Dim.Render("· Run ") + s.Info.Render("connect") + s.Dim.Render(" to open Odoo as any user"),
		"",
	}
}

func buildTopBar(s theme.Styles, version string, w int) string {
	cornerL := s.Faint.Render("╭")
	cornerR := s.Faint.Render("╮")
	title := "─── Echo v" + version + " "
	styledTitle := s.Accent.Render(title)
	used := lipgloss.Width(cornerL) + lipgloss.Width(styledTitle) + lipgloss.Width(cornerR)
	fill := w - used
	if fill < 1 {
		fill = 1
	}
	return cornerL + styledTitle + s.Faint.Render(strings.Repeat("─", fill)) + cornerR
}

func buildBottomBar(s theme.Styles, w int) string {
	cornerL := s.Faint.Render("╰")
	cornerR := s.Faint.Render("╯")
	fill := w - 2
	if fill < 1 {
		fill = 1
	}
	return cornerL + s.Faint.Render(strings.Repeat("─", fill)) + cornerR
}

// padRight pads a (possibly ANSI-styled) string to w visible cells.
func padRight(str string, w int) string {
	n := lipgloss.Width(str)
	if n >= w {
		return str
	}
	return str + strings.Repeat(" ", w-n)
}

func shortPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 40 {
		return 80
	}
	return w
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// LogoIcon returns the nerd-font glyph associated with a logo name.
func LogoIcon(name string) string {
	switch name {
	case "planet":
		return ""
	case "python":
		return "\U000f0320"
	case "anchor":
		return "\U000f0031"
	default:
		return ""
	}
}
