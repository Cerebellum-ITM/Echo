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
	return styles
}
