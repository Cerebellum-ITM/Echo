package repl

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	lastOutput *lastOutputBuffer
	prompt     *promptBuilder
}

// Start renders the header and enters the interactive prompt loop.
func Start(s theme.Styles, p theme.Palette, project, id string, stage theme.Stage, version, themeName, username, cwd string, cfg *config.Config) {
	opts := banner.Opts{
		Version:  "0.5.0",
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
		lastOutput: newLastOutputBuffer(),
	}

	valid, unknown := validatePromptSegments(cfg.PromptSegments)
	cfg.PromptSegments = valid
	sess.prompt = newPromptBuilder(sess)

	sess.clearAndRenderHeader()
	for _, u := range unknown {
		sess.print(Line{Kind: "warn", Text: "unknown prompt segment in global.toml: " + u})
	}

	ctx := context.Background()
	history := loadHistory()

	for {
		res, err := readLine(sess.renderPrompt(), history, s.Info)
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
	return sess.prompt.Render(context.Background())
}

// dispatchNames lists every command name routed by `dispatch`. It is
// the second source of truth for the registry consistency test in
// registry_test.go; `exit` and `quit` are handled in Start (above) and
// are therefore not part of this slice.
var dispatchNames = []string{
	"help", "clear", "copy-last",
	"init", "reset",
	"up", "down", "stop", "restart", "ps", "logs",
	"install", "update", "uninstall", "modules",
	"i18n-export", "i18n-update",
	"db-backup", "db-restore", "db-drop", "db-list",
	"shell", "bash", "psql", "connect",
}

// isMetaCommand returns true for commands whose output should not be
// recorded as "the last command" — they are about the REPL itself, not
// about a project action. Calling `copy-last` after `copy-last` should
// still copy the previously-buffered command, not just the ok line.
func isMetaCommand(name string) bool {
	switch name {
	case "copy-last", "help", "clear":
		return true
	}
	return false
}

func (sess *session) dispatch(ctx context.Context, input string) {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return
	}

	cmd, args := parts[0], parts[1:]

	if !isMetaCommand(cmd) {
		sess.lastOutput.Reset()
	}

	switch cmd {
	case "help":
		sess.runHelp()
	case "clear":
		sess.clearAndRenderHeader()
	case "copy-last":
		sess.runCopyLast(args)
	case "init":
		sess.runInit()
	case "reset":
		sess.runReset()
	case "up", "down", "stop", "restart", "ps", "logs":
		sess.runDocker(ctx, cmd, args)
	case "install", "update", "uninstall", "modules":
		sess.runModules(ctx, cmd, args)
	case "i18n-export", "i18n-update":
		sess.runI18n(ctx, cmd, args)
	case "db-backup", "db-restore", "db-drop", "db-list":
		sess.runDB(ctx, cmd, args)
	case "shell", "bash", "psql":
		sess.runShell(ctx, cmd, args)
	case "connect":
		sess.runConnect(ctx, args)
	default:
		sess.print(Line{Kind: "warn", Text: "unknown command: " + cmd + " — try help"})
	}
}

type helpEntry struct{ cmd, desc string }
type helpSection struct {
	title string
	items []helpEntry
}

func helpSections() []helpSection {
	return []helpSection{
		{"Project", []helpEntry{
			{"init", "Configure Odoo project (containers, version, DB)"},
			{"reset", "Wipe Echo configuration (global / per-project / all)"},
		}},
		{"Modules", []helpEntry{
			{"install <mod...>", "Install modules in the current DB"},
			{"  --with-demo", "Include demo data"},
			{"update <mod...>", "Update modules"},
			{"  --all", "Update every installed module"},
			{"uninstall <mod...>", "Uninstall modules"},
			{"modules", "List modules from configured addons paths"},
			{"  --config", "Pick which folders are addons paths (form)"},
		}},
		{"i18n", []helpEntry{
			{"i18n-export <mod> [lang]", "Export <mod>/i18n/<lang>.po (default es_MX)"},
			{"  --out <path>", "Write to <path> instead of the module's i18n/"},
			{"i18n-update <mod> [lang]", "Import the module's <lang>.po into the DB (--i18n-overwrite)"},
			{"  --force", "Skip the prod-stage confirmation prompt"},
		}},
		{"Database", []helpEntry{
			{"db-backup [name]", "Dump DB (default: configured) to ./backups/"},
			{"  --with-filestore", "Include filestore (.zip instead of .dump)"},
			{"db-restore [--as N]", "Pick a backup and restore (creates DB)"},
			{"  --force", "Replace target DB if it already exists"},
			{"db-drop [name]", "Drop a database (confirmation by default)"},
			{"  --force", "Skip the confirmation prompt"},
			{"db-list", "List DBs with size, date; ● marks the active one"},
		}},
		{"Shell", []helpEntry{
			{"bash", "Bash session inside the Odoo container"},
			{"psql", "PostgreSQL client against the configured DB"},
			{"shell", "Odoo Python shell against the configured DB"},
			{"connect [<login>]", "Impersonate a user (mint session, open Chrome logged in)"},
			{"  --all", "Include inactive users in the picker"},
			{"  --force", "Skip the prod-stage confirmation prompt"},
		}},
		{"Docker", []helpEntry{
			{"up [service]", "Start containers (compose up -d)"},
			{"down [service]", "Stop and remove containers (prod confirm)"},
			{"  --force", "Skip the prod-stage confirmation prompt"},
			{"stop [service]", "Stop containers without removing them"},
			{"restart [service]", "Restart services"},
			{"ps", "Show container status"},
			{"logs [service]", "Follow logs of Odoo (or [service]) — Ctrl+C to exit"},
			{"  -t N", "Tail last N lines (default 100)"},
			{"  --no-follow", "Disable follow; print bounded output"},
			{"  -c, --copy", "Bounded output and copy to clipboard"},
			{"  --all", "All compose services (instead of just Odoo)"},
		}},
		{"Shell", []helpEntry{
			{"copy-last", "Copy the last command's output to clipboard"},
			{"  --errors", "Only copy error/warning lines"},
			{"clear", "Clear screen and reprint header"},
			{"help", "Show this help"},
			{"exit, quit, Ctrl+D", "Quit Echo"},
		}},
	}
}

