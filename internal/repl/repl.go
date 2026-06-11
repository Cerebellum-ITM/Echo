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

// Version is the Echo binary version shown in the header. Bumped in
// the same commit that promotes the [Unreleased] section of
// CHANGELOG.md to a new [X.Y.Z] block. VersionMeta is a build-time
// suffix injected via `-ldflags` from the Makefile: always the build's
// commit (`+<shortsha>`), plus a `.dirty` marker when the working tree
// had uncommitted or untracked changes at build time. Showing the
// commit even on a clean build is deliberate — it pins exactly which
// revision a moved binary came from. Together they form a string like
// `0.5.0+abc1234` or `0.5.0+abc1234.dirty`. A plain `go build` without
// the Makefile leaves VersionMeta empty (bare semver).
var (
	Version     = "0.10.0"
	VersionMeta = ""
)

// FullVersion returns Version with the build metadata suffix applied.
func FullVersion() string { return Version + VersionMeta }

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
	// interactive is true only in the live REPL prompt loop (Start), not
	// in one-shot (RunOnce) or recipe (RunRecipe) dispatch. Gates the
	// `update` empty-picker "repeat last" confirmation.
	interactive bool
	// exitCode records the outcome of the last dispatched command for
	// one-shot (script) mode. It is set by the terminal log helpers
	// (finalize, *FailureLog, readonlyFinalize, …) and read by RunOnce /
	// RunRecipe. In the interactive REPL it is set but never read.
	exitCode int
	// lastErrors / lastWarnings mirror the last dispatched command's
	// runStats (the ERROR/CRITICAL and WARNING line counts). Set by the
	// same terminal helpers that set exitCode, reset at dispatch start,
	// and read by RunRecipe to build the per-step run summary (Unit 37).
	lastErrors   int
	lastWarnings int
	// lastModinfoModule / lastViewModule / lastViewFile remember the last
	// `modinfo` / `view` target so `--last` can replay it without the
	// picker (e.g. to copy a result first reached interactively). These
	// live only for the session — never persisted to disk.
	lastModinfoModule string
	lastViewModule    string
	lastViewFile      string
}

// Exit codes returned by one-shot (script) dispatch. The interactive REPL
// records them on the session but never surfaces them.
const (
	exitOK        = 0 // command completed, no ERROR lines
	exitError     = 1 // execution error or ERROR/CRITICAL lines counted
	exitUsage     = 2 // unknown command, bad args, or non-interactive guard
	exitCancelled = 3 // user aborted a confirm / picker (TTY only)
)

// scriptExitCode maps a command's terminal outcome (its returned error and
// streamed ERROR-line count) to a process exit code. Order matters:
// cancellation and the non-interactive guard are both "errors" but must
// not be reported as a generic execution failure.
func scriptExitCode(err error, errCount int) int {
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		return exitCancelled
	case errors.Is(err, cmd.ErrNonInteractive):
		return exitUsage
	case err != nil:
		return exitError
	case errCount > 0:
		return exitError
	default:
		return exitOK
	}
}

