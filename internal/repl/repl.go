package repl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/banner"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
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
	Version     = "0.27.0"
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
	// recipe is true while a recipe (RunRecipe) is executing. It suppresses
	// the interactive `help` pager for a `help` step so a recipe never blocks
	// on an alt-screen viewer mid-run; the step falls back to flat printing.
	recipe bool
	// exitCode records the outcome of the last dispatched command for
	// one-shot (script) mode. It is set by the terminal log helpers
	// (finalize, *FailureLog, readonlyFinalize, …) and read by RunOnce /
	// RunRecipe. In the interactive REPL it is set but never read.
	exitCode int
	// quit is set when a command surfaces cmd.ErrQuit (Ctrl+X inside a
	// picker): the Start loop breaks on it so Echo exits entirely, the same
	// as Ctrl+X at the line prompt. Detected by the terminal log helpers.
	quit bool
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
	// lastViewFrom / lastViewRemote remember the last view's remote source
	// so `view --last` replays from the same target when the current args
	// carry no remote flag of their own.
	lastViewFrom   string
	lastViewRemote bool
	// liveRuns stacks the live cmd-log files of nested dispatches
	// (sequence steps, build mode); captured lines go to the top one. A
	// nil slot is a run whose live file failed and was dropped.
	liveRuns []*config.CmdLogLive
	// extras holds what the current dispatch learned for its record beyond
	// the captured lines; dispatchParsed scopes it to one dispatch.
	extras recordExtras
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
	case errors.Is(err, cmd.ErrQuit):
		return exitOK
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
		Banner:   cfg.Banner,
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
	logDBMax = cfg.LogDBMax
	jsonLogs = os.Getenv("ECHO_LOG_FORMAT") == "json"
	captureLine = sess.capture
	cmd.OnRemoteResolved = sess.noteRemoteResolved
	return sess, unknown
}

// Start renders the header and enters the interactive prompt loop.
func Start(s theme.Styles, p theme.Palette, project, id string, stage theme.Stage, version, themeName, username, cwd string, cfg *config.Config) {
	sess, unknown := newSession(s, p, project, id, stage, version, themeName, username, cwd, cfg)
	sess.interactive = true
	sess.pruneCmdLogs()

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
		if sess.quit {
			// Ctrl+X inside a picker asked to close Echo entirely.
			break
		}
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
	"help", "clear", "copy-last", "report", "logview", "sequence",
	"init", "reset", "alias", "link", "doctor",
	"up", "down", "stop", "restart", "ps", "logs", "push", "deploy", "sideload", "watch", "checkpoint", "actions", "promote",
	"install", "update", "uninstall", "test", "modules", "modinfo", "modstate", "probe", "view", "compare", "lint",
	"i18n-export", "i18n-update", "i18n-pull",
	"db-admin", "db-backup", "db-restore", "db-pull", "db-push", "db-drop", "db-neutralize", "db-list", "db-use",
	"shell", "shell-run", "bash", "psql", "connect",
}

