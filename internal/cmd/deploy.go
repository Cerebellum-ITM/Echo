package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

// DeployOpts configures a `deploy` run.
type DeployOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	// Log emits one Odoo-style progress line (rendered by the REPL through
	// emitOdooLog under `echo.deploy[.sub]`), mirroring i18n-pull's logger.
	Log func(level, sub, msg, db string, fields ...[2]string)
	// StreamOut receives the remote stop/up/odoo lines as they stream.
	StreamOut func(string)
}

// log emits a progress line when a logger is set; a no-op otherwise.
func (o DeployOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

// deployArgs is the parsed shape of the deploy input.
type deployArgs struct {
	from   string
	limit  int
	dryRun bool
	force  bool
	// i18n forces --i18n-overwrite on the run; noI18n suppresses it even
	// when an i18n/ change is detected. Mutually exclusive; absent both,
	// the flag is decided by auto-detection.
	i18n   bool
	noI18n bool
	// commits / modules pre-select the deploy targets non-interactively
	// (skipping the picker): commits are short/full SHAs, modules are addon
	// names (the dirty-module equivalent). Set by the deploy builder so a
	// sequence can show & replay the selection. When both are empty deploy
	// opens its interactive picker as before.
	commits []string
	modules []string
	// auto auto-selects the pending work (commits ahead of upstream, minus
	// already-deployed, plus every dirty module) and skips the picker — the
	// headless counterpart of the default selection. Mutually exclusive with
	// commits/modules.
	auto bool
	// jsonOut emits a machine-readable deploy summary instead of the decorated
	// stream (the caller routes it to stdout, logs to stderr).
	jsonOut bool
	// push syncs the resolved modules to the remote addons dir (Unit 83)
	// right before the stop → up → -u run, so a deploy also ships the code.
	push bool
}

// parseDeployArgs extracts --from/--limit/--dry-run/--force/--i18n/--no-i18n
// plus the non-interactive --commits/--modules selection. Deploy takes no
// positionals — the commits come from the picker or those flags.
func parseDeployArgs(args []string) (deployArgs, error) {
	out := deployArgs{limit: 20}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--i18n":
			out.i18n = true
		case a == "--no-i18n":
			out.noI18n = true
		case a == "--commits":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--commits requires a comma-separated list")
			}
			out.commits = splitCSV(args[i+1])
			i++
		case strings.HasPrefix(a, "--commits="):
			out.commits = splitCSV(strings.TrimPrefix(a, "--commits="))
		case a == "--modules":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--modules requires a comma-separated list")
			}
			out.modules = splitCSV(args[i+1])
			i++
		case strings.HasPrefix(a, "--modules="):
			out.modules = splitCSV(strings.TrimPrefix(a, "--modules="))
		case a == "--from":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--from requires a target name")
			}
			out.from = args[i+1]
			i++
		case strings.HasPrefix(a, "--from="):
			out.from = strings.TrimPrefix(a, "--from=")
		case a == "--limit":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--limit requires a number")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				return out, fmt.Errorf("--limit takes a positive number, got %q", args[i+1])
			}
			out.limit = n
			i++
		case strings.HasPrefix(a, "--limit="):
			v := strings.TrimPrefix(a, "--limit=")
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return out, fmt.Errorf("--limit takes a positive number, got %q", v)
			}
			out.limit = n
		case a == "--auto":
			out.auto = true
		case a == "--push":
			out.push = true
		case a == "--json":
			out.jsonOut = true
		case a == "--dry-run":
			out.dryRun = true
		case a == "--force":
			out.force = true
		case strings.HasPrefix(a, "-"):
			return out, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			return out, fmt.Errorf("%w: deploy takes no positional arguments (commits are picked interactively)", ErrUsage)
		}
	}
	if out.i18n && out.noI18n {
		return out, fmt.Errorf("%w: --i18n and --no-i18n are mutually exclusive", ErrUsage)
	}
	if out.auto && (len(out.commits) > 0 || len(out.modules) > 0) {
		return out, fmt.Errorf("%w: --auto cannot be combined with --commits/--modules", ErrUsage)
	}
	return out, nil
}

// DeployModule is one module in a deploy summary: its resolved name, the
// action taken (`install` / `update`), and whether the remote run for it
// succeeded (false while planned/dry-run or on failure).
type DeployModule struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
}

