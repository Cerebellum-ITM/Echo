package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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
	// Via names the caller in the deploy lock ("deploy" when empty).
	Via string
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
	// moduleRefs pins modules to a ref (`--modules mod@ref`, or every
	// unpinned --modules entry under `--at <ref>`): they ship as committed at
	// that ref, never from the working tree (Unit 125).
	moduleRefs map[string]string
	at         string
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
	// consumeCheckpoint restores a "db"-method checkpoint by renaming it over
	// the live DB (the pre-Unit behavior) instead of copying it back — cheaper
	// on disk but it destroys the checkpoint, leaving no restore point. Applies
	// only to `deploy --rollback`; ignored for the "dump" method, which always
	// preserves its file.
	consumeCheckpoint bool
	// noActions skips all declared deploy actions (Unit 92) for this run —
	// the escape hatch when a server-declared action is broken.
	noActions bool
	// noPush forces the code push off even when `[deploy] push` makes it the
	// configured default (Unit 95). Mutually exclusive with --push.
	noPush bool
	// setPush, when non-nil, is a config-only request to persist `[deploy]
	// push` locally and exit (deploy --set-push[=true|false]).
	setPush *bool
	// setCheckpoint, when non-nil, is a config-only request to persist the
	// local [checkpoint] policy and exit (Unit 104). Its pointer fields carry
	// only the values named in this invocation (--set-checkpoint[=mode],
	// --set-checkpoint-method, --set-checkpoint-keep); unnamed fields keep the
	// currently-resolved value.
	setCheckpoint *checkpointManage
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
	// rollbackOnFail fixes the on-failure rollback decision without prompting
	// (Unit 101): non-nil overrides the TTY-based default — true rolls back,
	// false leaves the broken DB. nil = fall through to the confirm/headless
	// default. Set by --rollback-on-fail / --no-rollback-on-fail.
	rollbackOnFail *bool
	// noGit forces the legacy rsync push for one run on a git-deploy target
	// (Unit 102), the escape hatch when the git advance can't or shouldn't run.
	noGit bool
	// noLint skips the pre-flight lint for one run (Unit 110). Per-run and
	// logged, deliberately not a config key: a persisted opt-out would be
	// set once by whoever hit a false positive and then stay off forever on
	// the machine that needed the check most.
	noLint bool
	// noDepCheck skips the dependency check of the shipped modules for one
	// run (Unit 128); per-run and logged for the same reason as noLint.
	noDepCheck bool
	// restoreCode / restoreCodeSet drive the standalone code-only restore of a
	// git-deploy target (deploy --restore-code [<sha>]) — no DB, no checkpoint.
	// restoreCodeSet is true whenever the flag is present; restoreCode holds the
	// explicit hash, or "" for the interactive picker.
	restoreCode    string
	restoreCodeSet bool
	// setCode / setCodeSet drive `deploy --set-code <ref>` (Unit 112): move a
	// git-deploy target's code to ANY local or fetched ref, not only to a hash
	// it already ran. fetch / noFetch control the refresh of the ref's remote
	// before resolving it, keepOverlay preserves the server's dirty overlay
	// (default: the module-scoped overlay is cleaned, so the checkout matches
	// the ref), and withLocal resets the local [promote] branch to the same
	// ref first.
	setCode     string
	setCodeSet  bool
	fetch       bool
	noFetch     bool
	keepOverlay bool
	withLocal   bool
	// setGitBranch names the branch a git-deploy target's code lives on (Unit
	// 113). Config-only unless rename is set, which also moves the branch that
	// is already on the server.
	setGitBranch string
	rename       bool
	// lock prints the target's deploy lock and exits (Unit 124).
	lock bool
	// savePlan writes what a dry run resolved to this path; apply runs the
	// plan saved at this path, or refuses when anything changed (Unit 129).
	savePlan string
	apply    string
}

// applyFlags are the only flags --apply accepts: everything else shapes the
// run, and the plan already decided it.
var applyFlags = []string{"--apply", "--force", "--rollback-on-fail", "--no-rollback-on-fail", "--json", "--dry-run", "--from"}

// isTestManage reports whether the args carry a config-only test-management
// operation (toggle / pin-list edit) that runs standalone and exits.
func (p deployArgs) isTestManage() bool {
	return p.testToggle || p.testPick || p.testClear ||
		p.testModulesSet != nil || len(p.testAdd) > 0 || len(p.testRm) > 0
}

// checkpointManage is the config-only checkpoint-policy edit built from the
// --set-checkpoint* flags (Unit 104). A nil field means "not named in this
// invocation" — runDeployCheckpointManage leaves the resolved value untouched.
type checkpointManage struct {
	mode   *string // on | off | auto
	method *string // db | dump
	keep   *int    // >= 1
}

// isCheckpointManage reports whether the args carry a config-only
// checkpoint-policy edit that persists to the local project profile and exits.
func (p deployArgs) isCheckpointManage() bool {
	return p.setCheckpoint != nil
}

