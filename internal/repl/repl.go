package repl

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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
	Version     = "0.24.0"
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
	"init", "reset", "alias", "link",
	"up", "down", "stop", "restart", "ps", "logs", "push", "deploy", "watch", "checkpoint", "actions", "promote",
	"install", "update", "uninstall", "test", "modules", "modinfo", "modstate", "view", "compare",
	"i18n-export", "i18n-update", "i18n-pull",
	"db-admin", "db-backup", "db-restore", "db-pull", "db-drop", "db-neutralize", "db-list", "db-use",
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

	if !isMetaCommand(cmd) {
		sess.lastOutput.Reset()
	}

	// Persist the captured output as a history record when the command
	// finishes (Unit 81). Deferred so the build-mode early return is still
	// recorded; fires before runStepCaptured's post-dispatch buffer reset,
	// so recipe steps land as their own records.
	started := time.Now()
	defer func() { sess.saveCmdLog(cmd, args, time.Since(started)) }()

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
	case "logview":
		sess.runLogview(ctx, args)
	case "sequence":
		sess.runSequence(ctx, args)
	case "init":
		sess.runInit()
	case "reset":
		sess.runReset()
	case "alias":
		sess.runAlias(ctx, args)
	case "link":
		sess.runLink(ctx, args)
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
	case "compare":
		sess.runCompare(ctx, args)
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
			{"link [<target>]", "Bind this directory to a connect target (no args: picker)"},
			{"  --show", "Show the binding, probe the remote, stream its `ps`"},
			{"  --rm", "Remove this directory's [connect] binding"},
		}},
		{"Modules", []helpEntry{
			{"install <mod...>", "Install modules in the current DB"},
			{"  --with-demo", "Include demo data"},
			{"  --level <lvl>", "Odoo --log-level (debug…critical; default info)"},
			{"update <mod...>", "Update modules"},
			{"  --all", "Update every installed module"},
			{"  --last", "Repeat the last update for this database"},
			{"  --i18n", "Overwrite the modules' translations from their .po (all langs)"},
			{"  --installed", "Pick from all installed modules (e.g. base), not just the repo"},
			{"  --level <lvl>", "Odoo --log-level (debug…critical; default info)"},
			{"  --from <target>", "Update on a remote instance (named connect target)"},
			{"  --remote", "Update on this directory's linked remote (see link)"},
			{"uninstall <mod...>", "Uninstall modules"},
			{"  --level <lvl>", "Odoo --log-level (debug…critical; default info)"},
			{"test <mod...>", "Run tests for installed modules (filters to /<mod>)"},
			{"  --update", "Reload modules first (adds -u; needed for XML/schema changes)"},
			{"  --tags <spec>", "Override --test-tags (e.g. :TestX.test_y, -external)"},
			{"  --from <t>", "Run the suite on a remote target (or --remote for the link binding)"},
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
			{"  --from <t>", "View the file from a remote target (or --remote for the link binding)"},
			{"compare [<mod>]", "Diff a local module file against its Docker copy"},
			{"  --all", "Compare the whole module: changed/added/missing table"},
			{"  --from <t>", "Compare against a remote target (or --remote for the link binding)"},
			{"  --copy", "Copy the diff to the clipboard"},
		}},
		{"i18n", []helpEntry{
			{"i18n-export <mod> [lang]", "Export <mod>/i18n/<lang>.po (default es_MX)"},
			{"  --out <path>", "Write to <path> instead of the module's i18n/"},
			{"i18n-update <mod> [lang]", "Import the module's <lang>.po into the DB (--i18n-overwrite)"},
			{"  --force", "Skip the prod-stage confirmation prompt"},
			{"i18n-pull [<mod>...] [lang]", "Pull one or more modules' <lang>.po from a remote into the repo"},
			{"  --from <target>", "Use a named connect target (default: project's [connect])"},
			{"  --lang <code>", "Language to pull (default es_MX); makes every positional a module"},
			{"  --all", "Pull every candidate module"},
			{"  --installed", "List candidates from the DB (all installed), not just the project's addons"},
		}},
		{"Database", []helpEntry{
			{"db-admin [name]", "Reset admin (uid 2) login+password to admin/admin"},
			{"  --force", "Skip the prod confirmation"},
			{"db-backup [name]", "Dump DB (default: configured) to ./backups/"},
			{"  --with-filestore", "Include filestore (.zip instead of .dump)"},
			{"db-restore [--as N]", "Pick a backup, name the target, and restore"},
			{"  --force", "Replace target DB (terminates its connections)"},
			{"  --neutralize", "Neutralize the DB after restoring"},
			{"db-pull", "Download a remote DB dump into ./backups/ (add --restore to load it into the local stack)"},
			{"  --from <target>", "Pull from a named connect target"},
			{"  --remote", "Pull from this directory's linked remote"},
			{"  --as <name>", "Local DB name (default: <remoteDB>_<target>)"},
			{"  --neutralize", "Force neutralize (default: only when source is prod)"},
			{"  --no-neutralize", "Skip neutralize even for a prod source"},
			{"  --filestore", "Also pull the DB's filestore attachments"},
			{"  --force", "Replace an existing local DB of the target name"},
			{"db-drop [name]", "Drop a database (confirmation by default)"},
			{"  --force", "Skip confirm and terminate active connections"},
			{"db-neutralize [name]", "Neutralize a DB (disable mail/cron/payments)"},
			{"  --force", "Skip the active-DB / prod confirmation"},
			{"db-list", "List DBs with size, date; ● marks the active one"},
			{"db-use [name]", "Switch the active DB (picker when no name)"},
		}},
		{"Shell", []helpEntry{
			{"bash", "Bash session inside the Odoo container"},
			{"psql", "PostgreSQL client against the configured DB"},
			{"shell", "Odoo Python shell against the configured DB"},
			{"  --from <target>", "Open the shell on a remote instance (named connect target)"},
			{"  --remote", "Open the shell on this directory's linked remote"},
			{"  --force", "Skip the prod-stage confirmation prompt"},
			{"shell-run [<file>]", "Run a .py through the Odoo shell (stdin); no file → picker"},
			{"  --no-copy", "Don't auto-copy the script output to the clipboard"},
			{"  --from <target>", "Run the script on a remote instance (named connect target)"},
			{"  --remote", "Run the script on this directory's linked remote"},
			{"  --force", "Skip the prod-stage confirmation prompt"},
			{"connect [<login>]", "Impersonate a user (mint session, open Chrome logged in)"},
			{"  --all", "Include inactive users in the picker"},
			{"  --fresh", "Ignore the cached session and mint a new one"},
			{"  --new-window", "Open an isolated incognito window (default: reuse a tab)"},
			{"  --force", "Skip the prod-stage confirmation prompt"},
		}},
		{"Docker", []helpEntry{
			{"up [service]", "Start containers (compose up -d)"},
			{"  --from <target>", "Start on a remote instance (named connect target)"},
			{"  --remote", "Start on this directory's linked remote"},
			{"down [service]", "Stop and remove containers (prod confirm)"},
			{"  --force", "Skip the prod-stage confirmation prompt"},
			{"stop [service]", "Stop containers without removing them"},
			{"  --from <target>", "Stop on a remote instance (named connect target)"},
			{"  --remote", "Stop on this directory's linked remote"},
			{"  --force", "Skip the remote prod-stage confirmation prompt"},
			{"restart [service]", "Restart services"},
			{"  --from <target>", "Restart on a remote instance (named connect target)"},
			{"  --remote", "Restart on this directory's linked remote"},
			{"  --force", "Skip the remote prod-stage confirmation prompt"},
			{"ps", "Show container status"},
			{"logs [service]", "Follow logs of Odoo (or [service]) — Ctrl+C to exit"},
			{"  -t N", "Tail last N lines (default 100)"},
			{"  --no-follow", "Disable follow; print bounded output"},
			{"  -c, --copy", "Bounded output and copy to clipboard"},
			{"  --all", "All compose services (instead of just Odoo)"},
			{"  --from <target>", "Follow logs on a remote instance (named connect target)"},
			{"  --remote", "Follow logs on this directory's linked remote"},
			{"push [<mod>...]", "Rsync local modules to the remote addons dir"},
			{"  --from <target>", "Use a named connect target (default: this dir's link)"},
			{"  --remote", "Push to this directory's linked remote"},
			{"  --dirty", "Push every module with uncommitted changes"},
			{"  --dry-run", "List the changes rsync would make; transfer nothing"},
			{"  --delete", "Remove remote files no longer present locally"},
			{"  --dest <path>", "Push to an explicit remote dir (skips auto-detect)"},
			{"  --pick-dest", "Browse the remote filesystem to choose the destination"},
			{"  --set-dest", "Set the remote push destination and exit (no push)"},
			{"  --mkdir", "Create the destination dir if it doesn't exist"},
			{"  --force", "Skip the prod-stage confirmation prompt"},
			{"deploy", "Deploy picked commits + dirty modules to a remote (stop, up, -i/-u)"},
			{"  --from <target>", "Use a named connect target (default: this dir's link)"},
			{"  --push", "Rsync the resolved modules to the remote before the run"},
			{"  --no-push", "Skip the push even when [deploy] push is the default"},
			{"  --set-push[=bool]", "Set deploy to push by default and exit (no deploy)"},
			{"  --limit <N>", "Commits offered in the picker (default 20)"},
			{"  --dry-run", "Resolve modules and show the plan; execute nothing"},
			{"  --force", "Skip the prod-stage confirmation prompt"},
			{"  --i18n", "Force --i18n-overwrite on the update run (default: auto when i18n/ changed)"},
			{"  --no-i18n", "Suppress --i18n-overwrite even when i18n/ changes are detected"},
			{"  --commits <shas>", "Deploy these commits non-interactively (skips the picker)"},
			{"  --modules <names>", "Deploy these modules non-interactively (skips the picker)"},
			{"  --auto", "Headless: deploy pending commits (ahead of upstream) + dirty modules, no picker"},
			{"  --json", "Emit a machine-readable deploy summary to stdout (logs to stderr)"},
			{"  --checkpoint[=db|dump]", "Force a DB checkpoint before the run (default: auto on staging/prod)"},
			{"  --no-checkpoint", "Skip the DB checkpoint even on staging/prod"},
			{"  --no-actions", "Skip declared [[deploy.actions]] for this run"},
			{"  --rollback", "Restore the target's most recent checkpoint (no deploy)"},
			{"watch [<branch>]", "Auto push+deploy when new commits land on a branch; no branch → picker (Ctrl+C to stop)"},
			{"  --from <target>", "Use a named connect target (default: this dir's link)"},
			{"  --remote", "Target this directory's linked remote"},
			{"  --interval <sec>", "Poll interval in seconds (default 10, min 2)"},
			{"  --force", "Required to watch a prod-stage target"},
			{"  --no-logs", "Don't follow the remote logs between cycles (silent wait)"},
			{"  --no-checkpoint", "Skip the DB checkpoint on each cycle's deploy"},
			{"  --no-actions", "Skip declared [[deploy.actions]] on each cycle's deploy"},
			{"checkpoint [list]", "List a target's DB checkpoints (size, age) + live DB size and disk free"},
			{"  --from <target>", "Use a named connect target (default: this dir's link)"},
			{"  --remote", "Target this directory's linked remote"},
			{"  --json", "Emit the checkpoint list as JSON to stdout (logs to stderr)"},
			{"  create", "Take a manual checkpoint (before a risky change)"},
			{"  create --method db|dump", "Checkpoint method (default: configured, else db)"},
			{"  rm [<name>]", "Delete a checkpoint (picker when no name); --all for every one"},
			{"  rm --force", "Skip the confirmation prompt"},
			{"actions [list]", "Manage [[deploy.actions]] interactively (list/add/edit/rm)"},
			{"  add", "Wizard: name → phase → where → exec dir (picker) → command"},
			{"  edit [<name>]", "Edit an action in place (picker when no name)"},
			{"  rm [<name>] [--force]", "Delete an action (picker when no name)"},
			{"  --from <target>/--remote", "Show the server list / target for the remote picker & upload"},
			{"  --json", "Emit the action list as JSON to stdout (with list)"},
			{"promote [<branch>]", "Funnel this worktree's changes onto the deploy branch (no args: picker)"},
			{"  --dirty [<folder>...]", "Move the current worktree's dirty patch (by folder); stays uncommitted"},
			{"  --commits <shas>", "Cherry-pick these commits from the source branch"},
			{"  --to <branch>", "Destination branch (else saved [promote] branch; prompts to pick if unset)"},
			{"  --set-branch <name>", "Persist the default promote branch and exit"},
			{"  --create-dest <path>", "Create the destination branch's worktree if none exists"},
			{"  --dry-run", "Preview the change tree / commit list; move nothing"},
		}},
		{"Session", []helpEntry{
			{"copy-last", "Copy the last command's output to clipboard"},
			{"  --errors", "Only copy error/warning lines"},
			{"report", "Inspect/copy the last run's logs by step and level"},
			{"  --step=<N>", "Only that step (default: all)"},
			{"  --level=<lvl>", "Only lines of that level (debug…critical)"},
			{"  --min-level=<lvl>", "That level and more severe"},
			{"  --copy", "Copy the matched lines (default: print)"},
			{"logview", "Browse past commands' logs (filter by text and level)"},
			{"  --list", "Print the run list without the interactive browser"},
			{"  --json", "Dump the run list as JSON to stdout (headless / agents)"},
			{"  --last", "Open the most recent run directly"},
			{"  --clear", "Delete this project's log history (--force skips confirm)"},
			{"sequence", "Pick several commands in order and run them (tri-state Tab)"},
			{"  --remote", "Run the whole sequence on this directory's linked remote"},
			{"  --from <target>", "Run the whole sequence on a named connect target"},
			{"  --last", "Repeat the last sequence run for this project"},
			{"  --continue-on-error", "Run every step instead of stopping at the first failure"},
			{"clear", "Clear screen and reprint header"},
			{"help", "Show this help"},
			{"exit, quit, Ctrl+D, Ctrl+X", "Quit Echo"},
		}},
	}
}