// DeployResult is the machine-readable summary of a deploy, emitted as JSON
// by the caller under `--json`. Errors/warnings are added by the REPL layer
// from its stream counters (runStats), so they aren't fields here.
type DeployResult struct {
	Target  string         `json:"target"`
	DB      string         `json:"db"`
	Modules []DeployModule `json:"modules"`
	Skipped int            `json:"skipped"`
	// Planned is true for a --dry-run: the plan resolved but nothing ran, so
	// every module's OK stays false.
	Planned bool `json:"planned,omitempty"`
	// JSON echoes whether the caller asked for --json, so the REPL wrapper can
	// route output without re-parsing the args.
	JSON bool `json:"-"`
}

// deployCommit is one local commit offered in the picker.
type deployCommit struct {
	sha     string
	subject string
}

// dirtyModule is one addon with uncommitted working-tree changes, offered
// in the picker alongside the commits. paths are its changed/untracked
// paths (repo-relative), kept for the i18n/ detection.
type dirtyModule struct {
	name  string
	paths []string
}

func (c deployCommit) short() string {
	if len(c.sha) > 7 {
		return c.sha[:7]
	}
	return c.sha
}

// deploySubjectRe captures the module name from the project's commit
// scheme `[Tag] module_name: title`.
var deploySubjectRe = regexp.MustCompile(`^\[[^\]]+\]\s*([A-Za-z0-9_]+)\s*:`)