// newSession builds a session with the rendering and finalize state but
// without the interactive prompt loop. Shared by Start (the REPL) and the
// one-shot / recipe runners in script.go. Returns the unknown prompt
// segments so the caller can warn about them (the REPL does; script mode
// stays quiet). It mutates cfg.PromptSegments to the validated subset.
func newSession(s theme.Styles, p theme.Palette, project, id string, stage theme.Stage, version, themeName, username, cwd string, cfg *config.Config) (*session, []string) {
	opts := banner.Opts{
		Version:  FullVersion(),
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
	return sess, unknown
}

// Start renders the header and enters the interactive prompt loop.
func Start(s theme.Styles, p theme.Palette, project, id string, stage theme.Stage, version, themeName, username, cwd string, cfg *config.Config) {
	sess, unknown := newSession(s, p, project, id, stage, version, themeName, username, cwd, cfg)
	sess.interactive = true

	sess.clearAndRenderHeader()
	for _, u := range unknown {
		sess.print(Line{Kind: "warn", Text: "unknown prompt segment in global.toml: " + u})
	}

	ctx := context.Background()
	history := loadHistory()

	for {
		res, err := readLine(sess.renderPrompt(), history, s.Info, sess.palette)
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
	"help", "clear", "copy-last", "report",
	"init", "reset", "alias",
	"up", "down", "stop", "restart", "ps", "logs",
	"install", "update", "uninstall", "test", "modules", "modinfo", "modstate", "view",
	"i18n-export", "i18n-update", "i18n-pull",
	"db-backup", "db-restore", "db-drop", "db-neutralize", "db-list",
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
	sess.dispatchParsed(ctx, parts[0], parts[1:])
}

// dispatchParsed routes an already-tokenized command. It is the shared
// core of the interactive REPL loop (via dispatch) and the one-shot /
// recipe runners, which call it directly to avoid re-splitting.
func (sess *session) dispatchParsed(ctx context.Context, cmd string, args []string) {
	sess.exitCode = exitOK
	sess.lastErrors, sess.lastWarnings = 0, 0

	if !isMetaCommand(cmd) {
		sess.lastOutput.Reset()
	}

	// Build mode (--build / -b) is universal: intercept before the switch,
	// but only for commands the switch actually routes — an unknown command
	// with --build falls through to the unknown-command path.
	if clean, build := stripBuildFlag(args); build && isDispatchName(cmd) {
		sess.runBuild(ctx, cmd, clean)
		return
	}

	switch cmd {
	case "help":
		sess.runHelp()
	case "clear":
		sess.clearAndRenderHeader()
	case "copy-last":
		sess.runCopyLast(args)
	case "report":
		sess.runReport(args)
	case "init":
		sess.runInit()
	case "reset":
		sess.runReset()
	case "alias":
		sess.runAlias(ctx, args)
	case "up", "down", "stop", "restart", "ps", "logs":
		sess.runDocker(ctx, cmd, args)
	case "install", "update", "uninstall", "test", "modules":
		sess.runModules(ctx, cmd, args)
	case "modinfo":
		sess.runModinfo(ctx, args)
	case "modstate":
		sess.runModstate(ctx, args)
	case "view":
		sess.runView(ctx, args)
	case "i18n-export", "i18n-update":
		sess.runI18n(ctx, cmd, args)
	case "i18n-pull":
		sess.runI18nPull(ctx, args)
	case "db-backup", "db-restore", "db-drop", "db-neutralize", "db-list":
		sess.runDB(ctx, cmd, args)
	case "shell", "bash", "psql":
		sess.runShell(ctx, cmd, args)
	case "connect":
		sess.runConnect(ctx, args)
	default:
		sess.print(Line{Kind: "warn", Text: "unknown command: " + cmd + " — try help"})
		sess.exitCode = exitUsage
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
			{"alias [<name>]", "Register this project as `-C <name>` (no args: list)"},
			{"  --list", "List all project aliases"},
			{"  --rm <name>", "Remove an alias"},
			{"  --migrate", "Backfill aliases from connect targets (local paths)"},
		}},
		{"Modules", []helpEntry{
			{"install <mod...>", "Install modules in the current DB"},
			{"  --with-demo", "Include demo data"},
			{"  --level <lvl>", "Odoo --log-level (debug…critical; default info)"},
			{"update <mod...>", "Update modules"},
			{"  --all", "Update every installed module"},
			{"  --last", "Repeat the last update for this database"},
			{"  --i18n", "Overwrite the modules' translations from their .po (all langs)"},
			{"  --level <lvl>", "Odoo --log-level (debug…critical; default info)"},
			{"uninstall <mod...>", "Uninstall modules"},
			{"  --level <lvl>", "Odoo --log-level (debug…critical; default info)"},
			{"test <mod...>", "Run tests for installed modules (filters to /<mod>)"},
			{"  --update", "Reload modules first (adds -u; needed for XML/schema changes)"},
			{"  --tags <spec>", "Override --test-tags (e.g. :TestX.test_y, -external)"},
			{"modules", "List modules from configured addons paths"},
			{"  --config", "Pick which folders are addons paths (form)"},
			{"modinfo [<mod>]", "Compare DB-installed version vs manifest version"},
			{"  --copy", "Copy the report to the clipboard"},
			{"  --last", "Re-show this session's last modinfo (skips the picker)"},
			{"modstate", "Dump module states from the DB (installed-only)"},
			{"  --all", "Include every module state, not just installed"},
			{"  --json", "Emit a JSON array to stdout (logs to stderr)"},
			{"view [<mod>]", "Pick a module file and view it (bat, else plain)"},
			{"  --copy", "Copy the file to the clipboard instead"},
			{"  --last", "Re-display this session's last viewed file (skips pickers)"},
		}},
		{"i18n", []helpEntry{
			{"i18n-export <mod> [lang]", "Export <mod>/i18n/<lang>.po (default es_MX)"},
			{"  --out <path>", "Write to <path> instead of the module's i18n/"},
			{"i18n-update <mod> [lang]", "Import the module's <lang>.po into the DB (--i18n-overwrite)"},
			{"  --force", "Skip the prod-stage confirmation prompt"},
			{"i18n-pull [<mod>] [lang]", "Pull a module's <lang>.po from a remote instance into the repo"},
			{"  --from <target>", "Use a named connect target (default: project's [connect])"},
			{"  --all", "Pull every candidate module"},
			{"  --installed", "List candidates from the DB (all installed), not just the project's addons"},
		}},
		{"Database", []helpEntry{
			{"db-backup [name]", "Dump DB (default: configured) to ./backups/"},
			{"  --with-filestore", "Include filestore (.zip instead of .dump)"},
			{"db-restore [--as N]", "Pick a backup and restore (creates DB)"},
			{"  --force", "Replace target DB (terminates its connections)"},
			{"  --neutralize", "Neutralize the DB after restoring"},
			{"db-drop [name]", "Drop a database (confirmation by default)"},
			{"  --force", "Skip confirm and terminate active connections"},
			{"db-neutralize [name]", "Neutralize a DB (disable mail/cron/payments)"},
			{"  --force", "Skip the active-DB / prod confirmation"},
			{"db-list", "List DBs with size, date; ● marks the active one"},
		}},
		{"Shell", []helpEntry{
			{"bash", "Bash session inside the Odoo container"},
			{"psql", "PostgreSQL client against the configured DB"},
			{"shell", "Odoo Python shell against the configured DB"},
			{"connect [<login>]", "Impersonate a user (mint session, open Chrome logged in)"},
			{"  --all", "Include inactive users in the picker"},
			{"  --fresh", "Ignore the cached session and mint a new one"},
			{"  --new-window", "Open an isolated incognito window (default: reuse a tab)"},
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
			{"report", "Inspect/copy the last run's logs by step and level"},
			{"  --step=<N>", "Only that step (default: all)"},
			{"  --level=<lvl>", "Only lines of that level (debug…critical)"},
			{"  --min-level=<lvl>", "That level and more severe"},
			{"  --copy", "Copy the matched lines (default: print)"},
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

	// Script mode is one-shot only (no REPL command), so it lives outside
	// helpSections() — which is cross-checked against the command Registry.
	sess.print(Line{Kind: "out", Text: ""})
	sess.print(Line{Kind: "accent", Text: "Scripting (one-shot, outside the REPL)"})
	for _, it := range []helpEntry{
		{"echo <cmd> [args]", "Run one command and exit with a status code"},
		{"echo run <file>", "Run a recipe (one command per line); - reads stdin"},
		{"  --pick", "Pick a .echo recipe from the current directory"},
		{"  --last", "Run the most recently created .echo recipe"},
		{"  --continue-on-error", "Run every step instead of stopping at the first failure"},
		{"  --log[=<path>]", "Save a plain transcript (default dir, a file, or --log=. for ./<recipe>.log)"},
		{"  <step> --silent[=lvl]", "Silence a step's output (screen+log); =lvl keeps that level and above"},
		{"echo -C <dir> <cmd>", "Run from outside the project directory"},
	} {
		label := lipgloss.NewStyle().Width(22).Render(it.cmd)
		fmt.Println("  " + s.Info.Render(label) + s.Out.Render(it.desc))
	}

	// Build mode is universal (any routed command) and interactive, so it
	// lives outside helpSections() — keeping the Registry cross-check clean.
	sess.print(Line{Kind: "out", Text: ""})
	sess.print(Line{Kind: "accent", Text: "Build mode (compose interactively)"})
	buildLabel := lipgloss.NewStyle().Width(22).Render("<cmd> --build")
	fmt.Println("  " + s.Info.Render(buildLabel) +
		s.Out.Render("Interactively compose the command (pickers + flags), then run/copy it"))
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
		sess.exitCode = scriptExitCode(err, 0)
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
		indent   = "    "
		iconCol  = 3
		labelCol = 10
	)
	iconCell := lipgloss.NewStyle().Width(iconCol).Render(icon)
	labelCell := lipgloss.NewStyle().Width(labelCol).Render(label)
	return indent + iconCell + labelCell + value
}

// clearAndRenderHeader wipes the terminal (including scrollback) and
// reprints the welcome banner. Any command can call this to reset the
// visible context.
func (sess *session) runModules(ctx context.Context, name string, args []string) {
	lc := &logColorer{}
	stats := &runStats{}
	migs := &migrationTracker{}
	opts := cmd.ModulesOpts{
		Cfg:         sess.cfg,
		Root:        sess.projectDir,
		Args:        args,
		Palette:     sess.palette,
		Interactive: sess.interactive,
		StreamOut: stats.wrap(func(line string) {
			migs.observe(line)
			sess.emitStreamLine(lc, line)
		}),
		// The start line is emitted here, once the module set is resolved
		// (after picker / --last), so it names the actual modules.
		OnResolve: func(resolved []string) { sess.startResolved(name, resolved) },
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
	case "test":
		resolved, err = cmd.RunTest(ctx, opts)
	case "modules":
		sess.startLog(name, args)
		err = cmd.RunModules(ctx, opts)
		sess.readonlyFinalize(name, err)
		return
	}
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive):
		sess.finalize(name, stats.errors, stats.warnings, err)
	case err != nil, stats.errors > 0:
		sess.copyFailureLog(name, resolved, err, stats.errors, stats.warnings)
	default:
		sess.successLog(name, resolved, stats.warnings)
	}
	// Migration summary closes the run, after the success/failure recap.
	sess.emitMigrations(name, resolved, migs.migrations())
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
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive):
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
			sess.emitStreamLine(lc, line)
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
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive):
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
	sess.exitCode = scriptExitCode(err, errorCount)
	sess.lastErrors, sess.lastWarnings = errorCount, warnCount
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
	case "db-neutralize":
		err = cmd.RunDBNeutralize(ctx, opts)
	}
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive):
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
		// Colorize Odoo's startup logs (printed raw through the PTY) so they
		// match the rest of Echo's Odoo-styled output; non-log lines (IPython
		// banner, prompt, eval output) pass through verbatim. Under a TTY
		// (docker exec -t) Odoo colors its own logs, so strip its ANSI first
		// — otherwise the level/logger SGR codes break the log-line match and
		// the line would slip through wearing Odoo's coloring, not Echo's.
		opts.LineTransform = func(line string) (string, bool) {
			clean := stripANSISeq(line)
			if styled, ok := renderLogLine(clean, sess.styles, sess.palette); ok {
				return styled, true
			}
			// Restyle the shell's namespace globals and fade the
			// Python/IPython banner so the block reads as Echo's.
			if styled, ok := styleShellBanner(clean, sess.styles, sess.palette); ok {
				return styled, true
			}
			// Loose-severity stderr (wkhtmltopdf `Warn:`/`Error:` …): reformat
			// to Echo's Odoo style under the synthetic report.wkhtmltopdf
			// logger — same fallback the update/install stream uses (Unit 36).
			if ll, ok := parseLooseSeverity(clean); ok {
				return renderOdooLog(ll.level, looseSeverityLogger, ll.message, nil,
					sess.styles, sess.palette, sess.cfg.DBName), true
			}
			return "", false
		}
		captured, interrupted, err = cmd.RunOdooShell(ctx, opts)
	}
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		sess.exitCode = exitCancelled
		sess.print(Line{Kind: "warn", Text: name + " cancelled"})
	case errors.Is(err, cmd.ErrNonInteractive):
		sess.finalize(name, 0, 0, err)
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
		Log:     sess.connectLogger(),
	})
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		sess.finalize("connect", 0, 0, err)
		return
	case err != nil:
		sess.connectFailureLog(err)
		return
	}

	verb := "session minted"
	if res.Reused {
		verb = "session reused (cached)"
	}
	emitOdooLog("INFO", "echo.connect", verb,
		[]logField{
			{"login", res.Login},
			{"uid", strconv.Itoa(res.UID)},
			{"mode", connectModeLabel(res.Remote)},
			{"mfa", "bypassed"},
		},
		sess.styles, sess.palette, connectDB(res, sess.cfg.DBName))
	sess.finalize("connect", 0, 0, nil)
}