// scriptingHelpEntries documents the one-shot script mode. It lives outside
// helpSections() — which is cross-checked against the command Registry —
// because `echo <cmd>` is not a REPL command.
var scriptingHelpEntries = []helpEntry{
	{"echo <cmd> [args]", "Run one command and exit with a status code"},
	{"echo run <file>", "Run a recipe (one command per line); - reads stdin"},
	{"  --pick", "Pick a .echo recipe from the current directory"},
	{"  --last", "Run the most recently created .echo recipe"},
	{"  --continue-on-error", "Run every step instead of stopping at the first failure"},
	{"  --log[=<path>]", "Save a plain transcript (default dir, a file, or --log=. for ./<recipe>.log)"},
	{"  <step> --silent[=lvl]", "Silence a step's output (screen+log); =lvl keeps that level and above"},
	{"echo -C <dir> <cmd>", "Run from outside the project directory"},
}

// buildHelpEntries documents the universal build mode — also outside
// helpSections(), keeping the Registry cross-check clean.
var buildHelpEntries = []helpEntry{
	{"<cmd> --build", "Interactively compose the command (pickers + flags), then run/copy it"},
}

// runHelp shows the command reference. It opens the paginated viewer (one
// section per page, ←/→ to move) in the interactive REPL and for a one-shot
// `echo help` run on a real terminal; inside a recipe, on a non-TTY (piped /
// redirected / CI), or if the pager can't start, it prints the flat listing.
func (sess *session) runHelp() {
	if sess.helpPagerEnabled() {
		err := sess.runHelpPager()
		if err == nil || sess.handleQuit(err) {
			return
		}
	}
	sess.printHelpFlat()
}