// parseDeployArgs extracts --from/--limit/--dry-run/--force/--i18n/--no-i18n
// plus the non-interactive --commits/--modules selection. Deploy takes no
// positionals — the commits come from the picker or those flags.
func parseDeployArgs(args []string) (deployArgs, error) {
	out := deployArgs{limit: 20}
	var sawRB, sawNoRB bool
	var flags []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			name, _, _ := strings.Cut(a, "=")
			flags = append(flags, name)
		}
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
		// -E is recognized only so the Reverb guard can explain why deploy
		// does not support a Reverb target yet, instead of the opaque
		// "unknown flag" the catch-all would produce.
		case a == "-E", a == "--env":
			if i+1 >= len(args) {
				return out, fmt.Errorf("%w: -E requires <project>/<env>", ErrUsage)
			}
			out.from = ReverbRef(args[i+1])
			i++
		case strings.HasPrefix(a, "-E="):
			out.from = ReverbRef(strings.TrimPrefix(a, "-E="))
		case strings.HasPrefix(a, "--env="):
			out.from = ReverbRef(strings.TrimPrefix(a, "--env="))
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
		case a == "--set-checkpoint":
			if out.setCheckpoint == nil {
				out.setCheckpoint = &checkpointManage{}
			}
			m := "on"
			out.setCheckpoint.mode = &m
		case strings.HasPrefix(a, "--set-checkpoint="):
			v := strings.TrimPrefix(a, "--set-checkpoint=")
			if v != "on" && v != "off" && v != "auto" {
				return out, fmt.Errorf("%w: --set-checkpoint takes on, off or auto, got %q", ErrUsage, v)
			}
			if out.setCheckpoint == nil {
				out.setCheckpoint = &checkpointManage{}
			}
			out.setCheckpoint.mode = &v
		case strings.HasPrefix(a, "--set-checkpoint-method="):
			v := strings.TrimPrefix(a, "--set-checkpoint-method=")
			if v != "db" && v != "dump" {
				return out, fmt.Errorf("%w: --set-checkpoint-method takes db or dump, got %q", ErrUsage, v)
			}
			if out.setCheckpoint == nil {
				out.setCheckpoint = &checkpointManage{}
			}
			out.setCheckpoint.method = &v
		case strings.HasPrefix(a, "--set-checkpoint-keep="):
			v := strings.TrimPrefix(a, "--set-checkpoint-keep=")
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return out, fmt.Errorf("%w: --set-checkpoint-keep takes a number >= 1, got %q", ErrUsage, v)
			}
			if out.setCheckpoint == nil {
				out.setCheckpoint = &checkpointManage{}
			}
			out.setCheckpoint.keep = &n
		case a == "--rollback":
			out.rollback = true
		case a == "--consume-checkpoint":
			out.consumeCheckpoint = true
		case a == "--no-git":
			out.noGit = true
		case a == "--no-lint":
			out.noLint = true
		case a == "--no-dep-check":
			out.noDepCheck = true
		case a == "--restore-code":
			// The SHA is optional: a following non-flag token is the target
			// hash; a bare --restore-code opens the interactive picker.
			out.restoreCodeSet = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				out.restoreCode = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--restore-code="):
			out.restoreCodeSet = true
			out.restoreCode = strings.TrimPrefix(a, "--restore-code=")
			if strings.TrimSpace(out.restoreCode) == "" {
				return out, fmt.Errorf("%w: --restore-code= needs a SHA (use bare --restore-code for the picker)", ErrUsage)
			}
		case a == "--set-code":
			// The ref is required: unlike --restore-code, whose universe is the
			// handful of hashes the server has run, --set-code can target any
			// ref in the repo — there is nothing sensible to pick from.
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return out, fmt.Errorf("%w: --set-code needs a ref (branch, tag or SHA)", ErrUsage)
			}
			out.setCodeSet = true
			out.setCode = args[i+1]
			i++
		case strings.HasPrefix(a, "--set-code="):
			out.setCodeSet = true
			out.setCode = strings.TrimPrefix(a, "--set-code=")
			if strings.TrimSpace(out.setCode) == "" {
				return out, fmt.Errorf("%w: --set-code needs a ref (branch, tag or SHA)", ErrUsage)
			}
		case a == "--set-git-branch":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return out, fmt.Errorf("%w: --set-git-branch needs a branch name", ErrUsage)
			}
			out.setGitBranch = args[i+1]
			i++
		case strings.HasPrefix(a, "--set-git-branch="):
			out.setGitBranch = strings.TrimPrefix(a, "--set-git-branch=")
			if strings.TrimSpace(out.setGitBranch) == "" {
				return out, fmt.Errorf("%w: --set-git-branch needs a branch name", ErrUsage)
			}
		case a == "--rename":
			out.rename = true
		case a == "--lock":
			out.lock = true
		case a == "--at":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return out, fmt.Errorf("%w: --at needs a ref (branch, tag or SHA)", ErrUsage)
			}
			out.at = args[i+1]
			i++
		case strings.HasPrefix(a, "--at="):
			out.at = strings.TrimPrefix(a, "--at=")
			if strings.TrimSpace(out.at) == "" {
				return out, fmt.Errorf("%w: --at needs a ref (branch, tag or SHA)", ErrUsage)
			}
		case a == "--fetch":
			out.fetch = true
		case a == "--no-fetch":
			out.noFetch = true
		case a == "--keep-overlay":
			out.keepOverlay = true
		case a == "--with-local":
			out.withLocal = true
		case a == "--no-actions":
			out.noActions = true
		case a == "--rollback-on-fail":
			t := true
			out.rollbackOnFail = &t
			sawRB = true
		case a == "--no-rollback-on-fail":
			f := false
			out.rollbackOnFail = &f
			sawNoRB = true
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
		case a == "--save-plan", a == "--apply":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return out, fmt.Errorf("%w: %s needs the path of a plan file", ErrUsage, a)
			}
			if a == "--apply" {
				out.apply = args[i+1]
			} else {
				out.savePlan = args[i+1]
			}
			i++
		case strings.HasPrefix(a, "--save-plan="):
			out.savePlan = strings.TrimPrefix(a, "--save-plan=")
			if strings.TrimSpace(out.savePlan) == "" {
				return out, fmt.Errorf("%w: --save-plan needs the path of a plan file", ErrUsage)
			}
		case strings.HasPrefix(a, "--apply="):
			out.apply = strings.TrimPrefix(a, "--apply=")
			if strings.TrimSpace(out.apply) == "" {
				return out, fmt.Errorf("%w: --apply needs the path of a plan file", ErrUsage)
			}
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
	if out.apply != "" {
		for _, f := range flags {
			if !slices.Contains(applyFlags, f) {
				return out, fmt.Errorf("%w: --apply runs the saved plan as reviewed; %s would change it — re-plan with deploy --dry-run --save-plan", ErrUsage, f)
			}
		}
	}
	if out.savePlan != "" && !out.dryRun {
		return out, fmt.Errorf("%w: --save-plan writes the plan of a dry run; add --dry-run", ErrUsage)
	}
	if out.i18n && out.noI18n {
		return out, fmt.Errorf("%w: --i18n and --no-i18n are mutually exclusive", ErrUsage)
	}
	if out.at != "" && (len(out.modules) == 0 || len(out.commits) > 0) {
		return out, fmt.Errorf("%w: --at pins the --modules entries to a ref; it needs --modules and cannot combine with --commits", ErrUsage)
	}
	var err error
	if out.modules, out.moduleRefs, err = splitModuleRefs(out.modules, out.at); err != nil {
		return out, err
	}
	if len(out.moduleRefs) > 0 && out.noPush {
		return out, fmt.Errorf("%w: a module pinned to a ref must ship — --no-push would run -u on whatever the server has", ErrUsage)
	}
	if out.auto && (len(out.commits) > 0 || len(out.modules) > 0) {
		return out, fmt.Errorf("%w: --auto cannot be combined with --commits/--modules", ErrUsage)
	}
	if out.checkpointSet && out.noCheckpoint {
		return out, fmt.Errorf("%w: --checkpoint and --no-checkpoint are mutually exclusive", ErrUsage)
	}
	if out.isCheckpointManage() && (out.checkpointSet || out.noCheckpoint) {
		return out, fmt.Errorf("%w: --set-checkpoint sets the persisted policy; it can't combine with the per-run --checkpoint/--no-checkpoint", ErrUsage)
	}
	if out.push && out.noPush {
		return out, fmt.Errorf("%w: --push and --no-push are mutually exclusive", ErrUsage)
	}
	if out.rollback && (out.auto || len(out.commits) > 0 || len(out.modules) > 0 || out.push) {
		return out, fmt.Errorf("%w: --rollback cannot be combined with --commits/--modules/--auto/--push", ErrUsage)
	}
	if out.consumeCheckpoint && !out.rollback {
		return out, fmt.Errorf("%w: --consume-checkpoint only applies to --rollback", ErrUsage)
	}
	if out.restoreCodeSet && (out.rollback || out.auto || out.push ||
		len(out.commits) > 0 || len(out.modules) > 0 || out.isTestManage()) {
		return out, fmt.Errorf("%w: --restore-code runs on its own (no deploy selection/rollback)", ErrUsage)
	}
	if out.noGit && out.restoreCodeSet {
		return out, fmt.Errorf("%w: --no-git and --restore-code are mutually exclusive", ErrUsage)
	}
	if out.setCodeSet && (out.rollback || out.auto || out.push || out.restoreCodeSet ||
		len(out.commits) > 0 || len(out.modules) > 0 || out.isTestManage()) {
		return out, fmt.Errorf("%w: --set-code runs on its own (no deploy selection/rollback/restore)", ErrUsage)
	}
	if out.noGit && out.setCodeSet {
		return out, fmt.Errorf("%w: --no-git and --set-code are mutually exclusive — --set-code IS the git path", ErrUsage)
	}
	if out.fetch && out.noFetch {
		return out, fmt.Errorf("%w: --fetch and --no-fetch are mutually exclusive", ErrUsage)
	}
	if !out.setCodeSet && (out.keepOverlay || out.withLocal) {
		return out, fmt.Errorf("%w: --keep-overlay/--with-local only apply to --set-code", ErrUsage)
	}
	if !out.setCodeSet && len(out.moduleRefs) == 0 && (out.fetch || out.noFetch) {
		return out, fmt.Errorf("%w: --fetch/--no-fetch only apply to --set-code and to modules pinned to a ref", ErrUsage)
	}
	if out.setGitBranch != "" && (out.setCodeSet || out.restoreCodeSet || out.rollback || out.auto || out.push ||
		len(out.commits) > 0 || len(out.modules) > 0 || out.isTestManage() || out.isCheckpointManage()) {
		return out, fmt.Errorf("%w: --set-git-branch names the deploy branch and exits (no deploy selection)", ErrUsage)
	}
	if out.lock && (out.setGitBranch != "" || out.setCodeSet || out.restoreCodeSet || out.rollback || out.auto ||
		out.push || out.dryRun || len(out.commits) > 0 || len(out.modules) > 0 || out.setPush != nil ||
		out.isTestManage() || out.isCheckpointManage()) {
		return out, fmt.Errorf("%w: --lock prints the target's deploy lock and exits (no deploy selection)", ErrUsage)
	}
	if out.rename && out.setGitBranch == "" {
		return out, fmt.Errorf("%w: --rename only applies to --set-git-branch", ErrUsage)
	}
	if out.test && out.noTest {
		return out, fmt.Errorf("%w: --test and --no-test are mutually exclusive", ErrUsage)
	}
	if sawRB && sawNoRB {
		return out, fmt.Errorf("%w: --rollback-on-fail and --no-rollback-on-fail are mutually exclusive", ErrUsage)
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

// runDeployCheckpointManage persists the local [checkpoint] policy from the
// --set-checkpoint* flags and logs the resulting state (Unit 104). Fields not
// named in the invocation keep their currently-resolved value; the write is
// tagged as project-sourced so SaveProject re-emits it. Config-only: no remote
// resolution, no deploy.
func runDeployCheckpointManage(opts DeployOpts, p deployArgs) error {
	cfgCopy := *opts.Cfg
	cm := p.setCheckpoint
	if cm.mode != nil {
		cfgCopy.CheckpointMode = *cm.mode
	}
	if cm.method != nil {
		cfgCopy.CheckpointMethod = *cm.method
	}
	if cm.keep != nil {
		cfgCopy.CheckpointKeep = *cm.keep
	}
	cfgCopy.CheckpointSource = "project"
	if err := config.SaveProject(&cfgCopy); err != nil {
		return fmt.Errorf("save checkpoint policy: %w", err)
	}
	opts.Cfg.CheckpointMode = cfgCopy.CheckpointMode
	opts.Cfg.CheckpointMethod = cfgCopy.CheckpointMethod
	opts.Cfg.CheckpointKeep = cfgCopy.CheckpointKeep
	opts.Cfg.CheckpointSource = "project"
	opts.log("INFO", "", "checkpoint policy set", opts.Cfg.DBName,
		[2]string{"mode", cfgCopy.CheckpointMode},
		[2]string{"method", cfgCopy.CheckpointMethod},
		[2]string{"keep", strconv.Itoa(cfgCopy.CheckpointKeep)})
	return nil
}

// pickTestModules opens a multi-select over the project's modules with the
// current pinned list pre-checked, so one picker both adds and removes. An
// empty confirmed selection clears the list (back to auto).
func pickTestModules(opts DeployOpts, current []string) ([]string, error) {
	available := mergeTestModules(listAddons(opts.Cfg, opts.Root), current)
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
// The Odoo tally is matched ONLY with a non-zero failure OR error count:
// a passing suite prints `0 failed, 0 error(s)`, which must NOT count as a hit.
var deployFailureRe = regexp.MustCompile(`\bCRITICAL\b|Traceback \(most recent call last\)|Failed to load registry|\b[1-9]\d* failed, \d+ error\(s\)|\b\d+ failed, [1-9]\d* error\(s\)|FAILED \((?:failures|errors)=`)

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
	// Source, SHA and Version describe the code the run shipped for the
	// module (empty when the deploy did not push).
	Source  string `json:"source,omitempty"`
	SHA     string `json:"sha,omitempty"`
	Version string `json:"version,omitempty"`
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
	// CodeSHA is the deploy branch's SHA on the server after a git-deploy run
	// (Unit 102) — the exact hash now checked out. Empty for non-git targets.
	CodeSHA string `json:"code_sha,omitempty"`
	// Ref, PreviousCodeSHA and Cleaned describe a `deploy --set-code` run (Unit
	// 112): the ref asked for, where the deploy branch stood before the move,
	// and how many overlay files were removed on the way.
	Ref             string `json:"ref,omitempty"`
	PreviousCodeSHA string `json:"previous_sha,omitempty"`
	Cleaned         int    `json:"cleaned,omitempty"`
	// Lock is the target's deploy lock, set only by `deploy --lock`.
	Lock *DeployLock `json:"lock,omitempty"`
	// Dependencies lists the uses, in modules staying on the server, of the
	// symbols the shipped modules no longer define (Unit 128).
	Dependencies []DependencyFinding `json:"dependencies,omitempty"`
	// Plan is the plan --save-plan wrote or --apply ran; PlanStale lists why
	// --apply refused it (Unit 129).
	Plan      *DeployPlan  `json:"plan,omitempty"`
	PlanStale []PlanChange `json:"plan_stale,omitempty"`
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
	dir   string // repo-relative addon directory; differs from name in a nested layout
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
	var saved *DeployPlan
	if p.apply != "" {
		plan, applied, aerr := loadAppliedPlan(opts, p)
		if aerr != nil {
			return DeployResult{}, aerr
		}
		saved, p = &plan, applied
	}
	planning := saved != nil || p.savePlan != ""
	if err := requireNoReverb("deploy", p.from); err != nil {
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

	// deploy --set-checkpoint* is config-only: persist the local [checkpoint]
	// policy and exit — no remote resolution, no deploy.
	if p.isCheckpointManage() {
		if err := runDeployCheckpointManage(opts, p); err != nil {
			return DeployResult{}, err
		}
		return DeployResult{}, nil
	}

	// deploy --rollback is a standalone operation: restore the target's most
	// recent checkpoint instead of deploying anything.
	if p.rollback {
		return runDeployRollback(ctx, opts, p)
	}

	// deploy --restore-code is a standalone code-only restore: move the remote
	// deploy branch to a hash and restart Odoo, without touching the DB.
	if p.restoreCodeSet {
		return runDeployRestoreCode(ctx, opts, p)
	}

	if p.lock {
		return runDeployLockShow(ctx, opts, p)
	}

	// deploy --set-git-branch names the target's deploy branch: config-only
	// unless --rename also moves the branch already on the server.
	if p.setGitBranch != "" {
		return runDeploySetGitBranch(ctx, opts, p)
	}

	// deploy --set-code re-baselines the target's code onto any ref — a force
	// move, not a deploy: no selection, no DB, no checkpoint, no lint.
	if p.setCodeSet {
		return runDeploySetCode(ctx, opts, p)
	}

	// Validate an explicit --modules list against the local repo before any
	// remote work: a name that isn't an addon here (no __manifest__.py) is a
	// usage error, caught early so we never touch the server for a typo.
	// A module pinned to a ref is validated in that ref's tree instead: it
	// may not exist on disk at all.
	var plainModules []string
	for _, m := range p.modules {
		if _, pinned := p.moduleRefs[m]; pinned {
			continue
		}
		_, name, rerr := resolveAddon(opts.Cfg, opts.Root, m)
		if rerr != nil {
			return DeployResult{}, addonError(opts.Root, m, rerr)
		}
		plainModules = append(plainModules, name)
	}
	p.modules = plainModules
	refSources, err := resolveModuleRefs(ctx, opts, p)
	if err != nil {
		return DeployResult{}, err
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
			if p.savePlan != "" {
				opts.log("INFO", "plan", "no plan written — nothing to deploy", "", [2]string{"path", p.savePlan})
			}
			return DeployResult{Target: fromName, JSON: p.jsonOut}, nil
		}
	case len(p.commits) > 0 || len(p.modules) > 0 || len(refSources) > 0:
		// Non-interactive selection (deploy builder / sequence / --last):
		// resolve the SHAs and module names straight from the flags, no picker.
		selected, selectedDirty = deploySelectionFromFlags(ctx, opts, p, dirty)
		if len(selected) == 0 && len(selectedDirty) == 0 && len(refSources) == 0 {
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
	commitsByModule := map[string][]string{}
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
			if !i18nTouched[dm.name] && pathsTouchI18n(dm.dir, dm.paths) {
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
		commitsByModule[mod] = append(commitsByModule[mod], c.sha)
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
	for mod := range refSources {
		if _, viaCommit := commitsByModule[mod]; viaCommit {
			return DeployResult{}, fmt.Errorf("%w: %s is both in the selected commits and pinned to %s — pick one source",
				ErrUsage, mod, p.moduleRefs[mod])
		}
		seen[mod] = true
		modules = append(modules, mod)
	}
	if len(modules) == 0 {
		return DeployResult{}, fmt.Errorf("no deployable modules: every selected commit was skipped")
	}
	sort.Strings(modules)

	// Git-deploy topology (Unit 102): when the target opts in (and --no-git
	// isn't set), a run's COMMITTED content advances a deploy branch on the
	// server with identical SHAs instead of an rsync overlay; the dirty overlay
	// still rsyncs.
	gitCfg := resolveGitDeploy(opts.Cfg, fromName, sshHost, remotePath)
	gitActive := gitCfg.enabled && !p.noGit
	dirtyNameSet := map[string]bool{}
	for _, dm := range selectedDirty {
		dirtyNameSet[dm.name] = true
	}

	// What each module ships (Unit 125): a pinned ref, or — on a target
	// without a deploy branch — the module's newest selected commit. Both
	// come from `git archive`, so the working tree never leaks into them; a
	// module selected as dirty keeps shipping the working tree.
	sources := maps.Clone(refSources)
	if !gitActive {
		for mod, shas := range commitsByModule {
			if dirtyNameSet[mod] {
				opts.log("INFO", "", "selected as dirty and by commits — shipping the working tree", "",
					[2]string{"module", mod})
				continue
			}
			tip, terr := resolveGitTip(ctx, opts.Root, shas)
			if terr != nil {
				return DeployResult{}, fmt.Errorf("module %s: %w", mod, terr)
			}
			dir, lerr := locateModuleAt(ctx, opts.Cfg, opts.Root, tip, mod)
			if lerr != nil {
				return DeployResult{}, lerr
			}
			sources[mod] = moduleSource{kind: lockSourceCommit, sha: tip, path: dir}
		}
	}
	for _, dm := range dirty {
		if src, ok := sources[dm.name]; ok {
			opts.log("WARNING", "", "module has uncommitted changes — ignored", "",
				[2]string{"module", dm.name}, [2]string{"shipping", shortSHA(src.sha)})
		}
	}
	archiveDir := ""
	if len(sources) > 0 {
		dir, cleanup, aerr := archiveModuleSources(ctx, opts.Root, sources)
		if aerr != nil {
			return DeployResult{}, aerr
		}
		defer cleanup()
		archiveDir = dir
	}

	// Pre-flight lint (Unit 110): the selected modules are checked against
	// the data loader's own rules here — after the selection, which is what
	// defines the scope, and before the first remote contact. A block at
	// this point costs nothing: no push, no checkpoint, no `-u`, nothing to
	// roll back. It blocks on manifest-listed defects only, so it can never
	// be stricter than the server (see deployLintPreflight).
	lintScopes := []lintScope{{root: opts.Root}, {root: archiveDir}}
	for _, m := range modules {
		if _, archived := sources[m]; archived {
			lintScopes[1].modules = append(lintScopes[1].modules, m)
		} else {
			lintScopes[0].modules = append(lintScopes[0].modules, m)
		}
	}
	if lerr := deployLintPreflight(opts, p, lintScopes); lerr != nil {
		return DeployResult{}, lerr
	}

	// Remote profile + DB credentials, same as i18n-pull.
	cfgRemote := *opts.Cfg
	cfgRemote.ConnectSSHHost = sshHost
	cfgRemote.ConnectRemotePath = remotePath
	opts.log("INFO", "remote", "reading remote profile", "", [2]string{"host", sshHost})
	prof, err := fetchRemoteProfile(ctx, ConnectOpts{Cfg: &cfgRemote, Root: opts.Root})
	if err != nil {
		return DeployResult{}, err
	}
	target := remoteConnectTarget(prof)
	opts.log("INFO", "system", "system", prof.DBName,
		statusFields(target.odooVersion, prof.Stage,
			statusProjectName(opts.Cfg, true, remotePath, fromName),
			prof.DBName)...)
	warnUndeclaredStage(target, opts.log)

	// Resolve the effective push default (Unit 95): explicit flags win, then
	// the server [deploy] push, then the local one, then off. Setting p.push
	// here lets every downstream check read it unchanged.
	p.push = resolveDeployPush(p, prof, opts.Cfg)
	if len(refSources) > 0 && !p.push {
		return DeployResult{}, fmt.Errorf("%w: a module pinned to a ref must ship, and this target does not push by default — add --push", ErrUsage)
	}

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
	// A plan records the lock as read, so it is read even without a push, and
	// a read that fails leaves nothing to check the plan against.
	var current DeployLock
	var lockID string
	switch {
	case planning:
		raw, state, lerr := fetchDeployLock(ctx, rsc)
		if state == lockUnreadable {
			return DeployResult{}, fmt.Errorf("read the deploy lock, which the plan records: %w", lerr)
		}
		lockID = lockDigest(raw, state)
		current, _ = decodeDeployLock(rsc, opts.Log, raw)
	case p.push:
		current, _ = readDeployLock(ctx, rsc, opts.Log)
	}

	// gitTip is the single hash a git-deploy branch advances to; a non-linear
	// commit selection errors here, before any remote change.
	var gitTip, gitPreCodeSHA string
	if gitActive && len(deployedShas) > 0 {
		tip, terr := resolveGitTip(ctx, opts.Root, deployedShas)
		if terr != nil {
			return DeployResult{}, terr
		}
		gitTip = tip
	}

	// Install vs update, decided by the remote instance's module states.
	opts.log("INFO", "remote", "querying installed modules", prof.DBName)
	states, installedVersions, err := remoteModuleStates(ctx, sshHost, remotePath, target, conn.User, target.dbName)
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

	// A pinned module has no commit diff to scan: its i18n/ tree is compared
	// with the one at the commit the lock says the target runs.
	for mod, src := range refSources {
		prev, hasPrev := current.Modules[mod]
		switch touched, known := refTouchesI18n(ctx, opts.Root, prev, hasPrev, src.sha, src.path); {
		case touched:
			i18nTouched[mod] = true
			opts.log("INFO", "i18n", "i18n changes detected", "",
				[2]string{"module", mod}, [2]string{"ref", src.ref})
		case !known:
			opts.log("INFO", "i18n", "i18n changes unknown for a pinned module — pass --i18n to overwrite its terms", "",
				[2]string{"module", mod})
		}
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

	// How each module travels: from a commit's tree (archived), on the git
	// deploy branch, or rsynced from the working tree — the only path on a
	// target without git for anything not archived, and the dirty overlay on
	// one with it.
	var worktreeMods, branchMods []string
	archived := map[string]moduleSource{}
	for _, m := range append(append([]string(nil), update...), install...) {
		switch src, ok := sources[m]; {
		case ok:
			archived[m] = src
		case gitActive && !dirtyNameSet[m]:
			branchMods = append(branchMods, m)
		default:
			worktreeMods = append(worktreeMods, m)
		}
	}
	var shipped map[string]LockModule
	var shippedBase *LockBase
	var releasing []string
	if p.push {
		shipped, shippedBase = deployShipEntries(ctx, opts, gitCfg, gitTip, branchMods, worktreeMods, archived, archiveDir)
		logCodePlan(opts.Log, prof.DBName, shipped, current, installedVersions)
		if p.noDepCheck {
			opts.log("WARNING", "plan", "dependency check skipped", prof.DBName,
				[2]string{"flag", "--no-dep-check"})
		} else {
			result.Dependencies = checkDeployDependencies(ctx, opts, rsc, gitCfg, gitTip, branchMods, worktreeMods, archived, archiveDir)
		}
		for i, m := range result.Modules {
			if e, ok := shipped[m.Name]; ok {
				result.Modules[i].Source, result.Modules[i].SHA, result.Modules[i].Version = e.Source, e.SHA, e.Version
			}
		}
		// A module the lock pins to a ref that now ships from anything else
		// must not keep files of the pinned tree: the last ship wins whole.
		for name, e := range shipped {
			if prev, ok := current.Modules[name]; ok && prev.Source == lockSourceRef && e.Source != lockSourceRef {
				releasing = append(releasing, name)
			}
		}
		sort.Strings(releasing)
	}

	// The saved plan is compared here: everything it records is resolved and
	// nothing has been written on the server yet.
	var plan DeployPlan
	if planning {
		mods, perr := planModules(opts, update, install, archived, branchMods, shipped)
		if perr != nil {
			return DeployResult{}, perr
		}
		ckpt := "off"
		if ckptEnabled {
			ckpt = ckptMethod
		}
		dest := ""
		if p.push {
			dest, _, _ = resolvePushDest(pushArgs{}, prof, opts.Cfg)
		}
		plan = newDeployPlan(ctx, opts.Root)
		plan.Target = planTarget{Name: fromName, SSHHost: sshHost, RemotePath: remotePath, DB: target.dbName, Stage: target.stage}
		plan.Run = planRun{
			Push: p.push, Git: gitActive, Checkpoint: ckpt, Test: runTests, I18nOverwrite: overwrite,
			Actions: !p.noActions, Lint: !p.noLint, DepCheck: !p.noDepCheck, Fetch: planFetch(p),
		}
		plan.Commits = append([]string{}, deployedShas...)
		plan.Modules = mods
		plan.BranchTip = gitTip
		plan.Dest = dest
		plan.TestModules = append([]string{}, testMods...)
		plan.DeployActions = actionsDigest(actions)
		plan.Lock = lockID
		plan.Dependencies = planDependencies(result.Dependencies)
	}
	if saved != nil {
		result.Plan = saved
		if changes := diffPlans(*saved, plan); len(changes) > 0 {
			for _, c := range changes {
				opts.log("ERROR", "plan", "changed", prof.DBName, c.fields()...)
			}
			result.PlanStale = changes
			return result, planStaleError(p.apply, *saved, len(changes))
		}
		opts.log("INFO", "plan", "plan matches", prof.DBName,
			[2]string{"age", humanAge(time.Since(saved.CreatedAt))})
	}

	// --push shares the deploy's already-resolved target: sync the resolved
	// modules' local code to the remote addons dir before the run. In dry-run
	// it prints the rsync itemization; on a real run a push failure aborts
	// before anything restarts.
	// The code rollback (Unit 126): before the first write, the run saves on
	// the server what it is about to overwrite, so any later failure can put
	// it back. --no-rollback-on-fail skips it: nobody would restore it.
	takeSnapshot := p.rollbackOnFail == nil || *p.rollbackOnFail
	var snap *codeSnapshot
	codeWritten := false

	// --push shares the deploy's already-resolved target: sync the resolved
	// modules' local code to the remote addons dir before the run. In dry-run
	// it prints the rsync itemization; on a real run a push failure aborts
	// before anything restarts.
	runPush := func(dryRun bool) error {
		if !p.push {
			return nil
		}
		archivedMods := make([]string, 0, len(archived))
		for m := range archived {
			archivedMods = append(archivedMods, m)
		}
		sort.Strings(archivedMods)
		pushOpts := PushOpts{
			Cfg: opts.Cfg, Root: opts.Root, Palette: opts.Palette,
			Log: opts.Log, StreamOut: opts.StreamOut, OnSync: opts.OnSync,
		}
		// Resolve an explicit destination (server/local [push], no picker in
		// a headless deploy). Empty → per-module auto-detect.
		destBase := ""
		if len(worktreeMods) > 0 || len(archivedMods) > 0 {
			if err := requireRsync(); err != nil {
				return err
			}
			if dest, source, mkdir := resolvePushDest(pushArgs{}, prof, opts.Cfg); dest != "" {
				resolved, derr := applyResolvedDest(ctx, rsc, pushOpts, dest, source, mkdir, dryRun)
				if derr != nil {
					return derr
				}
				destBase = resolved
			}
		}

		// Everything the run writes outside the deploy branch: the modules it
		// rsyncs and, on a git target, the pinned overlays it reverts.
		snapMods := append(append([]string(nil), worktreeMods...), archivedMods...)
		if gitActive {
			snapMods = append(snapMods, exceptModules(releasing, toStringSet(snapMods))...)
		}
		if takeSnapshot && len(snapMods) > 0 {
			if dryRun {
				opts.log("INFO", "plan", "code snapshot", prof.DBName,
					[2]string{"modules", strings.Join(snapMods, ",")})
			} else {
				dests := make(map[string]string, len(snapMods))
				for _, m := range snapMods {
					d, derr := moduleDestDir(ctx, remoteView{rsc: rsc}, pushOpts, destBase, m)
					if derr != nil {
						return derr
					}
					dests[m] = d
				}
				s, serr := createCodeSnapshot(ctx, rsc, dests, opts.Log)
				if serr != nil {
					return serr
				}
				snap = &s
			}
		}
		codeWritten = !dryRun

		for _, m := range releasing {
			opts.log("INFO", "push", "pin released", prof.DBName,
				[2]string{"module", m}, [2]string{"was", current.Modules[m].label()})
		}
		if gitActive && len(releasing) > 0 && !dryRun {
			absDir := absGitDir(remotePath, gitCfg.path)
			pinned := filterDirtyByModules(remoteDirtyEntries(ctx, rsc, absDir), releasing)
			if err := runRemoteClean(ctx, rsc, absDir, pinned); err != nil {
				return err
			}
		}
		// Git-deploy: the committed content advances the deploy branch with
		// identical SHAs (real object transfer). The pre-advance branch HEAD is
		// captured so a rollback can move it back.
		if gitActive && gitTip != "" {
			if err := gitDeployCommitted(ctx, opts, rsc, gitCfg, gitTip, dryRun, &gitPreCodeSHA); err != nil {
				return err
			}
		}
		push := func(mods []string, srcRoot string, del bool) error {
			if len(mods) == 0 {
				return nil
			}
			opts.log("INFO", "push", "syncing modules to remote", prof.DBName,
				[2]string{"modules", strings.Join(mods, ",")})
			_, dests, perr := pushModuleSet(ctx, rsc, pushOpts, mods, srcRoot, destBase, dryRun, del)
			setLockDests(shipped, dests)
			return perr
		}
		// The working tree keeps push's opt-in --delete, except over a module
		// pinned to a ref on a target without git, where the delete is what
		// drops the pinned tree's files.
		var plain, replacing []string
		for _, m := range worktreeMods {
			if !gitActive && slices.Contains(releasing, m) {
				replacing = append(replacing, m)
			} else {
				plain = append(plain, m)
			}
		}
		if err := push(plain, opts.Root, false); err != nil {
			return err
		}
		if err := push(replacing, opts.Root, true); err != nil {
			return err
		}
		// A commit's tree ships exactly: --delete removes what it deleted.
		return push(archivedMods, archiveDir, true)
	}

	if p.dryRun {
		if err := runPush(true); err != nil {
			return DeployResult{}, err
		}
		if ckptEnabled {
			opts.log("INFO", "plan", "checkpoint enabled", prof.DBName, [2]string{"method", ckptMethod})
		}
		if p.savePlan != "" {
			if err := writePlan(p.savePlan, plan); err != nil {
				return DeployResult{}, fmt.Errorf("save the plan: %w", err)
			}
			result.Plan = &plan
			opts.log("INFO", "plan", "plan saved", prof.DBName,
				[2]string{"path", p.savePlan}, [2]string{"modules", strconv.Itoa(len(plan.Modules))})
		}
		opts.log("INFO", "", "dry-run — nothing executed", prof.DBName)
		return result, nil
	}
	if len(result.Dependencies) > 0 && target.stage != "dev" && !p.force {
		if err := confirmDependencyRisk(opts.Palette, target.dbName, removedInUse(result.Dependencies)); err != nil {
			return DeployResult{}, err
		}
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

	// Every failure from the first code write on goes through fail, which
	// offers to put back the code (and the DB, once a checkpoint exists).
	var ckptEntry *config.CheckpointEntry
	appTouched := false
	fail := func(failErr error) (DeployResult, error) {
		if !codeWritten && ckptEntry == nil {
			return DeployResult{}, failErr
		}
		return handleDeployFailure(ctx, opts, rsc, p, deployFailure{
			checkpoint: ckptEntry,
			snapshot:   snap,
			codeSHA:    gitPreCodeSHA,
			appTouched: appTouched,
			rerunPush: func() error {
				if err := runActions(config.PhasePrePush); err != nil {
					return err
				}
				return runActions(config.PhasePostPush)
			},
		}, result, projectKey, targetKey, failErr)
	}

	if err := runPush(false); err != nil {
		return fail(fmt.Errorf("push failed: %w", err))
	}
	if p.push {
		updateDeployLock(ctx, rsc, opts.Log, func(l *DeployLock) {
			if shippedBase != nil {
				l.Base = shippedBase
			}
			l.record(shipped)
		})
	}
	if err := runActions(config.PhasePostPush); err != nil {
		return fail(err)
	}

	// Disk preflight runs before any container stop, so a doomed deploy never
	// takes the service down: if the DB won't fit alongside its checkpoint,
	// abort now with both numbers named.
	if ckptEnabled {
		if err := checkpointPreflight(ctx, rsc, ckptMethod, opts.Log); err != nil {
			return fail(err)
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
		return fail(err)
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
	appTouched = true
	if err := step("stop", stopCmd); err != nil {
		return fail(err)
	}

	// Checkpoint the DB with the app stopped (no sessions on the source) but the
	// DB container still up. A creation failure aborts before the run so nothing
	// is half-migrated.
	if ckptEnabled {
		entry, info, cerr := createCheckpoint(ctx, rsc, ckptMethod, deployedShas, opts.StreamOut, opts.Log)
		if cerr != nil {
			return fail(cerr)
		}
		ckptEntry = &entry
		result.Checkpoint = &info
	}

	if err := step("up -d", remoteComposeCmd(remotePath, target.composeCmd, "up", "-d")); err != nil {
		return fail(err)
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
		return fail(deployRunError(runErr))
	}
	// The remote run landed: every resolved module deployed OK.
	for i := range result.Modules {
		result.Modules[i].OK = true
	}
	if p.push {
		updateDeployLock(ctx, rsc, opts.Log, func(l *DeployLock) { l.markVerified(shipped) })
	}
	if gitActive && gitTip != "" {
		result.CodeSHA = gitTip
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
	// The code snapshot rides along with it so a later --rollback restores
	// DB and code together; without a checkpoint nothing will ever use it.
	if ckptEntry != nil {
		ckptEntry.CodeSHA = gitPreCodeSHA
		if snap != nil {
			ckptEntry.CodeSnapshot = snap.Name
		}
		if err := config.AddCheckpoint(projectKey, targetKey, *ckptEntry); err == nil {
			pruneCheckpoints(ctx, rsc, projectKey, targetKey, ckptPolicy.keep, opts.Log)
		}
	} else if snap != nil {
		if err := destroyCodeSnapshot(ctx, rsc, snap.Name); err != nil {
			opts.log("WARNING", "snapshot", "could not remove the code snapshot", prof.DBName,
				[2]string{"name", snap.Name}, [2]string{"err", err.Error()})
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

// rollbackDecision resolves whether a failed deploy rolls back WITHOUT a
// prompt. Returns (decided, doRollback): decided=false means the caller must
// fall back to the interactive confirm (the TTY case). An explicit
// --rollback-on-fail/--no-rollback-on-fail wins outright; else --force means
// roll back; else a TTY defers to the confirm; else (headless, no flags) the
// default is to roll back. Pure — testable without a real terminal.
func rollbackDecision(p deployArgs, tty bool) (decided, doRollback bool) {
	switch {
	case p.rollbackOnFail != nil:
		return true, *p.rollbackOnFail
	case p.force:
		return true, true
	case tty:
		return false, false // defer to confirmRollback
	default:
		return true, true // headless default: roll back
	}
}

// deployFailure is what a failed deploy leaves to undo.
type deployFailure struct {
	checkpoint *config.CheckpointEntry // the DB copy the run took; nil when none
	snapshot   *codeSnapshot           // the module directories it overwrote; nil when none
	codeSHA    string                  // the deploy branch before it moved; "" when it did not
	appTouched bool                    // the run stopped or restarted the app
	rerunPush  func() error            // the push actions, to rebuild from restored code
}

// restorePoint is the checkpoint entry that keeps what was not restored
// restorable with `deploy --rollback`: the DB copy, the code, or both.
func (f deployFailure) restorePoint(withDB, withCode bool) (config.CheckpointEntry, bool) {
	var entry config.CheckpointEntry
	switch {
	case withDB && f.checkpoint != nil:
		entry = *f.checkpoint
	case withCode && (f.snapshot != nil || f.codeSHA != ""):
		entry = config.CheckpointEntry{Method: codeCheckpointMethod, CreatedAt: time.Now()}
		if f.snapshot != nil {
			entry.Name = f.snapshot.Name
		} else {
			entry.Name = "code_" + shortSHA(f.codeSHA)
		}
	default:
		return config.CheckpointEntry{}, false
	}
	if withCode {
		entry.CodeSHA = f.codeSHA
		if f.snapshot != nil {
			entry.CodeSnapshot = f.snapshot.Name
		}
	}
	return entry, true
}

// handleDeployFailure undoes a deploy that failed after it wrote code or took
// a checkpoint. In an interactive session it asks first (so the operator can
// inspect the broken state); headless (--force or no TTY) it rolls back, and
// --rollback-on-fail / --no-rollback-on-fail fix the choice either way.
//
// A rollback stops the app when the run had touched it, restores the DB from
// the checkpoint when there is one, puts the code back (the deploy branch, then
// the snapshot of every module directory the run overwrote), re-runs the push
// actions so an image-built target rebuilds from the restored code, and
// starts the app again. Whatever is not restored is recorded as a checkpoint
// so `deploy --rollback` can finish the job. The deploy's commits are never
// marked deployed. It returns the result (RolledBack set when restored) with
// failErr, so a headless caller like watch can read the outcome.
func handleDeployFailure(ctx context.Context, opts DeployOpts, rsc remoteShellContext, p deployArgs, f deployFailure, result DeployResult, projectKey, targetKey string, failErr error) (DeployResult, error) {
	db := rsc.prof.DBName
	if f.checkpoint == nil && f.snapshot == nil && f.codeSHA == "" {
		return result, failErr
	}
	decided, doRollback := rollbackDecision(p, stdinIsTTY())
	if !decided {
		doRollback = confirmRollback(opts.Palette, db, f.checkpoint)
	}
	if !doRollback {
		if entry, ok := f.restorePoint(true, true); ok {
			_ = config.AddCheckpoint(projectKey, targetKey, entry)
			opts.log("WARNING", "rollback", "skipped — restore later with deploy --rollback", db,
				[2]string{"checkpoint", entry.Name})
		}
		return result, failErr
	}

	if f.appTouched || f.checkpoint != nil {
		// The restore's psql/pg_restore needs the DB container up: stop only
		// the app.
		opts.log("INFO", "rollback", "stopping app before restore", db)
		_ = runSSHStream(ctx, rsc.sshHost, remoteStopApp(rsc), nil, opts.StreamOut)
	}

	consumed := false
	if f.checkpoint != nil {
		// On-failure auto-rollback keeps consuming the just-made checkpoint
		// (its purpose is served the moment the failed deploy is reverted).
		c, rerr := restoreCheckpoint(ctx, rsc, *f.checkpoint, true, opts.StreamOut, opts.Log)
		if rerr != nil {
			entry, _ := f.restorePoint(true, true)
			_ = config.AddCheckpoint(projectKey, targetKey, entry)
			opts.log("ERROR", "rollback", "rollback failed — checkpoint preserved", db,
				[2]string{"checkpoint", entry.Name}, [2]string{"err", rerr.Error()})
			return result, fmt.Errorf("deploy failed and rollback failed: %v (deploy error: %w)", rerr, failErr)
		}
		consumed = c
	} else {
		opts.log("WARNING", "rollback", "no database checkpoint — restoring the code only", db)
	}

	codeRestored := restoreDeployedCode(ctx, opts, rsc, f.codeSHA, f.snapshot)
	if f.snapshot != nil || f.codeSHA != "" {
		if err := f.rerunPush(); err != nil {
			opts.log("ERROR", "rollback", "push actions failed on the restored code", db,
				[2]string{"err", err.Error()})
		}
	}
	_ = runSSHStream(ctx, rsc.sshHost, remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "up", "-d"), nil, opts.StreamOut)

	// A dump survives its own restore, so it stays recorded for a possible
	// re-rollback; a db-method copy was consumed by the rename. Code that did
	// not come back stays restorable too.
	if entry, ok := f.restorePoint(!consumed, !codeRestored); ok {
		_ = config.AddCheckpoint(projectKey, targetKey, entry)
	}
	if f.snapshot != nil && codeRestored {
		_ = destroyCodeSnapshot(ctx, rsc, f.snapshot.Name)
	}
	result.RolledBack = true
	opts.log("INFO", "rollback", "rolled back — commits not marked deployed", db,
		[2]string{"database", strconv.FormatBool(f.checkpoint != nil)},
		[2]string{"code", strconv.FormatBool(codeRestored)})
	return result, failErr
}

// restoreDeployedCode moves the deploy branch back to codeSHA (git targets)
// and extracts the snapshot over the module directories, reporting whether
// all of it came back. Failures are logged, never returned: the caller is
// already handling a failed deploy and must not lose that error.
func restoreDeployedCode(ctx context.Context, opts DeployOpts, rsc remoteShellContext, codeSHA string, snap *codeSnapshot) bool {
	ok := true
	if codeSHA != "" {
		g := resolveGitDeploy(opts.Cfg, rsc.fromName, rsc.sshHost, rsc.remotePath)
		if err := gitRestoreCode(ctx, rsc, g, codeSHA, opts.Log); err != nil {
			opts.log("ERROR", "rollback", "deploy branch restore failed", rsc.prof.DBName,
				[2]string{"sha", shortSHA(codeSHA)}, [2]string{"err", err.Error()})
			ok = false
		}
	}
	if snap != nil {
		if err := restoreCodeSnapshot(ctx, rsc, *snap, opts.Log); err != nil {
			opts.log("ERROR", "rollback", "code snapshot restore failed", rsc.prof.DBName,
				[2]string{"snapshot", snap.Name}, [2]string{"err", err.Error()})
			ok = false
		}
	}
	return ok
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
	// Keep the checkpoint by default (restore leaves it intact, so the point
	// stays restorable); --consume-checkpoint opts into the cheaper rename that
	// destroys it. The "dump" method preserves its file regardless. A "code"
	// entry has no database part.
	consumed := false
	if chosen.Method != codeCheckpointMethod {
		c, rerr := restoreCheckpoint(ctx, rsc, chosen, p.consumeCheckpoint, opts.StreamOut, opts.Log)
		if rerr != nil {
			return DeployResult{}, fmt.Errorf("restore failed: %w", rerr)
		}
		consumed = c
	}
	// Put back the code the checkpoint recorded — the deploy branch's
	// pre-deploy hash and the snapshot of the module directories — so a
	// post-hoc rollback returns DB and code together.
	var snap *codeSnapshot
	if chosen.CodeSnapshot != "" {
		s, serr := readCodeSnapshot(ctx, rsc, chosen.CodeSnapshot)
		if serr != nil {
			opts.log("ERROR", "rollback", "code snapshot unreadable — code not restored", rsc.prof.DBName,
				[2]string{"snapshot", chosen.CodeSnapshot}, [2]string{"err", serr.Error()})
		} else {
			snap = &s
		}
	}
	if chosen.CodeSHA != "" || snap != nil {
		restoreDeployedCode(ctx, opts, rsc, chosen.CodeSHA, snap)
		var modules []string
		if snap != nil {
			for m := range snap.Dests {
				modules = append(modules, m)
			}
			sort.Strings(modules)
		}
		if err := rerunPushActions(ctx, opts, rsc, p.noActions, modules); err != nil {
			opts.log("ERROR", "rollback", "push actions failed on the restored code", rsc.prof.DBName,
				[2]string{"err", err.Error()})
		}
	}
	if err := runSSHStream(ctx, rsc.sshHost, remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "up", "-d"), nil, opts.StreamOut); err != nil {
		return DeployResult{}, fmt.Errorf("up -d failed: %w", err)
	}

	// Un-mark the checkpoint's commits so they can be corrected and redeployed;
	// drop a consumed (--consume-checkpoint) checkpoint from the store. When it
	// was preserved (the default), the entry stays so the point is restorable
	// again.
	_ = config.UnmarkDeployed(projectKey, targetKey, chosen.DeploySHAs)
	disposition := "preserved"
	if consumed {
		_ = config.RemoveCheckpoint(projectKey, targetKey, chosen.Name)
		if chosen.CodeSnapshot != "" {
			_ = destroyCodeSnapshot(ctx, rsc, chosen.CodeSnapshot)
		}
		disposition = "consumed"
	}
	opts.log("INFO", "", "rollback complete", rsc.prof.DBName,
		[2]string{"checkpoint", chosen.Name}, [2]string{"disposition", disposition},
		[2]string{"unmarked", strconv.Itoa(len(chosen.DeploySHAs))})
	return DeployResult{
		Target:     rsc.fromName,
		DB:         rsc.prof.DBName,
		RolledBack: true,
		JSON:       p.jsonOut,
	}, nil
}

// runDeployRestoreCode is the standalone `deploy --restore-code [<sha>]`: move a
// git-deploy target's deploy branch to an already-deployed hash and restart
// Odoo, without touching the DB or a checkpoint. With no SHA it opens a picker
// over the remote deploy branch's history (the states the server has been in);
// an explicit hash must already exist on the server.
func runDeployRestoreCode(ctx context.Context, opts DeployOpts, p deployArgs) (DeployResult, error) {
	logFn := func(level, sub, msg, db string, fields ...[2]string) { opts.log(level, sub, msg, db, fields...) }
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, logFn)
	if err != nil {
		return DeployResult{}, err
	}
	g := resolveGitDeploy(opts.Cfg, rsc.fromName, rsc.sshHost, rsc.remotePath)
	if !g.enabled {
		return DeployResult{}, fmt.Errorf("%w: --restore-code needs a git-deploy target (set git_deploy on it)", ErrUsage)
	}
	absDir := absGitDir(rsc.remotePath, g.path)

	sha := p.restoreCode
	if sha == "" {
		// Interactive: pick from the remote deploy branch's recent history.
		picked, perr := pickRestoreCommit(ctx, opts, rsc, g, absDir, p.limit)
		if perr != nil {
			return DeployResult{}, perr
		}
		sha = picked
	} else if !gitRemoteHasCommit(ctx, rsc, absDir, sha) {
		return DeployResult{}, fmt.Errorf("%w: commit %s is not on the remote — only previously deployed hashes can be restored",
			ErrUsage, shortSHA(sha))
	}
	if err := confirmRemoteProd(opts.Palette, "restore-code", rsc, opts.Args); err != nil {
		return DeployResult{}, err
	}
	if err := gitRestoreCode(ctx, rsc, g, sha, opts.Log); err != nil {
		return DeployResult{}, err
	}
	opts.log("INFO", "compose", "restart", rsc.prof.DBName)
	if err := runSSHStream(ctx, rsc.sshHost,
		remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "restart", rsc.target.odooContainer),
		nil, opts.StreamOut); err != nil {
		return DeployResult{}, fmt.Errorf("restart failed: %w", err)
	}
	opts.log("INFO", "", "code restored", rsc.prof.DBName,
		[2]string{"branch", g.branch}, [2]string{"sha", shortSHA(sha)})
	return DeployResult{Target: rsc.fromName, DB: rsc.prof.DBName, CodeSHA: sha, JSON: p.jsonOut}, nil
}

// pickRestoreCommit opens a single-select picker over the remote deploy
// branch's recent commits (newest first) — the code states the server has been
// in — and returns the chosen SHA. TTY-guarded: a headless caller must pass the
// SHA explicitly.
func pickRestoreCommit(ctx context.Context, opts DeployOpts, rsc remoteShellContext, g gitDeployConfig, absDir string, limit int) (string, error) {
	if err := requireTTY("pass the SHA: deploy --restore-code <sha>"); err != nil {
		return "", err
	}
	commits, err := remoteBranchCommits(ctx, rsc, absDir, g.branch, limit)
	if err != nil {
		return "", err
	}
	if len(commits) == 0 {
		return "", fmt.Errorf("%w: no commits on remote branch %s to restore to", ErrUsage, g.branch)
	}
	labels := make([]string, len(commits))
	byLabel := make(map[string]string, len(commits))
	for i, c := range commits {
		lbl := c.short() + "  " + c.subject
		labels[i] = lbl
		byLabel[lbl] = c.sha
	}
	pick, err := PickOne("Restore code on branch "+g.branch, labels, opts.Palette)
	if err != nil {
		return "", err
	}
	return byLabel[pick], nil
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
		// No binding to mark here — this fires precisely when the directory
		// has none, so the picker opens on the first row.
		t, perr := pickConnectTarget(cfg.ConnectTargets, palette,
			"Select connect target", "", log, nil)
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
func pathsTouchI18n(moduleDir string, paths []string) bool {
	prefix := moduleDir + "/i18n/"
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
	if m == nil || !hasAddon(root, m[1]) {
		return ""
	}
	return m[1]
}

// modulesFromPaths maps changed paths to the distinct addons they live in,
// sorted.
func modulesFromPaths(root string, paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		mod, _ := addonFromPath(root, p)
		if mod == "" || seen[mod] {
			continue
		}
		seen[mod] = true
		out = append(out, mod)
	}
	sort.Strings(out)
	return out
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
func remoteModuleStates(ctx context.Context, sshHost, remotePath string, t connectTarget, pgUser, db string) (states, versions map[string]string, err error) {
	if pgUser == "" {
		pgUser = "odoo"
	}
	q := "SELECT name, state, COALESCE(latest_version, '') FROM ir_module_module"
	argv := odoo.Cmd{"psql", "-U", pgUser, "-d", db, "-At", "-c", q}
	out, err := runRemoteDBCmd(ctx, runSSH, sshHost, remotePath, t, argv)
	if err != nil {
		return nil, nil, err
	}
	states, versions = map[string]string{}, map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) < 2 || parts[0] == "" {
			continue
		}
		states[parts[0]] = parts[1]
		if len(parts) == 3 && parts[2] != "" {
			versions[parts[0]] = parts[2]
		}
	}
	return states, versions, nil
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
	dirs := map[string]string{}
	var order []string
	for _, p := range paths {
		mod, dir := addonFromPath(root, p)
		if mod == "" {
			continue
		}
		if _, ok := byMod[mod]; !ok {
			order = append(order, mod)
			dirs[mod] = dir
		}
		byMod[mod] = append(byMod[mod], p)
	}
	sort.Strings(order)
	out := make([]dirtyModule, 0, len(order))
	for _, m := range order {
		out = append(out, dirtyModule{name: m, dir: dirs[m], paths: byMod[m]})
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
