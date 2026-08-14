package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// runDeploySetCode is `deploy --set-code <ref>`: move a git-deploy target's
// code to any ref — a branch, a tag, a hash, a remote-tracking ref — instead of
// only to a hash the server has already run (`--restore-code`).
//
// It is the counterpart of the fast-forward gate in gitAdvance. A deploy must
// never leave the line it is on by accident; re-baselining that line is a
// deliberate, confirmed act, and this is the command that performs it. No DB,
// no checkpoint, no lint: only the code moves.
func runDeploySetCode(ctx context.Context, opts DeployOpts, p deployArgs) (DeployResult, error) {
	// The ref is resolved LOCALLY and first, so a typo costs zero remote calls
	// — and so a branch that lives only on this machine deploys exactly like
	// one on origin: the objects travel over SSH, not through a git host.
	res, err := resolveLocalRef(ctx, opts.log, opts.Root, p.setCode, p.fetch, p.noFetch)
	if err != nil {
		return DeployResult{}, err
	}
	if res.fetched != "" {
		opts.log("INFO", "git", "fetched", "", [2]string{"remote", res.fetched})
	}
	opts.log("INFO", "git", "ref resolved", "",
		[2]string{"ref", res.ref}, [2]string{"sha", res.short()})

	logFn := func(level, sub, msg, db string, fields ...[2]string) { opts.log(level, sub, msg, db, fields...) }
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, logFn)
	if err != nil {
		return DeployResult{}, err
	}
	g := resolveGitDeploy(opts.Cfg, rsc.fromName, rsc.sshHost, rsc.remotePath)
	if !g.enabled {
		return DeployResult{}, fmt.Errorf("%w: --set-code needs a git-deploy target (set git_deploy on it)", ErrUsage)
	}

	// The local reset is planned before any remote call and applied before the
	// remote move, so a local worktree that cannot be re-based aborts the run
	// while the server is still untouched.
	var localPlan *resetPlan
	var localPath string
	if p.withLocal {
		localPlan, localPath, err = planLocalReset(ctx, opts, res)
		if err != nil {
			return DeployResult{}, err
		}
	}

	plan, err := planGitSetCode(ctx, opts, rsc, g, p.keepOverlay)
	if err != nil {
		return DeployResult{}, err
	}
	direction := describeMove(ctx, opts.Root, plan.prev, res.sha)
	// Nothing to do only when the branch is already there AND there is no
	// overlay to sweep and no local branch to bring along — an unchanged SHA
	// with a dirty overlay is still a checkout that does not match the ref.
	if plan.prev == res.sha && len(plan.cleaned) == 0 && !p.withLocal {
		opts.log("INFO", "git", "nothing to set — the deploy branch is already at that ref", rsc.prof.DBName,
			[2]string{"branch", g.branch}, [2]string{"sha", res.short()})
		return DeployResult{Target: rsc.fromName, DB: rsc.prof.DBName, CodeSHA: res.sha, Ref: res.ref, JSON: p.jsonOut}, nil
	}

	opts.log("INFO", "git", "setting code", rsc.prof.DBName,
		[2]string{"branch", g.branch}, [2]string{"ref", res.ref},
		[2]string{"sha", res.short()}, [2]string{"prev", shortSHA(plan.prev)},
		[2]string{"move", direction})
	if len(plan.cleaned) > 0 && opts.OnSync != nil {
		opts.OnSync(dirtyEntriesToChanges(plan.cleaned))
	}

	if p.dryRun {
		fields := [][2]string{
			{"branch", g.branch}, {"sha", res.short()}, {"cleaned", strconv.Itoa(len(plan.cleaned))},
		}
		if localPlan != nil {
			fields = append(fields, [2]string{"local_files", strconv.Itoa(len(localPlan.changes))})
		}
		opts.log("INFO", "git", "dry-run — nothing moved", rsc.prof.DBName, fields...)
		return DeployResult{
			Target: rsc.fromName, DB: rsc.prof.DBName, Planned: true,
			CodeSHA: res.sha, Ref: res.ref, PreviousCodeSHA: plan.prev,
			Cleaned: len(plan.cleaned), JSON: p.jsonOut,
		}, nil
	}

	if err := confirmRemoteProd(opts.Palette, "set-code", rsc, opts.Args); err != nil {
		return DeployResult{}, err
	}
	if !p.force {
		if err := confirmSetCode(opts.Palette, rsc.prof.DBName, g.branch, res, plan, direction); err != nil {
			return DeployResult{}, err
		}
	}

	if localPlan != nil {
		if err := applyBranchReset(ctx, localPath, *localPlan, false); err != nil {
			return DeployResult{}, err
		}
		opts.log("INFO", "promote", "local branch reset", "",
			[2]string{"branch", localPlan.branch}, [2]string{"base", res.ref},
			[2]string{"sha", res.short()}, [2]string{"prev", shortSHA(localPlan.prevSHA)})
	}

	if err := applyGitSetCode(ctx, opts, rsc, g, plan, res.sha, res.ref); err != nil {
		return DeployResult{}, err
	}

	opts.log("INFO", "compose", "restart", rsc.prof.DBName)
	if err := runSSHStream(ctx, rsc.sshHost,
		remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "restart", rsc.target.odooContainer),
		nil, opts.StreamOut); err != nil {
		return DeployResult{}, fmt.Errorf("restart failed: %w", err)
	}

	// The recorded deploy history describes a line this target no longer has.
	// Re-seeding it with the new tip keeps `deploy --auto` honest: left stale,
	// it would skip commits that were never shipped here.
	projectKey := config.ProjectKey(opts.Root)
	targetKey := config.DeployTargetKey(rsc.sshHost, rsc.remotePath)
	if err := config.ResetDeployedSHAs(projectKey, targetKey, []string{res.sha}); err != nil {
		opts.log("WARNING", "", "could not re-baseline the deploy history", rsc.prof.DBName,
			[2]string{"reason", err.Error()})
	}

	opts.log("INFO", "git", "code set", rsc.prof.DBName,
		[2]string{"branch", g.branch}, [2]string{"ref", res.ref}, [2]string{"sha", res.short()},
		[2]string{"prev", shortSHA(plan.prev)}, [2]string{"cleaned", strconv.Itoa(len(plan.cleaned))})
	if hasCheckpoints(opts, rsc) {
		opts.log("WARNING", "", "existing checkpoints restore the code to the line before this move", rsc.prof.DBName)
	}
	return DeployResult{
		Target: rsc.fromName, DB: rsc.prof.DBName,
		CodeSHA: res.sha, Ref: res.ref, PreviousCodeSHA: plan.prev,
		Cleaned: len(plan.cleaned), JSON: p.jsonOut,
	}, nil
}

