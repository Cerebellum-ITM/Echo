package cmd

import (
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

// BuildHuhTheme returns a huh theme whose accents come from the active
// Echo palette so the form harmonises with the rest of the CLI.
func BuildHuhTheme(p theme.Palette) *huh.Theme {
	t := huh.ThemeBase()

	bold := lipgloss.NewStyle().Bold(true)

	t.Focused.Base = lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderLeft(true).
		BorderForeground(p.Accent).
		PaddingLeft(1)

	t.Focused.Title = bold.Foreground(p.Accent)
	t.Focused.NoteTitle = bold.Foreground(p.Accent)
	t.Focused.Description = lipgloss.NewStyle().Foreground(p.Dim)
	t.Focused.ErrorIndicator = lipgloss.NewStyle().Foreground(p.Error)
	t.Focused.ErrorMessage = lipgloss.NewStyle().Foreground(p.Error)
	t.Focused.SelectSelector = lipgloss.NewStyle().Foreground(p.Accent2).SetString("> ")
	t.Focused.NextIndicator = lipgloss.NewStyle().Foreground(p.Accent).SetString("›")
	t.Focused.PrevIndicator = lipgloss.NewStyle().Foreground(p.Accent).SetString("‹")
	t.Focused.Option = lipgloss.NewStyle().Foreground(p.Fg)
	t.Focused.MultiSelectSelector = lipgloss.NewStyle().Foreground(p.Accent2).SetString("> ")
	t.Focused.SelectedOption = lipgloss.NewStyle().Foreground(p.Accent).Bold(true)
	t.Focused.SelectedPrefix = lipgloss.NewStyle().Foreground(p.Success).SetString("✓ ")
	t.Focused.UnselectedOption = lipgloss.NewStyle().Foreground(p.Fg)
	t.Focused.UnselectedPrefix = lipgloss.NewStyle().Foreground(p.Faint).SetString("• ")
	t.Focused.FocusedButton = lipgloss.NewStyle().
		Foreground(p.Bg).Background(p.Accent).Bold(true).Padding(0, 2)
	t.Focused.BlurredButton = lipgloss.NewStyle().
		Foreground(p.Dim).Background(p.Bg).Padding(0, 2)
	t.Focused.TextInput.Cursor = lipgloss.NewStyle().Foreground(p.Accent2)
	t.Focused.TextInput.Placeholder = lipgloss.NewStyle().Foreground(p.Faint)
	t.Focused.TextInput.Prompt = lipgloss.NewStyle().Foreground(p.Accent)
	t.Focused.TextInput.Text = lipgloss.NewStyle().Foreground(p.Fg)

	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderForeground(p.Faint)
	t.Blurred.Title = bold.Foreground(p.Dim)
	t.Blurred.Description = lipgloss.NewStyle().Foreground(p.Faint)
	t.Blurred.SelectSelector = lipgloss.NewStyle().SetString("  ")
	t.Blurred.MultiSelectSelector = lipgloss.NewStyle().SetString("  ")

	t.Help.Ellipsis = lipgloss.NewStyle().Foreground(p.Faint)
	t.Help.ShortKey = lipgloss.NewStyle().Foreground(p.Dim)
	t.Help.ShortDesc = lipgloss.NewStyle().Foreground(p.Faint)
	t.Help.ShortSeparator = lipgloss.NewStyle().Foreground(p.Faint)
	t.Help.FullKey = lipgloss.NewStyle().Foreground(p.Dim)
	t.Help.FullDesc = lipgloss.NewStyle().Foreground(p.Faint)
	t.Help.FullSeparator = lipgloss.NewStyle().Foreground(p.Faint)

	return t
}