// RunDeploy deploys selected local commits to a remote Odoo instance: a
// multi-select picker over the repo's recent commits, commit→module
// resolution (subject scheme, then single-module diff fallback; unresolved
// commits are skipped and reported), an install/update split from the
// remote `ir_module_module` state, then — plan shown, prod gated — a
// streamed remote `stop` → `up -d` → one combined `-i`/`-u` Odoo run.
// Pre-condition: the new code is already pulled on the server.
func RunDeploy(ctx context.Context, opts DeployOpts) (DeployResult, error) {
	p, err := parseDeployArgs(opts.Args)
	if err != nil {
		return DeployResult{}, err
	}

	// Validate an explicit --modules list against the local repo before any
	// remote work: a name that isn't an addon here (no __manifest__.py) is a
	// usage error, caught early so we never touch the server for a typo.
	for _, m := range p.modules {
		if !isAddonDir(opts.Root, m) {
			return DeployResult{}, fmt.Errorf("%w: module %q is not an addon in %s (no __manifest__.py)", ErrUsage, m, opts.Root)
		}
	}

	sshHost, remotePath, fromName, err := resolveDeployRemote(opts, p.from)
	if err != nil {
		return DeployResult{}, err
	}
	opts.log("INFO", "remote", "target resolved", "",
		[2]string{"host", sshHost}, [2]string{"path", remotePath})

	// Local deploy history: which commits were already deployed to THIS
	// target from THIS repo, so the picker can mute them. Best-effort.
	projectKey := config.ProjectKey(opts.Root)
	targetKey := config.DeployTargetKey(sshHost, remotePath)
	deployedSet := config.LoadDeployedSHAs(projectKey, targetKey)

	// Working-tree dirty modules, offered in the picker alongside the
	// commits (and used to recover paths for a --modules selection).
	// Best-effort: a status failure just means commits-only.
	dirty, err := gitDirtyModules(ctx, opts.Root)
	if err != nil {
		opts.log("WARNING", "", "dirty detection skipped", "",
			[2]string{"reason", err.Error()})
	}

	var selected []deployCommit
	var selectedDirty []dirtyModule
	switch {
	case p.auto:
		// Headless auto-selection: commits ahead of upstream (minus the ones
		// already deployed to this target) + every dirty module. An empty set
		// is a clean no-op, not an error.
		ahead, aerr := gitAheadCommits(ctx, opts.Root)
		if aerr != nil {
			return DeployResult{}, aerr
		}
		for _, c := range ahead {
			if !deployedSet[c.sha] {
				selected = append(selected, c)
			}
		}
		selectedDirty = dirty
		if len(selected) == 0 && len(selectedDirty) == 0 {
			opts.log("INFO", "", "nothing to deploy", "",
				[2]string{"reason", "no pending commits or dirty modules"})
			return DeployResult{Target: fromName, JSON: p.jsonOut}, nil
		}
	case len(p.commits) > 0 || len(p.modules) > 0:
		// Non-interactive selection (deploy builder / sequence / --last):
		// resolve the SHAs and module names straight from the flags, no picker.
		selected, selectedDirty = deploySelectionFromFlags(ctx, opts, p, dirty)
		if len(selected) == 0 && len(selectedDirty) == 0 {
			return DeployResult{}, fmt.Errorf("%w: no deployable items: --commits/--modules resolved to nothing", ErrUsage)
		}
	default:
		// Commit selection — interactive by design. Without a TTY (script/CI)
		// fail closed with a deploy-specific hint before opening the picker.
		if terr := requireTTY("deploy needs a selection without a TTY: pass --auto or --modules"); terr != nil {
			return DeployResult{}, terr
		}
		commits, cerr := gitRecentCommits(ctx, opts.Root, p.limit)
		if cerr != nil {
			return DeployResult{}, cerr
		}
		if len(commits) == 0 {
			return DeployResult{}, fmt.Errorf("no commits found in %s", opts.Root)
		}
		var markDelta deployMarkDelta
		selected, selectedDirty, markDelta, err = pickDeployItems(commits, dirty, deployedSet, true, opts.Palette)
		if err != nil {
			return DeployResult{}, err
		}
		// Persist the operator's manual ctrl+d / ctrl+a marks the moment
		// the picker is confirmed — before any remote work or prod gate, so
		// the edit survives even if the deploy is later declined or fails.
		// Best-effort, mirroring the end-of-run auto-mark.
		if !markDelta.isEmpty() {
			if err := config.UpdateDeployedMarks(projectKey, targetKey, markDelta.added, markDelta.removed); err == nil {
				opts.log("INFO", "history", "updated deploy marks", "",
					[2]string{"marked", strconv.Itoa(len(markDelta.added))},
					[2]string{"unmarked", strconv.Itoa(len(markDelta.removed))})
			}
		}
	}
	opts.log("INFO", "", "items selected", "",
		[2]string{"commits", strconv.Itoa(len(selected))},
		[2]string{"dirty", strconv.Itoa(len(selectedDirty))})

	// Commit → module resolution. Unresolved commits are excluded and
	// reported, never fatal — unless nothing at all resolves. Each resolved
	// commit's diff is also scanned for changes under <module>/i18n/, which
	// later decides whether the `-u` run carries --i18n-overwrite.
	seen := map[string]bool{}
	i18nTouched := map[string]bool{}
	var modules []string
	var deployedShas []string // selected commits that resolved → recorded on success
	var skipped int

	// Selected dirty modules resolve straight to their name (via=dirty) and
	// feed i18n detection from their working-tree paths. Their code is not
	// committed/pushed, so warn once: deploy updates them on the server but
	// doesn't put the code there — that's the user's other tool's job.
	if len(selectedDirty) > 0 {
		var names []string
		for _, dm := range selectedDirty {
			names = append(names, dm.name)
			opts.log("INFO", "", "resolved", "",
				[2]string{"module", dm.name}, [2]string{"via", "dirty"})
			if !seen[dm.name] {
				seen[dm.name] = true
				modules = append(modules, dm.name)
			}
			if !i18nTouched[dm.name] && pathsTouchI18n(dm.name, dm.paths) {
				i18nTouched[dm.name] = true
				opts.log("INFO", "i18n", "i18n changes detected", "",
					[2]string{"module", dm.name})
			}
		}
		opts.log("WARNING", "", "selected modules have uncommitted changes — deploy updates them on the server but does not push the code", "",
			[2]string{"modules", strings.Join(names, ",")})
	}

	for _, c := range selected {
		mod, via, reason, paths := resolveCommitModule(ctx, opts.Root, c)
		if mod == "" {
			opts.log("WARNING", "", "skipped", "",
				[2]string{"commit", c.short()}, [2]string{"reason", reason})
			skipped++
			continue
		}
		opts.log("INFO", "", "resolved", "",
			[2]string{"commit", c.short()}, [2]string{"module", mod}, [2]string{"via", via})
		deployedShas = append(deployedShas, c.sha)
		if !seen[mod] {
			seen[mod] = true
			modules = append(modules, mod)
		}
		// Subject-resolved commits skip the diff during resolution; fetch
		// it now so i18n detection covers them too. A diff failure here
		// degrades to "no i18n change" — it must never drop the module.
		if paths == nil {
			p2, err := gitCommitPaths(ctx, opts.Root, c.sha)
			if err != nil {
				opts.log("WARNING", "", "i18n detection skipped", "",
					[2]string{"commit", c.short()}, [2]string{"reason", err.Error()})
			} else {
				paths = p2
			}
		}
		if !i18nTouched[mod] && pathsTouchI18n(mod, paths) {
			i18nTouched[mod] = true
			opts.log("INFO", "i18n", "i18n changes detected", "",
				[2]string{"commit", c.short()}, [2]string{"module", mod})
		}
	}
	if len(modules) == 0 {
		return DeployResult{}, fmt.Errorf("no deployable modules: every selected commit was skipped")
	}
	sort.Strings(modules)

	// Remote profile + DB credentials, same as i18n-pull.
	cfgRemote := *opts.Cfg
	cfgRemote.ConnectSSHHost = sshHost
	cfgRemote.ConnectRemotePath = remotePath
	opts.log("INFO", "remote", "reading remote profile", "", [2]string{"host", sshHost})
	prof, err := fetchRemoteProfile(ctx, ConnectOpts{Cfg: &cfgRemote, Root: opts.Root})
	if err != nil {
		return DeployResult{}, err
	}
	target := connectTarget{
		remote:        true,
		composeCmd:    prof.ComposeCmd,
		odooContainer: prof.OdooContainer,
		dbContainer:   prof.DBContainer,
		dbName:        prof.DBName,
		stage:         prof.Stage,
		odooVersion:   prof.OdooVersion,
	}
	opts.log("INFO", "system", "system", prof.DBName,
		statusFields(target.odooVersion, prof.Stage,
			statusProjectName(opts.Cfg, true, remotePath, fromName),
			prof.DBName)...)
	conn := odoo.Conn{DB: target.dbName, Host: target.dbContainer}
	pg := remotePullEnv(ctx, sshHost, remotePath)
	conn.Port = pg["POSTGRES_PORT"]
	conn.User = pg["POSTGRES_USER"]
	conn.Password = pg["POSTGRES_PASSWORD"]

	// Install vs update, decided by the remote instance's module states.
	opts.log("INFO", "remote", "querying installed modules", prof.DBName)
	states, err := remoteModuleStates(ctx, sshHost, remotePath, target, conn.User, target.dbName)
	if err != nil {
		return DeployResult{}, fmt.Errorf("query remote module states: %w", err)
	}
	install, update := splitInstallUpdate(modules, states)

	// Machine-readable summary — update-set first, then install-set (mirrors
	// the run fields). OK flips to true only after a successful remote run.
	result := DeployResult{
		Target:  fromName,
		DB:      prof.DBName,
		Skipped: skipped,
		Planned: p.dryRun,
		JSON:    p.jsonOut,
	}
	for _, m := range update {
		result.Modules = append(result.Modules, DeployModule{Name: m, Action: "update"})
	}
	for _, m := range install {
		result.Modules = append(result.Modules, DeployModule{Name: m, Action: "install"})
	}

	// --i18n-overwrite decision. Only update-set modules count: a fresh
	// install loads translations anyway. The flag is global to the one
	// Odoo run, so any update-set hit overwrites every updated module's
	// terms. --i18n forces it, --no-i18n suppresses a positive detection.
	for _, m := range install {
		if i18nTouched[m] {
			opts.log("INFO", "i18n", "i18n changes on install-set module — no overwrite needed",
				prof.DBName, [2]string{"module", m})
		}
	}
	detectedUpdate := false
	for _, m := range update {
		if i18nTouched[m] {
			detectedUpdate = true
			break
		}
	}
	i18nState, overwrite := i18nOverwriteDecision(p.i18n, p.noI18n, detectedUpdate)

	// The plan rides its own `echo.deploy.plan` logger so it renders in a
	// distinct color from the other deploy lines — it's the line the
	// operator reviews before the prod gate.
	opts.log("INFO", "plan", "modules resolved", prof.DBName,
		[2]string{"update", strings.Join(update, ",")},
		[2]string{"install", strings.Join(install, ",")},
		[2]string{"i18n", i18nState},
		[2]string{"skipped", strconv.Itoa(skipped)})

	// --push shares the deploy's already-resolved target: sync the resolved
	// modules' local code to the remote addons dir before the run. In dry-run
	// it prints the rsync itemization; on a real run a push failure aborts
	// before anything restarts.
	runPush := func(dryRun bool) error {
		if !p.push {
			return nil
		}
		if err := requireRsync(); err != nil {
			return err
		}
		pushMods := append(append([]string(nil), update...), install...)
		pushRSC := remoteShellContext{
			sshHost: sshHost, remotePath: remotePath, fromName: fromName,
			target: target, prof: prof, conn: conn,
		}
		pushOpts := PushOpts{
			Cfg: opts.Cfg, Root: opts.Root, Palette: opts.Palette,
			Log: opts.Log, StreamOut: opts.StreamOut,
		}
		opts.log("INFO", "push", "syncing modules to remote", prof.DBName,
			[2]string{"modules", strings.Join(pushMods, ",")})
		_, perr := pushModuleSet(ctx, pushRSC, pushOpts, pushMods, opts.Root, dryRun, false)
		return perr
	}

	if p.dryRun {
		if err := runPush(true); err != nil {
			return DeployResult{}, err
		}
		opts.log("INFO", "", "dry-run — nothing executed", prof.DBName)
		return result, nil
	}
	if strings.EqualFold(target.stage, "prod") && !p.force {
		if err := confirmProd(opts.Palette, "deploy", target.dbName); err != nil {
			return DeployResult{}, err
		}
	}
	if err := runPush(false); err != nil {
		return DeployResult{}, fmt.Errorf("push failed: %w", err)
	}

	// The three remote steps, each streamed live. Fail-fast with the step
	// named in the error.
	step := func(name, remoteCmd string) error {
		opts.log("INFO", "compose", name, prof.DBName)
		if err := runSSHStream(ctx, sshHost, remoteCmd, nil, opts.StreamOut); err != nil {
			return fmt.Errorf("%s failed: %w", name, err)
		}
		return nil
	}
	if err := step("stop", remoteComposeCmd(remotePath, target.composeCmd, "stop")); err != nil {
		return DeployResult{}, err
	}
	if err := step("up -d", remoteComposeCmd(remotePath, target.composeCmd, "up", "-d")); err != nil {
		return DeployResult{}, err
	}
	// Name the modules and the effective i18n flag right at the Odoo run, so
	// the execution line mirrors `update`'s start line.
	runFields := []([2]string){}
	if len(update) > 0 {
		runFields = append(runFields, [2]string{"update", strings.Join(update, ",")})
	}
	if len(install) > 0 {
		runFields = append(runFields, [2]string{"install", strings.Join(install, ",")})
	}
	if overwrite {
		runFields = append(runFields, [2]string{"flags", "--i18n-overwrite"})
	}
	opts.log("INFO", "odoo", "running module install/update", prof.DBName, runFields...)
	argv := odoo.WithI18nOverwrite(odoo.InstallUpdate(conn, install, update), overwrite)
	if err := runSSHStream(ctx, sshHost, remoteContainerCmd(remotePath, target, argv), nil, opts.StreamOut); err != nil {
		return DeployResult{}, fmt.Errorf("odoo run failed: %w", err)
	}
	// The remote run landed: every resolved module deployed OK.
	for i := range result.Modules {
		result.Modules[i].OK = true
	}

	// The run succeeded: remember the deployed commits for this target so a
	// later picker mutes them. Best-effort — a write failure never fails the
	// deploy that already landed.
	if err := config.MarkDeployed(projectKey, targetKey, deployedShas); err == nil {
		opts.log("INFO", "history", "recorded deployed commits", prof.DBName,
			[2]string{"n", strconv.Itoa(len(deployedShas))})
	}

	opts.log("INFO", "", "deploy complete", prof.DBName,
		[2]string{"update", strconv.Itoa(len(update))},
		[2]string{"install", strconv.Itoa(len(install))},
		[2]string{"skipped", strconv.Itoa(skipped)})
	return result, nil
}

