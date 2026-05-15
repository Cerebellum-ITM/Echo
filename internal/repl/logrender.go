package repl

import (
	"regexp"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

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
	loggerStyle := lipgloss.NewStyle().Foreground(p.Info)

	return s.Dim.Render(ts) + " " +
		s.Faint.Render(pid) + " " +
		levelStyle.Render(short) + " " +
		dbStyle.Render(db) + " " +
		loggerStyle.Render(logger+":") + " " +
		s.Out.Render(msg), true
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
