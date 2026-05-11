package repl

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/banner"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// Line is a single piece of styled output.
type Line struct {
	Kind string // out, dim, faint, info, ok, warn, err, accent, label
	Text string
}

type session struct {
	styles     theme.Styles
	palette    theme.Palette
	bannerOpts banner.Opts
	project    string
	id         string
	stage      theme.Stage
	version    string
	cfg        *config.Config
	projectDir string
}

// Start renders the header and enters the interactive prompt loop.
func Start(s theme.Styles, p theme.Palette, project, id string, stage theme.Stage, version, themeName, username, cwd string, cfg *config.Config) {
	opts := banner.Opts{
		Version:  "0.2.0",
		Username: username,
		Theme:    themeName,
		Stage:    string(stage),
		Path:     cwd,
	}

	sess := &session{
		styles:     s,
		palette:    p,
		bannerOpts: opts,
		project:    project,
		id:         id,
		stage:      stage,
		version:    version,
		cfg:        cfg,
		projectDir: cwd,
	}

	sess.clearAndRenderHeader()

	ctx := context.Background()
	history := loadHistory()

	for {
		res, err := readLine(sess.renderPrompt(), history)
		if err != nil {
			sess.print(Line{Kind: "err", Text: "read error: " + err.Error()})
			break
		}
		if res.eof {
			fmt.Println()
			break
		}
		if res.aborted {
			continue
		}
		input := strings.TrimSpace(res.value)
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			break
		}

		history = appendHistory(history, input)
		sess.dispatch(ctx, input)
	}

	fmt.Println(s.Dim.Render("Goodbye!"))
}

func (sess *session) renderPrompt() string {
	s := sess.styles
	projectID := sess.project + "-" + sess.id
	icon := banner.LogoIcon(sess.cfg.Logo)
	return s.Accent.Render(icon) + " " +
		s.Project.Render(projectID) +
		s.Out.Render(" [") +
		s.Bracket.Render(string(sess.stage)+"/"+sess.version+".0") +
		s.Out.Render("]:") +
		s.Tilde.Render("~") +
		s.Dollar.Render("$ ")
}

func (sess *session) dispatch(ctx context.Context, input string) {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return
	}

	cmd, args := parts[0], parts[1:]

	switch cmd {
	case "ls":
		sess.runLS(ctx, args)
	case "clear":
		sess.clearAndRenderHeader()
	case "init":
		sess.runInit()
	case "reset":
		sess.runReset()
	default:
		sess.print(Line{Kind: "warn", Text: "unknown command: " + cmd + " — try help"})
	}
}

func (sess *session) runLS(ctx context.Context, args []string) {
	display := "ls -la"
	if len(args) > 0 {
		display += " " + strings.Join(args, " ")
	}
	sess.print(Line{Kind: "info", Text: "$ " + display})

	cmdArgs := append([]string{"-la"}, args...)
	out, err := exec.CommandContext(ctx, "ls", cmdArgs...).Output()
	if err != nil {
		sess.print(Line{Kind: "err", Text: err.Error()})
		return
	}

	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		sess.print(Line{Kind: "out", Text: line})
	}
}

func (sess *session) runInit() {
	ctx := context.Background()
	newCfg, err := cmd.RunInit(ctx, cmd.InitOpts{
		Cfg:       sess.cfg,
		Root:      sess.projectDir,
		Palette:   sess.palette,
		StreamOut: func(line string) { sess.print(Line{Kind: "dim", Text: line}) },
	})
	if err != nil {
		switch {
		case errors.Is(err, huh.ErrUserAborted), errors.Is(err, cmd.ErrCancelled):
			sess.print(Line{Kind: "warn", Text: "init cancelled — no changes saved"})
		default:
			sess.print(Line{Kind: "err", Text: "init error: " + err.Error()})
		}
		return
	}
	sess.cfg = newCfg
	sess.stage = theme.StageFromString(newCfg.Stage)
	sess.version = newCfg.OdooVersion
	sess.styles = theme.New(sess.palette, sess.stage)

	sess.print(Line{Kind: "ok", Text: "  Project configured"})
	sess.print(Line{Kind: "dim", Text: confLine("\U000f01a7", "version", newCfg.OdooVersion)})
	sess.print(Line{Kind: "dim", Text: confLine("\U000f023b", "stage", newCfg.Stage)})
	sess.print(Line{Kind: "dim", Text: confLine("", "odoo", newCfg.OdooContainer)})
	sess.print(Line{Kind: "dim", Text: confLine("", "db", newCfg.DBContainer)})
	sess.print(Line{Kind: "dim", Text: confLine("\U000f01bc", "db name", newCfg.DBName)})
}

// confLine renders a single line of the post-init banner with consistent
// column widths regardless of nerd-font glyph cell width.
func confLine(icon, label, value string) string {
	const (
		indent      = "    "
		iconCol     = 3
		labelCol    = 10
	)
	iconCell := lipgloss.NewStyle().Width(iconCol).Render(icon)
	labelCell := lipgloss.NewStyle().Width(labelCol).Render(label)
	return indent + iconCell + labelCell + value
}

// clearAndRenderHeader wipes the terminal (including scrollback) and
// reprints the welcome banner. Any command can call this to reset the
// visible context.
func (sess *session) clearAndRenderHeader() {
	fmt.Print("\033[H\033[2J\033[3J")
	fmt.Println(banner.Render(sess.styles, sess.palette, sess.bannerOpts))
}

func (sess *session) runReset() {
	result, err := cmd.RunReset(sess.cfg.ProjectKey, sess.palette)
	if err != nil {
		switch {
		case errors.Is(err, huh.ErrUserAborted), errors.Is(err, cmd.ErrCancelled):
			sess.print(Line{Kind: "warn", Text: "reset cancelled"})
		default:
			sess.print(Line{Kind: "err", Text: "reset error: " + err.Error()})
		}
		return
	}
	sess.print(Line{Kind: "ok", Text: fmt.Sprintf("  Reset (%s) — restart echo to re-detect", result.Scope)})
	for _, path := range result.Removed {
		sess.print(Line{Kind: "dim", Text: "    removed " + path})
	}
}

func (sess *session) print(l Line) {
	s := sess.styles
	var text string
	switch l.Kind {
	case "out":
		text = s.Out.Render(l.Text)
	case "dim":
		text = s.Dim.Render(l.Text)
	case "faint":
		text = s.Faint.Render(l.Text)
	case "info":
		text = s.Info.Render(l.Text)
	case "ok":
		text = s.Ok.Render(l.Text)
	case "warn":
		text = s.Warn.Render(l.Text)
	case "err":
		text = s.Err.Render(l.Text)
	case "accent":
		text = s.Accent.Render(l.Text)
	case "label":
		text = s.Label.Render(l.Text)
	default:
		text = l.Text
	}
	fmt.Println(text)
}