// splitCSV splits a comma-separated flag value into trimmed, non-empty
// tokens.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// deploySelectionFromFlags resolves the non-interactive --commits/--modules
// selection. Commits are resolved by SHA (short or full) to recover their
// full hash and subject; an unresolvable SHA is warned and skipped. Module
// names map to their current dirty entry (for i18n path detection) when
// still dirty, else to a bare entry by name — so a `--last` replay survives
// the module having since been committed.
func deploySelectionFromFlags(ctx context.Context, opts DeployOpts, p deployArgs, dirty []dirtyModule) ([]deployCommit, []dirtyModule) {
	var selected []deployCommit
	for _, sha := range p.commits {
		c, err := resolveDeployCommit(ctx, opts.Root, sha)
		if err != nil {
			opts.log("WARNING", "", "commit not found, skipped", "",
				[2]string{"commit", sha}, [2]string{"reason", err.Error()})
			continue
		}
		selected = append(selected, c)
	}
	byName := make(map[string]dirtyModule, len(dirty))
	for _, dm := range dirty {
		byName[dm.name] = dm
	}
	var selectedDirty []dirtyModule
	for _, name := range p.modules {
		if dm, ok := byName[name]; ok {
			selectedDirty = append(selectedDirty, dm)
		} else {
			selectedDirty = append(selectedDirty, dirtyModule{name: name})
		}
	}
	return selected, selectedDirty
}

