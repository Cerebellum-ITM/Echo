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
	"time"

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
	// OnSync, when set, receives each --push module's file changes so the
	// caller can render the change tree (same as the standalone `push`).
	OnSync func(changes []FileChange)
	// PushSrcRoot overrides the local directory --push reads module files
	// from (default: Root). `watch` sets it to its git-archive scratch dir so
	// the deploy pushes the committed content at the target ref — and so the
	// push and its pre_push/post_push actions run in order inside the deploy
	// pipeline instead of being done separately by the watcher.
	PushSrcRoot string
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
	// checkpoint / noCheckpoint / checkpointSet control the DB checkpoint
	// (Unit 89). checkpointSet is true when --checkpoint was passed (turning
	// it on regardless of stage); checkpoint holds the requested method
	// ("db"/"dump"/"" = configured default). noCheckpoint forces it off.
	// The two flags are mutually exclusive.
	checkpoint    string
	checkpointSet bool
	noCheckpoint  bool
	// rollback restores the target's most recent checkpoint instead of
	// deploying (deploy --rollback). Mutually exclusive with any selection.
	rollback bool
	// noActions skips all declared deploy actions (Unit 92) for this run —
	// the escape hatch when a server-declared action is broken.
	noActions bool
	// noPush forces the code push off even when `[deploy] push` makes it the
	// configured default (Unit 95). Mutually exclusive with --push.
	noPush bool
	// setPush, when non-nil, is a config-only request to persist `[deploy]
	// push` locally and exit (deploy --set-push[=true|false]).
	setPush *bool
	// test / noTest run (or skip) the deployed modules' unit tests this run
	// (Unit 100), overriding the persisted `[deploy] test` default. Mutually
	// exclusive.
	test   bool
	noTest bool
	// The following are config-only test management flags (each persists and
	// exits like --set-push, mutually exclusive with a deploy selection):
	//   testToggle       — flip [deploy] test on/off, print the result.
	//   testModulesSet   — replace the pinned [deploy] test_modules list (nil =
	//                      untouched; non-nil incl. empty = set to that list).
	//   testPick         — bare --test-modules: pick the list via a picker.
	//   testAdd / testRm — add/remove modules from the pinned list.
	//   testClear        — empty the pinned list (back to auto).
	testToggle     bool
	testModulesSet *[]string
	testPick       bool
	testAdd        []string
	testRm         []string
	testClear      bool
}

