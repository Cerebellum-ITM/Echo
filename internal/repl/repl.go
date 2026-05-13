package repl

import (
	"context"
	"errors"
	"fmt"
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
	case "help":
		sess.runHelp()
	case "clear":
		sess.clearAndRenderHeader()
	case "init":
		sess.runInit()
	case "reset":
		sess.runReset()
	case "up", "down", "restart", "ps", "logs":
		sess.runDocker(ctx, cmd, args)
	case "install", "update", "uninstall", "modules":
		sess.runModules(ctx, cmd, args)
	default:
		sess.print(Line{Kind: "warn", Text: "unknown command: " + cmd + " — try help"})
	}
}

func (sess *session) runHelp() {
	type entry struct{ cmd, desc string }
	type section struct {
		title string
		items []entry
	}
	sections := []section{
		{"Project", []entry{
			{"init", "Configure Odoo project (containers, version, DB)"},
			{"reset", "Wipe Echo configuration (global / per-project / all)"},
		}},
		{"Modules", []entry{
			{"install <mod...>", "Install modules in the current DB"},
			{"  --with-demo", "Include demo data"},
			{"update <mod...>", "Update modules"},
			{"  --all", "Update every installed module"},
			{"uninstall <mod...>", "Uninstall modules"},
			{"modules", "List modules from configured addons paths"},
			{"  --config", "Pick which folders are addons paths (form)"},
		}},
		{"Docker", []entry{
			{"up [service]", "Start containers (compose up -d)"},
			{"down [service]", "Stop and remove containers"},
			{"restart [service]", "Restart services"},
			{"ps", "Show container status"},
			{"logs [service]", "Follow logs of Odoo (or [service]) — Ctrl+C to exit"},
			{"  -t N", "Tail last N lines (default 100)"},
			{"  --no-follow", "Disable follow; print bounded output"},
			{"  -c, --copy", "Bounded output and copy to clipboard"},
			{"  --all", "All compose services (instead of just Odoo)"},
		}},
		{"Shell", []entry{
			{"clear", "Clear screen and reprint header"},
			{"help", "Show this help"},
			{"exit, quit, Ctrl+D", "Quit Echo"},
		}},
	}

	s := sess.styles
	for i, sec := range sections {
		if i > 0 {
			sess.print(Line{Kind: "out", Text: ""})
		}
		sess.print(Line{Kind: "accent", Text: sec.title})
		for _, it := range sec.items {
			label := lipgloss.NewStyle().Width(22).Render(it.cmd)
			fmt.Println("  " + s.Info.Render(label) + s.Out.Render(it.desc))
		}
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
func (sess *session) runModules(ctx context.Context, name string, args []string) {
	display := name
	if len(args) > 0 {
		display += " " + strings.Join(args, " ")
	}
	sess.print(Line{Kind: "info", Text: "$ " + display})

	lc := &logColorer{}
	opts := cmd.ModulesOpts{
		Cfg:       sess.cfg,
		Root:      sess.projectDir,
		Args:      args,
		Palette:   sess.palette,
		StreamOut: func(line string) { sess.print(Line{Kind: lc.classify(line), Text: line}) },
	}

	var err error
	switch name {
	case "install":
		err = cmd.RunInstall(ctx, opts)
	case "update":
		err = cmd.RunUpdate(ctx, opts)
	case "uninstall":
		err = cmd.RunUninstall(ctx, opts)
	case "modules":
		err = cmd.RunModules(ctx, opts)
	}
	if err != nil {
		sess.print(Line{Kind: "err", Text: name + ": " + err.Error()})
	}
}

func (sess *session) runDocker(ctx context.Context, name string, args []string) {
	display := name
	if len(args) > 0 {
		display += " " + strings.Join(args, " ")
	}
	sess.print(Line{Kind: "info", Text: "$ " + display})

	lc := &logColorer{}
	opts := cmd.DockerOpts{
		Cfg:       sess.cfg,
		Root:      sess.projectDir,
		Args:      args,
		StreamOut: func(line string) { sess.print(Line{Kind: lc.classify(line), Text: line}) },
	}

	var err error
	switch name {
	case "up":
		err = cmd.RunUp(ctx, opts)
	case "down":
		err = cmd.RunDown(ctx, opts)
	case "restart":
		err = cmd.RunRestart(ctx, opts)
	case "ps":
		err = cmd.RunPS(ctx, opts)
	case "logs":
		err = cmd.RunLogs(ctx, opts)
	}
	if err != nil {
		sess.print(Line{Kind: "err", Text: name + ": " + err.Error()})
	}
}

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