// resolveDeployCommit resolves a short or full SHA to a deployCommit with
// its full hash and subject, so commit→module resolution still works.
func resolveDeployCommit(ctx context.Context, root, sha string) (deployCommit, error) {
	out, err := gitOutput(ctx, root, "show", "-s", "--format=%H%x1f%s", sha)
	if err != nil {
		return deployCommit{}, err
	}
	full, subject, ok := strings.Cut(strings.TrimSpace(string(out)), "\x1f")
	if !ok || full == "" {
		return deployCommit{}, fmt.Errorf("could not resolve commit %q", sha)
	}
	return deployCommit{sha: full, subject: subject}, nil
}

// resolveDeployRemote mirrors i18n-pull's target resolution: --from →
// project [connect] → global targets fallback (one auto, several picker).
func resolveDeployRemote(opts DeployOpts, from string) (sshHost, remotePath, fromName string, err error) {
	return resolveRemoteTarget(opts.Cfg, opts.Palette, from, opts.Log)
}

// resolveRemoteTarget is the shared remote resolution used by deploy,
// shell and shell-run: an explicit --from name, else the project/link
// [connect], else the global targets fallback (one auto-used, several
// open a TTY-guarded picker, none → ErrNoPullRemote).
func resolveRemoteTarget(cfg *config.Config, palette theme.Palette, from string, log func(level, sub, msg, db string, fields ...[2]string)) (sshHost, remotePath, fromName string, err error) {
	sshHost, remotePath, err = resolvePullRemote(cfg, from)
	if errors.Is(err, ErrNoPullRemote) && from == "" {
		t, perr := pickConnectTarget(cfg.ConnectTargets, palette,
			"Select connect target", log)
		if perr != nil {
			if errors.Is(perr, ErrNoConnectTargets) {
				return "", "", "", ErrNoPullRemote
			}
			return "", "", "", perr
		}
		return t.SSHHost, t.RemotePath, t.Name, nil
	}
	return sshHost, remotePath, from, err
}