// connectModeLabel describes where the session was minted for the summary.
func connectModeLabel(remote bool) string {
	if remote {
		return "remote-ssh"
	}
	return "local"
}

// connectDB prefers the db the connect target resolved to (which, for a
// remote run, comes from the server's profile, not the local config).
func connectDB(res cmd.ConnectResult, fallback string) string {
	if res.DBName != "" {
		return res.DBName
	}
	return fallback
}

// connectLogger wires cmd.RunConnect's progress events into the REPL's
// Odoo-style log stream so every step (target, user query, cache
// validation, mint, chrome) renders like the rest of the CLI.
func (sess *session) connectLogger() cmd.ConnectLogger {
	return func(level, sub, msg, db string, fields ...[2]string) {
		logger := "echo.connect"
		if sub != "" {
			logger += "." + sub
		}
		if db == "" {
			db = sess.cfg.DBName
		}
		lf := make([]logField, 0, len(fields))
		for _, f := range fields {
			lf = append(lf, logField{f[0], f[1]})
		}
		emitOdooLog(level, logger, msg, lf, sess.styles, sess.palette, db)
	}
}

func (sess *session) clearAndRenderHeader() {
	fmt.Print("\033[H\033[2J\033[3J")
	fmt.Println(banner.Render(sess.styles, sess.palette, sess.bannerOpts))
}