// isMetaCommand returns true for commands whose output should not be
// recorded as "the last command" — they are about the REPL itself, not
// about a project action. Calling `copy-last` after `copy-last` should
// still copy the previously-buffered command, not just the ok line.
func isMetaCommand(name string) bool {
	switch name {
	case "copy-last", "help", "clear", "logview":
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
	if name, ok := HelpRequest(append([]string{cmd}, args...)); ok {
		cmd, args = "help", []string{name}
	}

	if !isMetaCommand(cmd) {
		sess.lastOutput.Reset()
	}

	// Persist the captured output as a history record when the command
	// finishes (Unit 81). Deferred so the build-mode early return is still
	// recorded; fires before runStepCaptured's post-dispatch buffer reset,
	// so recipe steps land as their own records. The live file shares the
	// record's stem and is removed only after the record is written, so a
	// recorded run never disappears between the two. The caller's extras
	// are set aside so a nested step's remote target or script lands in the
	// step's record only.
	started := time.Now()
	pushed := sess.pushLiveRun(cmd, args, started)
	outerExtras := sess.extras
	sess.extras = recordExtras{}
	defer func() {
		sess.saveCmdLog(cmd, args, started)
		sess.extras = outerExtras
		if pushed {
			sess.popLiveRun()
		}
	}()

	// Build mode (--build / -b) is universal: intercept before the switch,
	// but only for commands the switch actually routes — an unknown command
	// with --build falls through to the unknown-command path.
	if clean, build := stripBuildFlag(args); build && isDispatchName(cmd) {
		sess.runBuild(ctx, cmd, clean)
		return
	}

	switch cmd {
	case "help":
		sess.runHelp(args)
	case "clear":
		sess.clearAndRenderHeader()
	case "copy-last":
		sess.runCopyLast(args)
	case "report":
		sess.runReport(args)
	case "logview":
		sess.runLogview(ctx, args)
	case "sequence":
		sess.runSequence(ctx, args)
	case "init":
		sess.runInit(ctx, args)
	case "reset":
		sess.runReset()
	case "alias":
		sess.runAlias(ctx, args)
	case "link":
		sess.runLink(ctx, args)
	case "doctor":
		sess.runDoctor(ctx, args)
	case "up", "down", "stop", "restart", "ps", "logs":
		sess.runDocker(ctx, cmd, args)
	case "install", "update", "uninstall", "test", "modules":
		sess.runModules(ctx, cmd, args)
	case "modinfo":
		sess.runModinfo(ctx, args)
	case "modstate":
		sess.runModstate(ctx, args)
	case "probe":
		sess.runProbe(ctx, args)
	case "view":
		sess.runView(ctx, args)
	case "compare":
		sess.runCompare(ctx, args)
	case "lint":
		sess.runLint(args)
	case "i18n-export", "i18n-update":
		sess.runI18n(ctx, cmd, args)
	case "i18n-pull":
		sess.runI18nPull(ctx, args)
	case "db-admin", "db-backup", "db-restore", "db-pull", "db-drop", "db-neutralize", "db-list", "db-use":
		sess.runDB(ctx, cmd, args)
	case "shell", "bash", "psql":
		sess.runShell(ctx, cmd, args)
	case "shell-run":
		sess.runShellRun(ctx, args)
	case "connect":
		sess.runConnect(ctx, args)
	case "push":
		sess.runPush(ctx, args)
	case "deploy":
		sess.runDeploy(ctx, args)
	case "watch":
		sess.runWatch(ctx, args)
	case "checkpoint":
		sess.runCheckpoint(ctx, args)
	case "actions":
		sess.runActions(ctx, args)
	case "promote":
		sess.runPromote(ctx, args)
	case "sideload":
		sess.runSideload(ctx, args)
	case "db-push":
		sess.runDBPush(ctx, args)
	default:
		sess.print(Line{Kind: "warn", Text: "unknown command: " + cmd + " — try help"})
		sess.exitCode = exitUsage
	}
}

func (sess *session) runInit(ctx context.Context, args []string) {
	if len(args) > 0 {
		sess.runInitRemote(ctx, args)
		return
	}
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
	started := time.Now()
	wantJSON := name != "modules" && slices.Contains(args, "--json")
	lc := &logColorer{}
	stats := &runStats{}
	migs := &migrationTracker{}
	outcome := &odoo.Outcome{}
	// --json keeps stdout for the one result object: the run's lines go to
	// stderr, still captured for the run record.
	logOut := io.Writer(os.Stdout)
	render := func(line string) { sess.emitStreamLine(lc, line) }
	if wantJSON {
		logOut = os.Stderr
		render = func(line string) {
			sess.capture(Line{Kind: lc.classify(line), Text: line})
			os.Stderr.WriteString(line + "\n")
		}
	}
	opts := cmd.ModulesOpts{
		Cfg:         sess.cfg,
		Root:        sess.projectDir,
		Args:        args,
		Palette:     sess.palette,
		Interactive: sess.interactive,
		StreamOut: stats.wrap(func(line string) {
			migs.observe(line)
			outcome.Feed(stripANSISeq(line))
			render(line)
		}),
		// The start line is emitted here, once the module set is resolved
		// (after picker / --last), so it names the actual modules and the
		// flags the user passed (e.g. --i18n, --level).
		OnResolve: func(resolved []string) { sess.startResolved(logOut, name, args, resolved) },
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
		sess.runModulesList(ctx, opts, args)
		return
	}
	// A clean exit without a report, or with `of 0 tests`, is a typo'd tag
	// or module: Odoo calls that a success.
	if name == "test" && err == nil && outcome.Tests.Run == 0 {
		emitOdooLogTo(logOut, "ERROR", "echo.test", "no tests ran",
			[]logField{{"modules", moduleField(resolved)}, {"tags", cmd.TestTags(args, resolved)}},
			sess.styles, sess.palette, sess.cfg.DBName)
		stats.errors++
	}
	if wantJSON {
		sess.finishModulesJSON(name, resolved, err, stats, outcome, migs.migrations(), time.Since(started))
		return
	}

	var closing []logField
	if name == "test" {
		closing = testTallyFields(outcome.Tests)
	}
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive):
		sess.finalize(name, stats.errors, stats.warnings, err)
	case errors.Is(err, cmd.ErrUsage):
		sess.finalize(name, stats.errors, stats.warnings, err)
		sess.exitCode = exitUsage
	case err != nil, stats.errors > 0:
		sess.copyFailureLog(name, resolved, err, stats.errors, stats.warnings, closing...)
	default:
		sess.successLog(name, resolved, stats.warnings, closing...)
	}
	sess.emitTestFailures(outcome.Failures)
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
		Log:     sess.cmdOdooLogger(name),
		StreamOut: stats.wrap(func(line string) {
			sess.emitStreamLine(lc, line)
		}),
	}

	// A remote up/stop/restart leaves the local stack untouched, so the local
	// prompt health stays valid — only invalidate for a local run.
	local := func() bool { from, remote := remoteRunFlags(args); return from == "" && !remote }
	var err error
	switch name {
	case "up":
		err = cmd.RunUp(ctx, opts)
		if local() {
			sess.prompt.health.Invalidate()
		}
	case "down":
		err = cmd.RunDown(ctx, opts)
		sess.prompt.health.Invalidate()
	case "stop":
		err = cmd.RunStop(ctx, opts)
		if local() {
			sess.prompt.health.Invalidate()
		}
	case "restart":
		err = cmd.RunRestart(ctx, opts)
		if local() {
			sess.prompt.health.Invalidate()
		}
	case "ps":
		sess.runPSTable(ctx, opts)
		return
	case "logs":
		sess.readonlyFinalize("logs", cmd.RunLogs(ctx, opts))
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
// handleQuit intercepts a picker-initiated Ctrl+X (cmd.ErrQuit): it flags
// the session to exit the REPL loop and records a clean exit code, so the
// quit reads as a deliberate close rather than a command failure. Returns
// true when err was ErrQuit and the terminal helper should stop early.
func (sess *session) handleQuit(err error) bool {
	if !errors.Is(err, cmd.ErrQuit) {
		return false
	}
	sess.quit = true
	sess.exitCode = exitOK
	sess.lastErrors, sess.lastWarnings = 0, 0
	return true
}

func (sess *session) finalize(name string, errorCount, warnCount int, err error) {
	if sess.handleQuit(err) {
		return
	}
	sess.exitCode = scriptExitCode(err, errorCount)
	sess.lastErrors, sess.lastWarnings = errorCount, warnCount
	sess.print(Line{Kind: "out", Text: ""})
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		logger := echoCommandLogger(name, nil) + ".cancelled"
		emitOdooLog("WARNING", logger, name+" cancelled by user",
			nil, sess.styles, sess.palette, sess.cfg.DBName)
	case err != nil:
		fields := []logField{{"err", err.Error()}}
		var hinted interface{ Hint() string }
		if errors.As(err, &hinted) {
			fields = append(fields, logField{"hint", hinted.Hint()})
		}
		emitOdooLog("ERROR", failureLogger(name, nil), name+" failed",
			fields, sess.styles, sess.palette, sess.cfg.DBName)
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
		Log: func(level, step, msg, db string, fields ...[2]string) {
			logger := "echo." + name
			if step != "" {
				logger += "." + step
			}
			if db == "" {
				db = sess.cfg.DBName
			}
			lf := make([]logField, 0, len(fields))
			for _, f := range fields {
				lf = append(lf, logField{f[0], f[1]})
			}
			emitOdooLog(level, logger, msg, lf, sess.styles, sess.palette, db)
		},
	}

	if name == "db-list" {
		sess.runDBListTable(ctx, opts)
		return
	}

	var err error
	switch name {
	case "db-admin":
		err = cmd.RunDBAdmin(ctx, opts)
	case "db-use":
		err = cmd.RunDBUse(ctx, opts)
	case "db-backup":
		err = cmd.RunDBBackup(ctx, opts)
	case "db-restore":
		err = cmd.RunDBRestore(ctx, opts)
	case "db-pull":
		err = cmd.RunDBPull(ctx, opts)
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
		// Piped stdin (`cat fix.py | echo shell`) → headless pipe mode: feed
		// stdin to the Odoo shell through the shell-run machinery (local or
		// remote per --from/--remote) instead of demanding a TTY. Inside the
		// REPL stdin is always a TTY, so this only fires in one-shot runs.
		if cmd.StdinPiped() {
			sess.runShellPiped(ctx, args)
			return
		}
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
		// sub == "system" is the cross-command system-status line: it uses the
		// shared `echo.system.status` logger, not this command's namespace.
		logger := "echo.connect"
		switch {
		case sub == "system":
			logger = "echo.system.status"
		case sub != "":
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
	// Normalize away any embedded ANSI before parsing. `update`/`install`
	// run Odoo under `exec -T` (no TTY → plain logs), but `docker compose
	// logs` replays whatever the container stored, which carries Odoo's
	// ColoredFormatter SGR codes when it ran attached to a TTY. Those codes
	// break the Odoo/loguru prefix regexes, so the line would fall through
	// to a verbatim print with docker's native colors instead of Echo's
	// per-segment styling. Stripping here routes logs through the exact same
	// formatter as `update` (matching what the `shell` transform already does).
	line = stripANSISeq(line)
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
	sess.capture(l)
	if outputSuppressed(levelFromKind(l.Kind)) {
		return
	}
	if jsonLogs {
		writeJSONLine(os.Stdout, reportLine(l))
		teeRunLog(l.Text)
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

// printStyled prints a pre-rendered (ANSI-styled) line for display while
// capturing and logging its ANSI-free `plain` form — so `copy-last` and
// `--log` stay clean even when the display string carries per-segment color
// the standard Kind styling can't express (e.g. the push change tree).
func (sess *session) printStyled(rendered, plain, kind string) {
	l := Line{Kind: kind, Text: plain}
	sess.capture(l)
	if outputSuppressed(levelFromKind(kind)) {
		return
	}
	if jsonLogs {
		writeJSONLine(os.Stdout, reportLine(l))
	} else {
		fmt.Println(rendered)
	}
	teeRunLog(plain)
}