// dirtyLabel is the picker label for a dirty module — distinct from a
// commit label (`<sha7>  <subject>`) so the two never collide and read
// differently on screen.
func dirtyLabel(dm dirtyModule) string {
	return "~ " + dm.name + "  ·  uncommitted (" + strconv.Itoa(len(dm.paths)) + " files)"
}

// deployMarkDelta is the net change to a target's deployed-SHA history made
// by the operator's manual ctrl+d / ctrl+a toggles in the picker: SHAs to
// add (newly marked) and SHAs to remove (a previously-deployed row un-muted).
type deployMarkDelta struct {
	added   []string
	removed []string
}

func (d deployMarkDelta) isEmpty() bool { return len(d.added) == 0 && len(d.removed) == 0 }

// pickDeployItems opens the multi-select picker over the dirty modules
// (listed first — your current uncommitted work) and the recent commits,
// then splits the chosen labels back into commits and dirty modules.
// Commits whose full SHA is in deployedSet are passed as "deployed" labels
// so the picker mutes them. When allowMark is true the commit rows become
// markable (ctrl+d / ctrl+a toggle their deployed mark), and the returned
// deployMarkDelta carries the net change against deployedSet for the caller
// to persist; build mode passes false (no target to write to). An empty
// selection or a cancel maps to ErrCancelled, matching the rest of deploy.
func pickDeployItems(commits []deployCommit, dirty []dirtyModule, deployedSet map[string]bool, allowMark bool, palette theme.Palette) ([]deployCommit, []dirtyModule, deployMarkDelta, error) {
	var labels []string
	byCommit := make(map[string]deployCommit, len(commits))
	byDirty := make(map[string]dirtyModule, len(dirty))
	var deployedLabels, markableLabels []string

	for _, dm := range dirty {
		lbl := dirtyLabel(dm)
		labels = append(labels, lbl)
		byDirty[lbl] = dm
	}
	for _, c := range commits {
		lbl := c.short() + "  " + c.subject
		labels = append(labels, lbl)
		byCommit[lbl] = c
		if deployedSet[c.sha] {
			deployedLabels = append(deployedLabels, lbl)
		}
		if allowMark {
			markableLabels = append(markableLabels, lbl)
		}
	}

	picked, deployedFinal, canceled, err := runFuzzyPickerCore(
		"Select commits / dirty modules to deploy", labels, nil, deployedLabels, markableLabels, palette, "")
	if err != nil {
		return nil, nil, deployMarkDelta{}, err
	}
	if canceled || len(picked) == 0 {
		return nil, nil, deployMarkDelta{}, ErrCancelled
	}
	var pickedCommits []deployCommit
	var pickedDirty []dirtyModule
	for _, lbl := range picked {
		if c, ok := byCommit[lbl]; ok {
			pickedCommits = append(pickedCommits, c)
			continue
		}
		if dm, ok := byDirty[lbl]; ok {
			pickedDirty = append(pickedDirty, dm)
		}
	}

	// Diff the picker's final deployed marks against the incoming set to
	// recover the manual edit. Removals are scoped to the SHAs actually
	// shown (byCommit): a deployed SHA outside this commit window isn't
	// represented in the picker and must not be treated as un-marked.
	var delta deployMarkDelta
	if allowMark {
		finalSHAs := make(map[string]bool, len(deployedFinal))
		for _, lbl := range deployedFinal {
			if c, ok := byCommit[lbl]; ok {
				finalSHAs[c.sha] = true
				if !deployedSet[c.sha] {
					delta.added = append(delta.added, c.sha)
				}
			}
		}
		for _, c := range commits {
			if deployedSet[c.sha] && !finalSHAs[c.sha] {
				delta.removed = append(delta.removed, c.sha)
			}
		}
	}
	return pickedCommits, pickedDirty, delta, nil
}