// isTestManage reports whether the args carry a config-only test-management
// operation (toggle / pin-list edit) that runs standalone and exits.
func (p deployArgs) isTestManage() bool {
	return p.testToggle || p.testPick || p.testClear ||
		p.testModulesSet != nil || len(p.testAdd) > 0 || len(p.testRm) > 0
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
		case a == "--no-push":
			out.noPush = true
		case a == "--set-push":
			t := true
			out.setPush = &t
		case strings.HasPrefix(a, "--set-push="):
			v := strings.TrimPrefix(a, "--set-push=")
			b, berr := strconv.ParseBool(v)
			if berr != nil {
				return out, fmt.Errorf("%w: --set-push takes true or false, got %q", ErrUsage, v)
			}
			out.setPush = &b
		case a == "--checkpoint":
			out.checkpointSet = true
		case strings.HasPrefix(a, "--checkpoint="):
			v := strings.TrimPrefix(a, "--checkpoint=")
			if v != "db" && v != "dump" {
				return out, fmt.Errorf("%w: --checkpoint takes db or dump, got %q", ErrUsage, v)
			}
			out.checkpointSet = true
			out.checkpoint = v
		case a == "--no-checkpoint":
			out.noCheckpoint = true
		case a == "--rollback":
			out.rollback = true
		case a == "--no-actions":
			out.noActions = true
		case a == "--test":
			out.test = true
		case a == "--no-test":
			out.noTest = true
		case a == "--test-toggle":
			out.testToggle = true
		case a == "--test-clear":
			out.testClear = true
		case a == "--test-modules":
			// Bare → picker; `=csv` → set the whole list.
			out.testPick = true
		case strings.HasPrefix(a, "--test-modules="):
			mods := splitCSV(strings.TrimPrefix(a, "--test-modules="))
			out.testModulesSet = &mods
		case a == "--test-add":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--test-add requires a comma-separated list")
			}
			out.testAdd = splitCSV(args[i+1])
			i++
		case strings.HasPrefix(a, "--test-add="):
			out.testAdd = splitCSV(strings.TrimPrefix(a, "--test-add="))
		case a == "--test-rm":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--test-rm requires a comma-separated list")
			}
			out.testRm = splitCSV(args[i+1])
			i++
		case strings.HasPrefix(a, "--test-rm="):
			out.testRm = splitCSV(strings.TrimPrefix(a, "--test-rm="))
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
	if out.checkpointSet && out.noCheckpoint {
		return out, fmt.Errorf("%w: --checkpoint and --no-checkpoint are mutually exclusive", ErrUsage)
	}
	if out.push && out.noPush {
		return out, fmt.Errorf("%w: --push and --no-push are mutually exclusive", ErrUsage)
	}
	if out.rollback && (out.auto || len(out.commits) > 0 || len(out.modules) > 0 || out.push) {
		return out, fmt.Errorf("%w: --rollback cannot be combined with --commits/--modules/--auto/--push", ErrUsage)
	}
	if out.test && out.noTest {
		return out, fmt.Errorf("%w: --test and --no-test are mutually exclusive", ErrUsage)
	}
	if out.testPick && out.testModulesSet != nil {
		return out, fmt.Errorf("%w: --test-modules picker and --test-modules=<list> are mutually exclusive", ErrUsage)
	}
	// The config-only test-management ops run standalone (persist + exit); they
	// can't ride along with an actual deploy selection or another such op.
	if out.isTestManage() {
		if out.test || out.noTest || out.auto || out.rollback || out.push ||
			len(out.commits) > 0 || len(out.modules) > 0 {
			return out, fmt.Errorf("%w: test-management flags run on their own (no deploy selection)", ErrUsage)
		}
		n := 0
		for _, on := range []bool{out.testToggle, out.testClear, out.testPick,
			out.testModulesSet != nil, len(out.testAdd) > 0, len(out.testRm) > 0} {
			if on {
				n++
			}
		}
		if n > 1 {
			return out, fmt.Errorf("%w: run one test-management flag at a time", ErrUsage)
		}
	}
	return out, nil
}

// resolveDeployPush computes whether this deploy ships code: an explicit
// --push/--no-push wins; otherwise the server [deploy] push, then the local
// one, then false (Unit 95).
func resolveDeployPush(p deployArgs, prof config.RemoteProfile, cfg *config.Config) bool {
	switch {
	case p.noPush:
		return false
	case p.push:
		return true
	case prof.DeployPush != nil:
		return *prof.DeployPush
	case cfg != nil && cfg.DeployPush != nil:
		return *cfg.DeployPush
	}
	return false
}

// resolveDeployTest computes whether this deploy runs the modules' tests,
// mirroring resolveDeployPush: an explicit --test/--no-test wins; otherwise the
// server [deploy] test, then the local one, then false (Unit 100).
func resolveDeployTest(p deployArgs, prof config.RemoteProfile, cfg *config.Config) bool {
	switch {
	case p.noTest:
		return false
	case p.test:
		return true
	case prof.DeployTest != nil:
		return *prof.DeployTest
	case cfg != nil && cfg.DeployTest != nil:
		return *cfg.DeployTest
	}
	return false
}

// resolveTestModules picks which modules get tested: the pinned
// `[deploy] test_modules` list (server-first) when non-empty, otherwise the
// deploy's own resolved module set (`deployed`).
func resolveTestModules(prof config.RemoteProfile, cfg *config.Config, deployed []string) []string {
	if len(prof.DeployTestModules) > 0 {
		return prof.DeployTestModules
	}
	if cfg != nil && len(cfg.DeployTestModules) > 0 {
		return cfg.DeployTestModules
	}
	return deployed
}