func (sess *session) runReset() {
	result, err := cmd.RunReset(sess.cfg.ProjectKey, sess.palette)
	if err != nil {
		sess.exitCode = scriptExitCode(err, 0)
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

// emitStreamLine renders one streamed subprocess line. Foreign lines that
// Echo can normalize — docker compose progress, and loose-severity stderr
// (Warn:/Error: … from tools like wkhtmltopdf) — are reformatted into the
// Odoo log style; everything else goes through the kind classifier (which
// also keeps traceback continuations grouped via err/warn inheritance).
func (sess *session) emitStreamLine(lc *logColorer, line string) {
	if cl, ok := parseComposeProgress(line); ok {
		emitOdooLog(cl.level, "docker."+cl.resource, cl.state,
			[]logField{{"name", cl.name}},
			sess.styles, sess.palette, sess.cfg.DBName)
		return
	}
	// Don't hijack a line out of an active traceback (err/warn inheritance):
	// a bare `SomeError: …` tail must stay grouped with its frames.
	if lc.last != "err" && lc.last != "warn" {
		if ll, ok := parseLooseSeverity(line); ok {
			emitOdooLog(ll.level, looseSeverityLogger, ll.message, nil,
				sess.styles, sess.palette, sess.cfg.DBName)
			return
		}
	}
	sess.print(Line{Kind: lc.classify(line), Text: line})
}

func (sess *session) print(l Line) {
	// Capture for `report` even when the line is silenced — suppression is
	// about live noise, not losing the data.
	if sess.lastOutput != nil {
		sess.lastOutput.Add(l)
	}
	if outputSuppressed(levelFromKind(l.Kind)) {
		return
	}

	s := sess.styles
	var text string
	if rendered, ok := renderLogLine(l.Text, s, sess.palette); ok {
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
	fmt.Println(text)
	teeRunLog(l.Text)
}