// resolveCommitModule maps one commit to its module: the subject scheme
// first (`[Tag] module: title`, valid only when the module exists as an
// addon in the repo), then the diff fallback (the commit's changed paths
// must touch exactly one addon). Returns ("", "", reason, …) when
// unresolved. The returned paths are the commit's changed paths when the
// diff was read (the fallback), and nil for subject-resolved commits — the
// caller fetches those separately for i18n detection.
func resolveCommitModule(ctx context.Context, root string, c deployCommit) (module, via, reason string, paths []string) {
	if m := moduleFromSubject(root, c.subject); m != "" {
		return m, "subject", "", nil
	}
	paths, err := gitCommitPaths(ctx, root, c.sha)
	if err != nil {
		return "", "", "git show failed: " + err.Error(), nil
	}
	mods := modulesFromPaths(root, paths)
	switch len(mods) {
	case 1:
		return mods[0], "diff", "", paths
	case 0:
		return "", "", "no addon module touched", paths
	default:
		return "", "", "touches several modules: " + strings.Join(mods, ", "), paths
	}
}

// i18nOverwriteDecision resolves whether the deploy's update run carries
// --i18n-overwrite and the state label shown in the plan. --i18n forces it
// on, --no-i18n suppresses a positive detection; otherwise it follows
// whether an update-set module changed its i18n/ folder.
func i18nOverwriteDecision(forceI18n, noI18n, detectedUpdate bool) (state string, overwrite bool) {
	switch {
	case forceI18n:
		return "forced", true
	case noI18n && detectedUpdate:
		return "suppressed", false
	case detectedUpdate:
		return "on", true
	default:
		return "off", false
	}
}

// pathsTouchI18n reports whether any changed path lives under the module's
// i18n/ folder (any file: .po, .pot, or otherwise) — the signal that a
// deploy of this module should overwrite the database translations.
func pathsTouchI18n(module string, paths []string) bool {
	prefix := module + "/i18n/"
	for _, p := range paths {
		if strings.HasPrefix(filepath.ToSlash(p), prefix) {
			return true
		}
	}
	return false
}

// moduleFromSubject extracts the module from the commit-subject scheme,
// valid only when it names a real addon in the repo (commits scoped to
// non-addon areas like `[FIX] docs: …` fall through to the diff).
func moduleFromSubject(root, subject string) string {
	m := deploySubjectRe.FindStringSubmatch(subject)
	if m == nil || !isAddonDir(root, m[1]) {
		return ""
	}
	return m[1]
}

// modulesFromPaths maps changed paths to the distinct top-level addon
// directories they live in, sorted.
func modulesFromPaths(root string, paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		top := strings.SplitN(filepath.ToSlash(p), "/", 2)[0]
		if top == "" || seen[top] || !isAddonDir(root, top) {
			continue
		}
		seen[top] = true
		out = append(out, top)
	}
	sort.Strings(out)
	return out
}

// isAddonDir reports whether <root>/<name> is an Odoo addon (has a
// __manifest__.py).
func isAddonDir(root, name string) bool {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return false
	}
	_, err := os.Stat(filepath.Join(root, name, "__manifest__.py"))
	return err == nil
}

// splitInstallUpdate partitions the modules by their remote state: present
// as installed / to upgrade → update; anything else (absent, uninstalled,
// uninstallable) → install. Inputs are sorted, so outputs stay sorted.
func splitInstallUpdate(modules []string, states map[string]string) (install, update []string) {
	for _, m := range modules {
		switch states[m] {
		case "installed", "to upgrade":
			update = append(update, m)
		default:
			install = append(install, m)
		}
	}
	return install, update
}

