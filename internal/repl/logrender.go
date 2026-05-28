package repl

import (
	"hash/fnv"
	"regexp"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

// loggerPalette is an 8-color pastel rotation assigned by name hash to
// each Odoo logger (odoo.service.server, odoo.modules.loading, …).
// Each logger renders consistently in the same tone across a session so
// the eye can group lines by origin.
var loggerPalette = []lipgloss.Color{
	"#ffb3ba", // coral
	"#ffd6a5", // peach
	"#caffbf", // mint
	"#9bf6ff", // cyan
	"#a0c4ff", // sky
	"#bdb2ff", // lavender
	"#ffc6ff", // pink
	"#f0a6ca", // rose
}

// loggerColor maps a logger name to one of the pastel rotation slots
// using FNV-1a so the assignment is stable across runs.
func loggerColor(logger string) lipgloss.Color {
	h := fnv.New32a()
	_, _ = h.Write([]byte(logger))
	return loggerPalette[h.Sum32()%uint32(len(loggerPalette))]
}

// odooLogLine matches the standard Odoo log line prefix:
//
//	YYYY-MM-DD HH:MM:SS,SSS PID LEVEL db logger: msg
//
// Tracebacks and stray stdout lines don't match and fall back to the
// existing kind-based styling in sess.print.
var odooLogLine = regexp.MustCompile(
	`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d{3}) (\d+) (DEBUG|INFO|WARNING|ERROR|CRITICAL) (\S+) ([^:]+): (.*)$`,
)

// formatOdooLine renders an Odoo log line with per-segment styling à la
// charmbracelet/log. Segment palette:
//
//	timestamp → dim          (low-contrast gray)
//	PID       → faint        (very low contrast — rarely needed)
//	LEVEL     → bold + level color (DEBU/INFO/WARN/ERRO/CRIT chip)
//	db        → palette.Accent (theme accent — distinguishes DB name)
//	logger    → palette.Info   (cool tone — looks like a code path)
//	message   → default fg     (highest contrast — the actual content)
//
// Returns the rendered string and a bool indicating whether the line
// matched. Tracebacks / stray stdout fall back to the kind-based path.
func formatOdooLine(line string, s theme.Styles, p theme.Palette) (string, bool) {
	m := odooLogLine.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	ts, pid, level, db, logger, msg := m[1], m[2], m[3], m[4], m[5], m[6]

	short, levelStyle := shortLevel(level, p)
	dbStyle := lipgloss.NewStyle().Foreground(p.Accent)
	loggerStyle := lipgloss.NewStyle().Foreground(loggerColor(logger))

	return s.Dim.Render(ts) + " " +
		s.Faint.Render(pid) + " " +
		levelStyle.Render(short) + " " +
		dbStyle.Render(db) + " " +
		loggerStyle.Render(logger+":") + " " +
		s.Out.Render(msg), true
}

// loguruLogLine matches and captures the parts of a loguru log line:
//
//	YYYY-MM-DD HH:MM:SS.mmm | LEVEL | module:func:line - msg
var loguruLogLine = regexp.MustCompile(
	`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d+) \| (DEBUG|INFO|WARNING|ERROR|CRITICAL) \| ([^:]+):([^:]+):(\d+) - (.*)$`,
)

// formatLoguruLine renders a loguru log line with per-segment styling.
// Segment palette mirrors the Odoo renderer where fields exist:
//
//	timestamp       → dim
//	LEVEL chip      → bold + level color (same shortLevel helper)
//	module path     → loggerColor pastel rotation (stable by name)
//	:func:line      → faint (low-contrast location suffix)
//	message         → default fg
func formatLoguruLine(line string, s theme.Styles, p theme.Palette) (string, bool) {
	m := loguruLogLine.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	ts, level, module, fn, lineno, msg := m[1], m[2], m[3], m[4], m[5], m[6]

	short, levelStyle := shortLevel(level, p)
	moduleStyle := lipgloss.NewStyle().Foreground(loggerColor(module))

	return s.Dim.Render(ts) + " " +
		levelStyle.Render(short) + " " +
		moduleStyle.Render(module) +
		s.Faint.Render(":"+fn+":"+lineno+":") + " " +
		s.Out.Render(msg), true
}

// renderLogLine tries the standard Odoo format first, then loguru.
// Falls back to ("", false) if neither matches, letting the caller
// apply kind-based styling.
func renderLogLine(line string, s theme.Styles, p theme.Palette) (string, bool) {
	if out, ok := formatOdooLine(line, s, p); ok {
		return out, true
	}
	return formatLoguruLine(line, s, p)
}

// shortLevel returns the 4-char display label and its style for an
// Odoo level token, mirroring charmbracelet/log's level chip.
func shortLevel(level string, p theme.Palette) (string, lipgloss.Style) {
	bold := lipgloss.NewStyle().Bold(true)
	switch level {
	case "DEBUG":
		return "DEBU", bold.Foreground(p.Faint)
	case "INFO":
		return "INFO", bold.Foreground(p.Info)
	case "WARNING":
		return "WARN", bold.Foreground(p.Warning)
	case "ERROR":
		return "ERRO", bold.Foreground(p.Error)
	case "CRITICAL":
		return "CRIT", bold.Foreground(p.Error)
	}
	return level, bold
}