func (sess *session) runHelp() {
	s := sess.styles
	for i, sec := range helpSections() {
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

// helpCommandNames extracts the flat set of top-level command names
// referenced in the help table. Used by the registry consistency test.
// Skips flag rows (leading whitespace or starting with "-") and
// keyboard tokens like "Ctrl+D".
func helpCommandNames() []string {
	var out []string
	for _, sec := range helpSections() {
		for _, it := range sec.items {
			if strings.HasPrefix(it.cmd, " ") || strings.HasPrefix(it.cmd, "-") {
				continue
			}
			for _, part := range strings.Split(it.cmd, ",") {
				fields := strings.Fields(part)
				if len(fields) == 0 {
					continue
				}
				name := fields[0]
				if strings.ContainsAny(name, "+<[") {
					continue
				}
				out = append(out, name)
			}
		}
	}
	return out
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
	sess.prompt.refresh(sess)

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
	sess.startLog(name, args)

	lc := &logColorer{}
	stats := &runStats{}
	opts := cmd.ModulesOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		StreamOut: stats.wrap(func(line string) {
			sess.print(Line{Kind: lc.classify(line), Text: line})
		}),
	}

	var err error
	var resolved []string
	switch name {
	case "install":
		resolved, err = cmd.RunInstall(ctx, opts)
	case "update":
		resolved, err = cmd.RunUpdate(ctx, opts)
	case "uninstall":
		resolved, err = cmd.RunUninstall(ctx, opts)
	case "modules":
		err = cmd.RunModules(ctx, opts)
		sess.readonlyFinalize(name, err)
		return
	}
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		sess.finalize(name, stats.errors, stats.warnings, err)
	case err != nil, stats.errors > 0:
		sess.copyFailureLog(name, resolved, err, stats.errors, stats.warnings)
	default:
		sess.successLog(name, resolved, stats.warnings)
	}
}

func (sess *session) runI18n(ctx context.Context, name string, args []string) {
	sess.startLog(name, args)

	lc := &logColorer{}
	stats := &runStats{}
	opts := cmd.I18nOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		StreamOut: stats.wrap(func(line string) {
			sess.print(Line{Kind: lc.classify(line), Text: line})
		}),
	}

	var err error
	switch name {
	case "i18n-export":
		err = cmd.RunI18nExport(ctx, opts)
	case "i18n-update":
		err = cmd.RunI18nUpdate(ctx, opts)
	}
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		sess.finalize(name, stats.errors, stats.warnings, err)
	case err != nil, stats.errors > 0:
		sess.commandFailureLog(name, err, stats.errors, stats.warnings)
	default:
		sess.finalize(name, stats.errors, stats.warnings, err)
	}
}

func (sess *session) runDocker(ctx context.Context, name string, args []string) {
	sess.startLog(name, args)

	lc := &logColorer{}
	stats := &runStats{}
	opts := cmd.DockerOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		StreamOut: stats.wrap(func(line string) {
			sess.print(Line{Kind: lc.classify(line), Text: line})
		}),
	}

	var err error
	switch name {
	case "up":
		err = cmd.RunUp(ctx, opts)
		sess.prompt.health.Invalidate()
	case "down":
		err = cmd.RunDown(ctx, opts)
		sess.prompt.health.Invalidate()
	case "stop":
		err = cmd.RunStop(ctx, opts)
		sess.prompt.health.Invalidate()
	case "restart":
		err = cmd.RunRestart(ctx, opts)
		sess.prompt.health.Invalidate()
	case "ps", "logs":
		var runErr error
		if name == "ps" {
			runErr = cmd.RunPS(ctx, opts)
		} else {
			runErr = cmd.RunLogs(ctx, opts)
		}
		sess.readonlyFinalize(name, runErr)
		return
	}
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		sess.finalize(name, stats.errors, stats.warnings, err)
	case err != nil, stats.errors > 0:
		sess.commandFailureLog(name, err, stats.errors, stats.warnings)
	default:
		sess.finalize(name, stats.errors, stats.warnings, err)
	}
}