// runDeployTestManage applies a config-only test-management op (toggle the
// [deploy] test default, or edit the pinned test_modules list), persists it to
// the local project profile, and logs the resulting state. Exactly one op is
// set (parse enforces it).
func runDeployTestManage(opts DeployOpts, p deployArgs) error {
	cfgCopy := *opts.Cfg
	switch {
	case p.testToggle:
		cur := cfgCopy.DeployTest != nil && *cfgCopy.DeployTest
		nv := !cur
		cfgCopy.DeployTest = &nv
	case p.testClear:
		cfgCopy.DeployTestModules = nil
	case p.testModulesSet != nil:
		cfgCopy.DeployTestModules = mergeTestModules(nil, *p.testModulesSet)
	case len(p.testAdd) > 0:
		cfgCopy.DeployTestModules = mergeTestModules(cfgCopy.DeployTestModules, p.testAdd)
	case len(p.testRm) > 0:
		cfgCopy.DeployTestModules = dropTestModules(cfgCopy.DeployTestModules, p.testRm)
	case p.testPick:
		mods, err := pickTestModules(opts, cfgCopy.DeployTestModules)
		if err != nil {
			return err
		}
		cfgCopy.DeployTestModules = mods
	}
	if err := config.SaveProject(&cfgCopy); err != nil {
		return fmt.Errorf("save deploy test config: %w", err)
	}
	opts.Cfg.DeployTest = cfgCopy.DeployTest
	opts.Cfg.DeployTestModules = cfgCopy.DeployTestModules
	opts.log("INFO", "", "deploy test config", opts.Cfg.DBName,
		[2]string{"test", boolOnOff(cfgCopy.DeployTest)},
		[2]string{"test_modules", testModulesLabel(cfgCopy.DeployTestModules)})
	return nil
}

// pickTestModules opens a multi-select over the project's modules with the
// current pinned list pre-checked, so one picker both adds and removes. An
// empty confirmed selection clears the list (back to auto).
func pickTestModules(opts DeployOpts, current []string) ([]string, error) {
	available := mergeTestModules(listAvailableModules(opts.Cfg, opts.Root), current)
	if len(available) == 0 {
		return nil, fmt.Errorf("%w: no modules found to pin — set them headlessly with --test-modules=<list>", ErrUsage)
	}
	picked, canceled, err := runFuzzyPickerWithSelected("Modules to test on deploy", available, current, opts.Palette)
	if err != nil {
		return nil, err
	}
	if canceled {
		return nil, ErrCancelled
	}
	return picked, nil
}