// helpPagerEnabled reports whether `help` should open the interactive pager.
// It runs in the live REPL, and also for a one-shot `echo help` when both
// stdin and stdout are real terminals — so running it straight from the
// shell gets the same paginated viewer. Recipe steps and TTY-less runs
// (pipes, redirects, CI) stay on the flat printout.
func (sess *session) helpPagerEnabled() bool {
	if sess.recipe {
		return false
	}
	if sess.interactive {
		return true
	}
	return stdinIsTTY() && stdoutIsTTY()
}

// printHelpFlat is the non-interactive help: every section printed in
// sequence, as `echo help` has always done in script mode.
func (sess *session) printHelpFlat() {
	printSection := func(title string, items []helpEntry) {
		sess.print(Line{Kind: "accent", Text: title})
		for _, line := range renderHelpEntries(sess.styles, items) {
			fmt.Println(line)
		}
	}
	for i, sec := range helpSections() {
		if i > 0 {
			sess.print(Line{Kind: "out", Text: ""})
		}
		printSection(sec.title, sec.items)
	}
	sess.print(Line{Kind: "out", Text: ""})
	printSection("Scripting (one-shot, outside the REPL)", scriptingHelpEntries)
	sess.print(Line{Kind: "out", Text: ""})
	printSection("Build mode (compose interactively)", buildHelpEntries)
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
		// (after picker / --last), so it names the actual modules and the
		// flags the user passed (e.g. --i18n, --level).
		OnResolve: func(resolved []string) { sess.startResolved(name, args, resolved) },
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

// printStyled prints a pre-rendered (ANSI-styled) line for display while
// capturing and logging its ANSI-free `plain` form — so `copy-last` and
// `--log` stay clean even when the display string carries per-segment color
// the standard Kind styling can't express (e.g. the push change tree).
func (sess *session) printStyled(rendered, plain, kind string) {
	if sess.lastOutput != nil {
		sess.lastOutput.Add(Line{Kind: kind, Text: plain})
	}
	if outputSuppressed(levelFromKind(kind)) {
		return
	}
	fmt.Println(rendered)
	teeRunLog(plain)
}