// finalize emits the Odoo-style end-log line for non-module commands
// that stream output through sess.print (docker up/down/stop/restart,
// i18n-*, db-backup/restore/drop). Success → INFO `echo.<cmd>`,
// user cancellation → WARNING `echo.<cmd>.cancelled`, other errors →
// ERROR `echo.<cmd>.error`. Mirrors the start/end pair already used
// by module commands and shell sessions.
func (sess *session) finalize(name string, errorCount, warnCount int, err error) {
	sess.print(Line{Kind: "out", Text: ""})
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		logger := echoCommandLogger(name, nil) + ".cancelled"
		emitOdooLog("WARNING", logger, name+" cancelled by user",
			nil, sess.styles, sess.palette, sess.cfg.DBName)
	case err != nil:
		emitOdooLog("ERROR", failureLogger(name, nil), name+" failed",
			[]logField{{"err", err.Error()}},
			sess.styles, sess.palette, sess.cfg.DBName)
	case errorCount > 0:
		emitOdooLog("ERROR", failureLogger(name, nil), name+" finished with errors",
			[]logField{{"errors", strconv.Itoa(errorCount)}},
			sess.styles, sess.palette, sess.cfg.DBName)
	default:
		var fields []logField
		if warnCount > 0 {
			fields = append(fields, logField{"warnings", strconv.Itoa(warnCount)})
		}
		emitOdooLog("INFO", echoCommandLogger(name, nil), name+" completed",
			fields, sess.styles, sess.palette, sess.cfg.DBName)
	}
}

func (sess *session) runDB(ctx context.Context, name string, args []string) {
	sess.startLog(name, args)

	opts := cmd.DBOpts{
		Cfg:       sess.cfg,
		Root:      sess.projectDir,
		Args:      args,
		Palette:   sess.palette,
		StreamOut: func(line string) { sess.print(Line{Kind: "out", Text: line}) },
	}

	if name == "db-list" {
		err := cmd.RunDBList(ctx, opts)
		sess.readonlyFinalize(name, err)
		return
	}

	var err error
	switch name {
	case "db-backup":
		err = cmd.RunDBBackup(ctx, opts)
	case "db-restore":
		err = cmd.RunDBRestore(ctx, opts)
	case "db-drop":
		err = cmd.RunDBDrop(ctx, opts)
	}
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		sess.finalize(name, 0, 0, err)
	case err != nil:
		sess.commandFailureLog(name, err, 0, 0)
	default:
		sess.finalize(name, 0, 0, err)
	}
}

func (sess *session) runShell(ctx context.Context, name string, args []string) {
	sess.startLog(name, args)

	opts := cmd.ShellOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
	}

	var err error
	var captured string
	var interrupted bool
	switch name {
	case "bash":
		captured, interrupted, err = cmd.RunBash(ctx, opts)
	case "psql":
		captured, interrupted, err = cmd.RunPsql(ctx, opts)
	case "shell":
		captured, interrupted, err = cmd.RunOdooShell(ctx, opts)
	}
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		sess.print(Line{Kind: "warn", Text: name + " cancelled"})
	case interrupted:
		sess.shellCancelledLog(name)
	case err != nil:
		sess.shellFailureLog(name, captured, err)
	default:
		sess.shellExitLog(name)
	}
}

func (sess *session) runConnect(ctx context.Context, args []string) {
	sess.startLog("connect", args)

	res, err := cmd.RunConnect(ctx, cmd.ConnectOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
	})
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		sess.finalize("connect", 0, 0, err)
		return
	case err != nil:
		sess.connectFailureLog(err)
		return
	}

	where := "local"
	if res.Remote {
		where = "remote (ssh)"
	}
	sess.print(Line{Kind: "info", Text: fmt.Sprintf("Session minted for %q (uid=%d) — %s", res.Login, res.UID, where)})
	sess.print(Line{Kind: "info", Text: "Opening Chrome at " + res.BaseURL + "/odoo (logged in)"})
	sess.print(Line{Kind: "dim", Text: "MFA bypassed — dev only"})
	sess.finalize("connect", 0, 0, nil)
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
	if rendered, ok := formatOdooLine(l.Text, s, sess.palette); ok {
		text = rendered
	} else {
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
	}
	if sess.lastOutput != nil {
		sess.lastOutput.Add(l)
	}
	fmt.Println(text)
}