// mergeTestModules returns base followed by the new entries, de-duplicated,
// order-preserving, dropping blanks. Used for both "set" (base nil) and "add".
func mergeTestModules(base, add []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(base)+len(add))
	for _, m := range append(append([]string{}, base...), add...) {
		if m = strings.TrimSpace(m); m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// dropTestModules removes the named modules from the list, order-preserving.
func dropTestModules(cur, rm []string) []string {
	drop := map[string]bool{}
	for _, m := range rm {
		drop[strings.TrimSpace(m)] = true
	}
	out := make([]string, 0, len(cur))
	for _, m := range cur {
		if !drop[m] {
			out = append(out, m)
		}
	}
	return out
}

// boolOnOff renders a *bool test default as on/off (nil = off).
func boolOnOff(b *bool) string {
	if b != nil && *b {
		return "on"
	}
	return "off"
}

// testModulesLabel renders the pinned list for a log line; empty = "(auto)".
func testModulesLabel(mods []string) string {
	if len(mods) == 0 {
		return "(auto)"
	}
	return strings.Join(mods, ",")
}

// stageWantsCheckpoint reports whether a stage is checkpoint-worthy under the
// "auto" mode: staging and prod are, dev is not.
func stageWantsCheckpoint(stage string) bool {
	s := strings.ToLower(strings.TrimSpace(stage))
	return s == "staging" || s == "prod"
}

// checkpointPolicy is the resolved mode/method/keep for a target after merging
// the server profile over the local config (Unit 90).
type checkpointPolicy struct {
	mode   string // auto | on | off
	method string // db | dump
	keep   int
}

// resolveCheckpointPolicy merges the checkpoint policy server-first: it starts
// from the local config (already carrying defaults) and lets each field the
// SERVER profile declares override it. A field the server omits falls back to
// the local value, so a partial server [checkpoint] still inherits the rest.
func resolveCheckpointPolicy(prof config.RemoteProfile, cfg *config.Config) checkpointPolicy {
	pol := checkpointPolicy{
		mode:   cfg.CheckpointMode,
		method: cfg.CheckpointMethod,
		keep:   cfg.CheckpointKeep,
	}
	if prof.CheckpointMode != "" {
		pol.mode = prof.CheckpointMode
	}
	if prof.CheckpointMethod != "" {
		pol.method = prof.CheckpointMethod
	}
	if prof.CheckpointKeep != 0 {
		pol.keep = prof.CheckpointKeep
	}
	if pol.method == "" {
		pol.method = "db"
	}
	if pol.keep <= 0 {
		pol.keep = 2
	}
	return pol
}

// resolveCheckpointMode decides whether a deploy takes a checkpoint and by
// which method. Precedence: the --checkpoint/--no-checkpoint flags win, then
// the resolved [checkpoint] policy mode (server-first, on/off/auto), then —
// under auto — the resolved remote stage. The method follows --checkpoint=<m>
// when given, else the policy method.
func resolveCheckpointMode(p deployArgs, pol checkpointPolicy, stage string) (enabled bool, method string) {
	method = pol.method
	if method == "" {
		method = "db"
	}
	if p.checkpoint == "db" || p.checkpoint == "dump" {
		method = p.checkpoint
	}
	switch {
	case p.noCheckpoint:
		enabled = false
	case p.checkpointSet:
		enabled = true
	default:
		switch strings.ToLower(pol.mode) {
		case "on":
			enabled = true
		case "off":
			enabled = false
		default: // "auto" (or unset)
			enabled = stageWantsCheckpoint(stage)
		}
	}
	return enabled, method
}

// deployFailureRe matches the streamed Odoo output patterns that mean the
// module run left the DB in a bad state even when the process exits 0:
// a CRITICAL log line, a Python traceback header, or a registry-load failure.
// The last two alternatives catch a failed test suite (Unit 100) — Odoo's
// per-module `N failed, M error(s)` tally and unittest's `FAILED (failures=…)`
// summary — so `deploy --test` fails even on the rare zero-exit-with-failures.
var deployFailureRe = regexp.MustCompile(`\bCRITICAL\b|Traceback \(most recent call last\)|Failed to load registry|\b\d+ failed, \d+ error\(s\)|FAILED \((?:failures|errors)=`)

// runFailureScanner wraps the odoo-run StreamOut, counting lines that signal
// a failed migration/update so a zero-exit run can still be treated as failed.
type runFailureScanner struct {
	inner func(string)
	hits  int
}

func (s *runFailureScanner) scan(line string) {
	if deployFailureRe.MatchString(line) {
		s.hits++
	}
	if s.inner != nil {
		s.inner(line)
	}
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
	// Checkpoint summarizes the DB checkpoint taken before the run (nil when
	// checkpointing was off). RolledBack is true when the run failed and the
	// database was restored from that checkpoint.
	Checkpoint *CheckpointInfo `json:"checkpoint,omitempty"`
	RolledBack bool            `json:"rolled_back,omitempty"`
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

	// deploy --set-push is config-only: persist the local [deploy] push
	// default and exit — no remote resolution, no deploy.
	if p.setPush != nil {
		cfgCopy := *opts.Cfg
		cfgCopy.DeployPush = p.setPush
		if serr := config.SaveProject(&cfgCopy); serr != nil {
			return DeployResult{}, fmt.Errorf("save deploy push default: %w", serr)
		}
		opts.Cfg.DeployPush = p.setPush
		opts.log("INFO", "", "deploy push default set", opts.Cfg.DBName,
			[2]string{"push", strconv.FormatBool(*p.setPush)})
		return DeployResult{}, nil
	}

	// deploy test-management flags are config-only: persist the [deploy] test
	// toggle / test_modules list, report the resulting value, and exit — no
	// remote resolution, no deploy.
	if p.isTestManage() {
		if err := runDeployTestManage(opts, p); err != nil {
			return DeployResult{}, err
		}
		return DeployResult{}, nil
	}

	// deploy --rollback is a standalone operation: restore the target's most
	// recent checkpoint instead of deploying anything.
	if p.rollback {
		return runDeployRollback(ctx, opts, p)
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

	// Resolve the effective push default (Unit 95): explicit flags win, then
	// the server [deploy] push, then the local one, then off. Setting p.push
	// here lets every downstream check read it unchanged.
	p.push = resolveDeployPush(p, prof, opts.Cfg)

	conn := odoo.Conn{DB: target.dbName, Host: target.dbContainer}
	pg := remotePullEnv(ctx, sshHost, remotePath)
	conn.Port = pg["POSTGRES_PORT"]
	conn.User = pg["POSTGRES_USER"]
	conn.Password = pg["POSTGRES_PASSWORD"]

	// Assemble the remote context the checkpoint helpers work against and
	// resolve whether this deploy checkpoints its DB (and by which method).
	rsc := remoteShellContext{
		sshHost: sshHost, remotePath: remotePath, fromName: fromName,
		target: target, prof: prof, conn: conn,
	}
	ckptPolicy := resolveCheckpointPolicy(prof, opts.Cfg)
	ckptEnabled, ckptMethod := resolveCheckpointMode(p, ckptPolicy, target.stage)

	// Install vs update, decided by the remote instance's module states.
	opts.log("INFO", "remote", "querying installed modules", prof.DBName)
	states, err := remoteModuleStates(ctx, sshHost, remotePath, target, conn.User, target.dbName)
	if err != nil {
		return DeployResult{}, fmt.Errorf("query remote module states: %w", err)
	}
	install, update := splitInstallUpdate(modules, states)

	// Resolve the test run: whether tests run this deploy, and over which
	// modules (the pinned [deploy] test_modules, or the deployed set).
	runTests := resolveDeployTest(p, prof, opts.Cfg)
	var testMods []string
	if runTests {
		deployed := append(append([]string{}, update...), install...)
		testMods = resolveTestModules(prof, opts.Cfg, deployed)
	}

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

	// Resolve declared deploy actions (Unit 92) once and list them in the
	// plan. An invalid config surfaces here, before any deploy step runs.
	actions, actionsSrc, aerr := resolveDeployActions(prof, opts.Cfg, p.noActions)
	if aerr != nil {
		return DeployResult{}, aerr
	}
	actEnv := actionEnv{
		stage:      target.stage,
		db:         prof.DBName,
		remotePath: remotePath,
		modules:    strings.Join(append(append([]string(nil), update...), install...), " "),
	}
	for _, a := range actions {
		opts.log("INFO", "plan", "action", prof.DBName,
			[2]string{"name", a.Name}, [2]string{"phase", a.Phase},
			[2]string{"where", a.Where}, [2]string{"source", actionsSrc})
	}
	// runActions runs one phase; the two push phases are skipped (with a note)
	// on a deploy that isn't pushing, so the same profile serves both flows.
	runActions := func(phase string) error {
		if len(actionsForPhase(actions, phase)) == 0 {
			return nil
		}
		if (phase == config.PhasePrePush || phase == config.PhasePostPush) && !p.push {
			opts.log("INFO", "action", "skipped — no push in this run", prof.DBName,
				[2]string{"phase", phase})
			return nil
		}
		return runDeployActions(ctx, rsc, opts, actions, phase, actEnv)
	}

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
			Log: opts.Log, StreamOut: opts.StreamOut, OnSync: opts.OnSync,
		}
		// Resolve an explicit destination (server/local [push], no picker in
		// a headless deploy). Empty → per-module auto-detect.
		destBase := ""
		if dest, source, mkdir := resolvePushDest(pushArgs{}, prof, opts.Cfg); dest != "" {
			resolved, derr := applyResolvedDest(ctx, pushRSC, pushOpts, dest, source, mkdir, dryRun)
			if derr != nil {
				return derr
			}
			destBase = resolved
		}
		srcRoot := opts.Root
		if opts.PushSrcRoot != "" {
			srcRoot = opts.PushSrcRoot
		}
		opts.log("INFO", "push", "syncing modules to remote", prof.DBName,
			[2]string{"modules", strings.Join(pushMods, ",")})
		_, perr := pushModuleSet(ctx, pushRSC, pushOpts, pushMods, srcRoot, destBase, dryRun, false)
		return perr
	}

	if p.dryRun {
		if err := runPush(true); err != nil {
			return DeployResult{}, err
		}
		if ckptEnabled {
			opts.log("INFO", "plan", "checkpoint enabled", prof.DBName, [2]string{"method", ckptMethod})
		}
		opts.log("INFO", "", "dry-run — nothing executed", prof.DBName)
		return result, nil
	}
	if runTests && strings.EqualFold(target.stage, "prod") && !p.force {
		return DeployResult{}, fmt.Errorf(
			"%w: running tests on a prod target needs --force (test-on-prod is opt-in)", ErrUsage)
	}
	if strings.EqualFold(target.stage, "prod") && !p.force {
		if err := confirmProd(opts.Palette, "deploy", target.dbName); err != nil {
			return DeployResult{}, err
		}
	}
	if err := runActions(config.PhasePrePush); err != nil {
		return DeployResult{}, err
	}
	if err := runPush(false); err != nil {
		return DeployResult{}, fmt.Errorf("push failed: %w", err)
	}
	if err := runActions(config.PhasePostPush); err != nil {
		return DeployResult{}, err
	}

	// Disk preflight runs before any container stop, so a doomed deploy never
	// takes the service down: if the DB won't fit alongside its checkpoint,
	// abort now with both numbers named.
	if ckptEnabled {
		if err := checkpointPreflight(ctx, rsc, ckptMethod, opts.Log); err != nil {
			return DeployResult{}, err
		}
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
	// pre_deploy runs right before the containers are touched — the last
	// hook while the service is still up (maintenance page, job drain).
	if err := runActions(config.PhasePreDeploy); err != nil {
		return DeployResult{}, err
	}

	// Stop before the run. With a checkpoint we stop ONLY the Odoo app service
	// so the Postgres container stays up for the copy (the source DB then has no
	// app sessions but is still queryable); without one, stop everything as
	// before. A full stop would take the DB container down and the checkpoint's
	// psql/pg_dump could not run.
	stopCmd := remoteComposeCmd(remotePath, target.composeCmd, "stop")
	if ckptEnabled {
		stopCmd = remoteStopApp(rsc)
	}
	if err := step("stop", stopCmd); err != nil {
		return DeployResult{}, err
	}

	// Checkpoint the DB with the app stopped (no sessions on the source) but the
	// DB container still up. A creation failure aborts before the run so nothing
	// is half-migrated.
	var ckptEntry config.CheckpointEntry
	if ckptEnabled {
		entry, info, cerr := createCheckpoint(ctx, rsc, ckptMethod, deployedShas, opts.StreamOut, opts.Log)
		if cerr != nil {
			return DeployResult{}, cerr
		}
		ckptEntry = entry
		result.Checkpoint = &info
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
	if runTests {
		runFields = append(runFields, [2]string{"test", strings.Join(testMods, ",")})
	}
	opts.log("INFO", "odoo", "running module install/update", prof.DBName, runFields...)
	argv := odoo.WithI18nOverwrite(odoo.InstallUpdate(conn, install, update), overwrite)
	if runTests {
		argv = odoo.WithTests(argv, testMods)
	}
	scanner := &runFailureScanner{inner: opts.StreamOut}
	runErr := runSSHStream(ctx, sshHost, remoteContainerCmd(remotePath, target, argv), nil, scanner.scan)

	// Verify: a non-zero exit OR failure patterns in the stream (a run can
	// exit 0 while leaving modules broken) both fail the deploy.
	if runErr != nil || scanner.hits > 0 {
		if runErr == nil {
			opts.log("ERROR", "verify", "run reported errors — treating as failed", prof.DBName,
				[2]string{"hits", strconv.Itoa(scanner.hits)})
		}
		if ckptEnabled {
			return handleDeployFailure(ctx, opts, rsc, p, ckptEntry, result, projectKey, targetKey, runErr)
		}
		return DeployResult{}, deployRunError(runErr)
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

	// Record the checkpoint (kept for a later deploy --rollback) and prune the
	// tail to the retention keep count.
	if ckptEnabled {
		if err := config.AddCheckpoint(projectKey, targetKey, ckptEntry); err == nil {
			pruneCheckpoints(ctx, rsc, projectKey, targetKey, ckptPolicy.keep, opts.Log)
		}
	}

	// post_deploy runs after verify passed and the code is live. A failure
	// here marks the run failed but never rolls back a healthy deploy —
	// undoing a verified-green deploy for a notification hook is worse.
	if err := runActions(config.PhasePostDeploy); err != nil {
		opts.log("ERROR", "", "deploy succeeded, post_deploy action failed", prof.DBName,
			[2]string{"action", deployActionName(err)})
		return result, err
	}

	opts.log("INFO", "", "deploy complete", prof.DBName,
		[2]string{"update", strconv.Itoa(len(update))},
		[2]string{"install", strconv.Itoa(len(install))},
		[2]string{"skipped", strconv.Itoa(skipped)})
	return result, nil
}

// deployActionName extracts the failing action's name from a deploy-action
// error, or "" for any other error.
func deployActionName(err error) string {
	var ae *deployActionError
	if errors.As(err, &ae) {
		return ae.name
	}
	return ""
}

// deployRunError wraps the odoo-run outcome into a deploy error: the SSH
// error when the process failed, or a synthetic one when the run exited 0 but
// its stream carried failure patterns.
func deployRunError(runErr error) error {
	if runErr != nil {
		return fmt.Errorf("odoo run failed: %w", runErr)
	}
	return fmt.Errorf("odoo run reported errors in its output")
}

// handleDeployFailure is the checkpoint-on failure path. In an interactive
// session it asks before restoring (so the operator can inspect the broken
// DB); headless (--force or no TTY) it rolls back automatically. Either way
// the deploy's commits are never marked deployed. It returns the populated
// result (RolledBack set when restored) alongside the deploy error, so a
// headless caller like watch can read the outcome.
func handleDeployFailure(ctx context.Context, opts DeployOpts, rsc remoteShellContext, p deployArgs, entry config.CheckpointEntry, result DeployResult, projectKey, targetKey string, runErr error) (DeployResult, error) {
	// Interactive: offer to inspect instead of restoring. A decline keeps the
	// broken DB and the checkpoint (recorded so `deploy --rollback` finds it).
	if !p.force && stdinIsTTY() {
		if !confirmRollback(opts.Palette, rsc.prof.DBName, entry) {
			_ = config.AddCheckpoint(projectKey, targetKey, entry)
			opts.log("WARNING", "rollback", "skipped — restore later with deploy --rollback", rsc.prof.DBName,
				[2]string{"checkpoint", entry.Name})
			return result, deployRunError(runErr)
		}
	}

	// Stop the app for a clean restore (the run left it up), keeping the DB
	// container up so the restore's psql/pg_restore can run.
	opts.log("INFO", "rollback", "stopping app before restore", rsc.prof.DBName)
	_ = runSSHStream(ctx, rsc.sshHost, remoteStopApp(rsc), nil, opts.StreamOut)

	consumed, rerr := restoreCheckpoint(ctx, rsc, entry, opts.StreamOut, opts.Log)
	if rerr != nil {
		// The rollback itself failed: keep the checkpoint recorded for a manual
		// retry and surface both failures.
		_ = config.AddCheckpoint(projectKey, targetKey, entry)
		opts.log("ERROR", "rollback", "rollback failed — checkpoint preserved", rsc.prof.DBName,
			[2]string{"checkpoint", entry.Name}, [2]string{"err", rerr.Error()})
		return result, fmt.Errorf("odoo run failed and rollback failed: %v (run error: %w)", rerr, runErrOrSynthetic(runErr))
	}
	_ = runSSHStream(ctx, rsc.sshHost, remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "up", "-d"), nil, opts.StreamOut)

	// A dump survives its own restore, so keep it recorded for a possible
	// re-rollback; a db-method copy was consumed by the rename.
	if !consumed {
		_ = config.AddCheckpoint(projectKey, targetKey, entry)
	}
	result.RolledBack = true
	opts.log("INFO", "rollback", "rolled back — commits not marked deployed", rsc.prof.DBName)
	return result, deployRunError(runErr)
}

// runErrOrSynthetic returns runErr, or a synthetic "reported errors" error
// when the run exited 0 but its stream flagged a failure.
func runErrOrSynthetic(runErr error) error {
	if runErr != nil {
		return runErr
	}
	return fmt.Errorf("run reported errors in its output")
}

// runDeployRollback restores a target's checkpoint outside a deploy
// (deploy --rollback): resolve the target, pick a checkpoint (newest headless,
// picker when >1 and interactive), red-confirm with an age warning, stop →
// restore → up, then un-mark the checkpoint's commits so they can be
// redeployed.
func runDeployRollback(ctx context.Context, opts DeployOpts, p deployArgs) (DeployResult, error) {
	logFn := func(level, sub, msg, db string, fields ...[2]string) { opts.log(level, sub, msg, db, fields...) }
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, logFn)
	if err != nil {
		return DeployResult{}, err
	}
	projectKey := config.ProjectKey(opts.Root)
	targetKey := config.DeployTargetKey(rsc.sshHost, rsc.remotePath)
	entries := config.LoadCheckpoints(projectKey, targetKey)
	if len(entries) == 0 {
		return DeployResult{}, fmt.Errorf("%w: no checkpoints recorded for this target", ErrUsage)
	}

	chosen := entries[0] // newest
	if len(entries) > 1 && stdinIsTTY() {
		labels := make([]string, len(entries))
		byLabel := make(map[string]config.CheckpointEntry, len(entries))
		for i, e := range entries {
			lbl := e.Name + "  ·  " + e.Method + ", " + humanAge(time.Since(e.CreatedAt)) + " ago"
			labels[i] = lbl
			byLabel[lbl] = e
		}
		pick, perr := PickOne("Checkpoint to restore", labels, opts.Palette)
		if perr != nil {
			return DeployResult{}, perr
		}
		chosen = byLabel[pick]
	}

	if !p.force {
		if err := confirmRollbackAged(opts.Palette, rsc.prof.DBName, chosen); err != nil {
			return DeployResult{}, err
		}
	}

	opts.log("INFO", "rollback", "stopping app before restore", rsc.prof.DBName)
	if err := runSSHStream(ctx, rsc.sshHost, remoteStopApp(rsc), nil, opts.StreamOut); err != nil {
		return DeployResult{}, fmt.Errorf("stop failed: %w", err)
	}
	consumed, rerr := restoreCheckpoint(ctx, rsc, chosen, opts.StreamOut, opts.Log)
	if rerr != nil {
		return DeployResult{}, fmt.Errorf("restore failed: %w", rerr)
	}
	if err := runSSHStream(ctx, rsc.sshHost, remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "up", "-d"), nil, opts.StreamOut); err != nil {
		return DeployResult{}, fmt.Errorf("up -d failed: %w", err)
	}

	// Un-mark the checkpoint's commits so they can be corrected and redeployed;
	// drop a consumed (db-method) checkpoint from the store.
	_ = config.UnmarkDeployed(projectKey, targetKey, chosen.DeploySHAs)
	if consumed {
		_ = config.RemoveCheckpoint(projectKey, targetKey, chosen.Name)
	}
	opts.log("INFO", "", "rollback complete", rsc.prof.DBName,
		[2]string{"checkpoint", chosen.Name}, [2]string{"unmarked", strconv.Itoa(len(chosen.DeploySHAs))})
	return DeployResult{
		Target:     rsc.fromName,
		DB:         rsc.prof.DBName,
		RolledBack: true,
		JSON:       p.jsonOut,
	}, nil
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
