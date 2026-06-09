package cmd

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/pascualchavez/echo/internal/theme"
)

// directConnectLogger returns a ConnectLogger for the projectless `echo
// connect` path. That flow runs outside the REPL, so the REPL's emitOdooLog
// (in package repl, which imports cmd) isn't reachable here. This renders
// the same Odoo-style line shape — `ts pid LEVEL db logger: msg key=val` —
// from the active palette, so the projectless command's output matches the
// in-REPL log stream.
func directConnectLogger(palette theme.Palette) ConnectLogger {
	return func(level, sub, msg, db string, fields ...[2]string) {
		logger := "echo.connect"
		if sub != "" {
			logger += "." + sub
		}
		os.Stdout.WriteString(renderOdooLogLine(palette, level, logger, msg, db, fields) + "\n")
	}
}

// renderOdooLogLine mirrors repl.emitOdooLog's segment styling: timestamp
// dim, pid faint, level chip (bold, per-level color), db accent, logger
// cool-toned, message default, and dim structured keys.
func renderOdooLogLine(p theme.Palette, level, logger, msg, db string, fields [][2]string) string {
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	ts = strings.Replace(ts, ".", ",", 1)
	pid := strconv.Itoa(os.Getpid())
	if db == "" {
		db = "-"
	}

	short, levelStyle := connectShortLevel(level, p)
	line := lipgloss.NewStyle().Foreground(p.Dim).Render(ts) + " " +
		lipgloss.NewStyle().Foreground(p.Faint).Render(pid) + " " +
		levelStyle.Render(short) + " " +
		lipgloss.NewStyle().Foreground(p.Accent).Render(db) + " " +
		lipgloss.NewStyle().Foreground(p.Info).Render(logger+":") + " " +
		msg

	keyStyle := lipgloss.NewStyle().Foreground(p.Dim)
	for _, f := range fields {
		line += " " + keyStyle.Render(f[0]) + "=" + quoteLogValue(f[1])
	}
	return line
}

// connectShortLevel mirrors repl.shortLevel: a 4-char level chip plus its
// bold, per-level color.
func connectShortLevel(level string, p theme.Palette) (string, lipgloss.Style) {
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

// quoteLogValue wraps values containing whitespace or quotes so the line
// stays parseable, matching repl.quoteIfNeeded.
func quoteLogValue(v string) string {
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, " \t\"") {
		return strconv.Quote(v)
	}
	return v
}
