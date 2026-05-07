package theme

import "github.com/charmbracelet/lipgloss"

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
	Warning: "#f6ad55",
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