// planLocalReset prepares the `--with-local` half: the [promote] branch's
// worktree re-based onto the same ref. It never discards — uncommitted work
// that the move would clobber aborts the whole run, and getting rid of it is an
// explicit `promote --reset --discard`, not a side effect of a deploy flag.
func planLocalReset(ctx context.Context, opts DeployOpts, res refResolution) (*resetPlan, string, error) {
	branch := ""
	if opts.Cfg != nil {
		branch = opts.Cfg.PromoteBranch
	}
	if branch == "" {
		return nil, "", fmt.Errorf("%w: --with-local needs a promote branch — set one with `promote --set-branch <name>`", ErrUsage)
	}
	srcRoot, err := gitToplevel(ctx, opts.Root)
	if err != nil {
		return nil, "", err
	}
	wts, err := gitWorktrees(ctx, srcRoot)
	if err != nil {
		return nil, "", fmt.Errorf("list worktrees: %w", err)
	}
	w, ok := worktreeForBranch(wts, branch)
	if !ok {
		return nil, "", fmt.Errorf("%w: no worktree has %q checked out — create one with `git worktree add <path> %s` or drop --with-local",
			ErrUsage, branch, branch)
	}
	plan, err := planBranchReset(ctx, w.path, branch, res.ref, res.sha)
	if err != nil {
		return nil, "", err
	}
	if len(plan.collisions) > 0 {
		return nil, "", fmt.Errorf("%w: %d uncommitted file(s) on %s would be lost by the local reset: %s — commit or promote them first, or run `promote --reset --discard` yourself",
			ErrPromoteConflict, len(plan.collisions), branch, strings.Join(plan.collisions, ", "))
	}
	opts.log("INFO", "promote", "local reset planned", "",
		[2]string{"branch", branch}, [2]string{"worktree", w.path},
		[2]string{"files", strconv.Itoa(len(plan.changes))}, [2]string{"kept", strconv.Itoa(plan.kept)})
	return &plan, w.path, nil
}

// hasCheckpoints reports whether the target has any stored checkpoint, so a
// set-code run can warn that restoring one returns the code to the pre-move
// line. Best-effort: an unreadable store simply means no warning.
func hasCheckpoints(opts DeployOpts, rsc remoteShellContext) bool {
	if opts.Cfg == nil {
		return false
	}
	entries := config.LoadCheckpoints(config.ProjectKey(opts.Root),
		config.DeployTargetKey(rsc.sshHost, rsc.remotePath))
	return len(entries) > 0
}

// confirmSetCode is the destructive confirm for a real `--set-code`: it names
// the direction of the move (a rewind and a jump to another line read very
// differently) and the overlay files that go with it. Bypassed by --force and
// TTY-guarded otherwise.
func confirmSetCode(palette theme.Palette, db, branch string, res refResolution, plan setCodePlan, direction string) error {
	if err := requireTTY("pass --force to set the code without a prompt"); err != nil {
		return err
	}
	danger := lipgloss.NewStyle().Foreground(palette.Error).Bold(true)
	desc := []string{
		fmt.Sprintf("%s: %s → %s (%s)", branch, shortSHA(plan.prev), res.short(), direction),
	}
	switch {
	case len(plan.cleaned) > 0:
		desc = append(desc, fmt.Sprintf("%d overlay file(s) on the server will be reverted or removed.", len(plan.cleaned)))
	default:
		desc = append(desc, "The dirty overlay is preserved (--keep-overlay).")
	}
	desc = append(desc, "The database is not touched and no checkpoint is taken.")
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  Set " + db + "'s code to " + danger.Render(res.ref)).
			Description(strings.Join(desc, "\n")).
			Affirmative("Set code").
			Negative("Cancel").
			Value(&confirmed),
	)).
		WithTheme(BuildHuhTheme(palette)).
		WithInput(os.Stdin).
		WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return err
	}
	if !confirmed {
		return ErrCancelled
	}
	return nil
}