// remoteModuleStates queries every module's state from the remote
// database (`ir_module_module`), over SSH inside the remote Postgres
// container. Read-only.
func remoteModuleStates(ctx context.Context, sshHost, remotePath string, t connectTarget, pgUser, db string) (map[string]string, error) {
	if pgUser == "" {
		pgUser = "odoo"
	}
	q := "SELECT name, state FROM ir_module_module"
	argv := odoo.Cmd{"psql", "-U", pgUser, "-d", db, "-At", "-c", q}
	out, err := runSSH(ctx, sshHost, remoteDBCmd(remotePath, t, argv), nil)
	if err != nil {
		return nil, err
	}
	states := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, state, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok || name == "" {
			continue
		}
		states[name] = state
	}
	return states, nil
}

// gitRecentCommits lists the last n commits of the repo's current branch,
// newest first.
func gitRecentCommits(ctx context.Context, root string, n int) ([]deployCommit, error) {
	out, err := gitOutput(ctx, root, "log", "-n", strconv.Itoa(n), "--pretty=format:%H%x1f%s")
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	var commits []deployCommit
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		sha, subject, ok := strings.Cut(line, "\x1f")
		if !ok || sha == "" {
			continue
		}
		commits = append(commits, deployCommit{sha: sha, subject: subject})
	}
	return commits, nil
}

// gitAheadCommits lists the commits on the current branch that are ahead of
// its upstream (`@{upstream}..HEAD`), newest first — the "not yet pushed"
// set --auto deploys. Returns a nil slice with no error when the branch has
// no upstream configured (a common case for a fresh feature branch): --auto
// then falls back to dirty modules only.
func gitAheadCommits(ctx context.Context, root string) ([]deployCommit, error) {
	out, err := gitOutput(ctx, root, "log", "@{upstream}..HEAD", "--pretty=format:%H%x1f%s")
	if err != nil {
		// No upstream (or a detached HEAD) is not fatal: treat it as "nothing
		// ahead" so --auto still deploys the dirty modules.
		if strings.Contains(err.Error(), "no upstream") ||
			strings.Contains(err.Error(), "unknown revision") ||
			strings.Contains(err.Error(), "@{upstream}") {
			return nil, nil
		}
		return nil, fmt.Errorf("git log ahead: %w", err)
	}
	var commits []deployCommit
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		sha, subject, ok := strings.Cut(line, "\x1f")
		if !ok || sha == "" {
			continue
		}
		commits = append(commits, deployCommit{sha: sha, subject: subject})
	}
	return commits, nil
}

// gitDirtyModules returns the addon modules with uncommitted working-tree
// changes (modified, staged, or untracked), each with its changed paths.
// Best-effort: a clean tree yields nil; the caller treats an error as "no
// dirty modules" so the picker still shows commits.
func gitDirtyModules(ctx context.Context, root string) ([]dirtyModule, error) {
	out, err := gitOutput(ctx, root, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	return dirtyModulesFromPaths(root, parsePorcelainPaths(string(out))), nil
}

// parsePorcelainPaths extracts the changed paths from `git status
// --porcelain` output: each line is `XY <path>` (two status chars + space),
// renames are `XY old -> new` (the new path wins), and paths with odd
// characters are quoted (the quotes are stripped).
func parsePorcelainPaths(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		p := line[3:]
		if i := strings.Index(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// dirtyModulesFromPaths groups changed paths by their top-level addon dir
// (skipping non-addon paths), sorted by module name, preserving each
// module's paths. Pure — the testable core of gitDirtyModules.
func dirtyModulesFromPaths(root string, paths []string) []dirtyModule {
	byMod := map[string][]string{}
	var order []string
	for _, p := range paths {
		top := strings.SplitN(filepath.ToSlash(p), "/", 2)[0]
		if top == "" || !isAddonDir(root, top) {
			continue
		}
		if _, ok := byMod[top]; !ok {
			order = append(order, top)
		}
		byMod[top] = append(byMod[top], p)
	}
	sort.Strings(order)
	out := make([]dirtyModule, 0, len(order))
	for _, m := range order {
		out = append(out, dirtyModule{name: m, paths: byMod[m]})
	}
	return out
}

// gitCommitPaths lists the paths changed by one commit (repo-relative).
// Merge commits yield no paths under diff-tree's defaults, which makes
// them unresolved — the right outcome for a deploy picker.
func gitCommitPaths(ctx context.Context, root, sha string) ([]string, error) {
	out, err := gitOutput(ctx, root, "diff-tree", "--no-commit-id", "--name-only", "-r", sha)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

// gitOutput runs one git command against the repo at root, returning
// stdout; stderr is folded into the error (mirrors runSSH).
func gitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}
