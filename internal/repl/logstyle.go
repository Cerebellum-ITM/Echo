package repl

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/pascualchavez/echo/internal/theme"
)

// buildLogStyles returns a *log.Styles tuned to the active palette so
// charmbracelet/log lines emitted from the REPL match the surrounding
// theme. Only the level token (ERRO/WARN) is colorised — no
// background fill — so the log line stays readable on any terminal.
// Structured keys get per-key colors so the eye can jump straight to
// the relevant field (module=, errors=, warnings=, copied=).
func buildLogStyles(p theme.Palette) *log.Styles {
	styles := log.DefaultStyles()
	styles.Levels[log.ErrorLevel] = lipgloss.NewStyle().
		SetString("ERRO").
		Bold(true).
		Foreground(p.Error)
	styles.Levels[log.WarnLevel] = lipgloss.NewStyle().
		SetString("WARN").
		Bold(true).
		Foreground(p.Warning)

	if styles.Keys == nil {
		styles.Keys = map[string]lipgloss.Style{}
	}
	styles.Keys["module"] = lipgloss.NewStyle().Foreground(p.Accent).Bold(true)
	styles.Keys["err"] = lipgloss.NewStyle().Foreground(p.Error).Bold(true)
	styles.Keys["errors"] = lipgloss.NewStyle().Foreground(p.Error).Bold(true)
	styles.Keys["warnings"] = lipgloss.NewStyle().Foreground(p.Warning).Bold(true)
	styles.Keys["copied"] = lipgloss.NewStyle().Foreground(p.Info).Bold(true)
	return styles
}
