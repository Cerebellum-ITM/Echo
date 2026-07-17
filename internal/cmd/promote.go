package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// ErrPromoteConflict is returned when a dirty patch or a cherry-pick collides
// with what the destination branch already has. The destination is left
// exactly as it was (nothing half-applied).
var ErrPromoteConflict = errors.New("promote conflict")

// PromoteOpts configures a `promote` run. Root is the invocation directory
// (cwd); the git worktree is resolved from it — promote never needs a compose
// project. Mirrors PushOpts for the logger/stream/tree plumbing.
type PromoteOpts struct {
	Cfg       *config.Config
	Root      string
	Args      []string
	Palette   theme.Palette
	Log       func(level, sub, msg, db string, fields ...[2]string)
	StreamOut func(string)
	OnSync    func(changes []FileChange)
}

func (o PromoteOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

// promoteArgs is the parsed shape of the promote input.
type promoteArgs struct {
	dirty      bool
	folders    []string // dirty-mode module dirs (positionals when --dirty)
	branch     string   // commits-mode source branch (positional)
	commits    []string // --commits shas (requires branch)
	to         string   // --to destination branch override
	setBranch  string   // --set-branch (config-only)
	createDest string   // --create-dest worktree path
	dryRun     bool
	force      bool
}

// parsePromoteArgs extracts the flags and interprets the positionals by mode:
// with --dirty they are module folders, otherwise the single one is the
// source branch. --set-branch is standalone.
func parsePromoteArgs(args []string) (promoteArgs, error) {
	out := promoteArgs{}
	var positionals []string
	value := func(i int, name string) (string, int, error) {
		if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
			return "", i, fmt.Errorf("%w: %s needs a value", ErrUsage, name)
		}
		return args[i+1], i + 1, nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var err error
		switch {
		case a == "--dirty":
			out.dirty = true
		case a == "--dry-run":
			out.dryRun = true
		case a == "--force":
			out.force = true
		case a == "--commits":
			var v string
			if v, i, err = value(i, "--commits"); err != nil {
				return promoteArgs{}, err
			}
			out.commits = splitCSV(v)
		case strings.HasPrefix(a, "--commits="):
			out.commits = splitCSV(strings.TrimPrefix(a, "--commits="))
		case a == "--to":
			if out.to, i, err = value(i, "--to"); err != nil {
				return promoteArgs{}, err
			}
		case strings.HasPrefix(a, "--to="):
			out.to = strings.TrimPrefix(a, "--to=")
		case a == "--set-branch":
			if out.setBranch, i, err = value(i, "--set-branch"); err != nil {
				return promoteArgs{}, err
			}
		case strings.HasPrefix(a, "--set-branch="):
			out.setBranch = strings.TrimPrefix(a, "--set-branch=")
		case a == "--create-dest":
			if out.createDest, i, err = value(i, "--create-dest"); err != nil {
				return promoteArgs{}, err
			}
		case strings.HasPrefix(a, "--create-dest="):
			out.createDest = strings.TrimPrefix(a, "--create-dest=")
		case strings.HasPrefix(a, "-"):
			return promoteArgs{}, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			positionals = append(positionals, a)
		}
	}

	if out.setBranch != "" {
		if out.dirty || out.to != "" || out.createDest != "" ||
			len(out.commits) > 0 || len(positionals) > 0 {
			return promoteArgs{}, fmt.Errorf("%w: --set-branch takes no other arguments", ErrUsage)
		}
		return out, nil
	}
	if out.dirty {
		if len(out.commits) > 0 {
			return promoteArgs{}, fmt.Errorf("%w: --commits cannot be combined with --dirty", ErrUsage)
		}
		out.folders = positionals
		return out, nil
	}
	switch len(positionals) {
	case 0:
		if len(out.commits) > 0 {
			return promoteArgs{}, fmt.Errorf("%w: --commits requires a source branch", ErrUsage)
		}
	case 1:
		out.branch = positionals[0]
	default:
		return promoteArgs{}, fmt.Errorf("%w: promote takes a single source branch (got %d)", ErrUsage, len(positionals))
	}
	return out, nil
}

// promoteSummary is what a mode run reports back for the summary log + record.
type promoteSummary struct {
	mode   string // "dirty" | "commits"
	what   string // module list or commit shas, comma-joined
	count  int    // files (dirty) or commits
	dryRun bool
}

// RunPromote funnels the current worktree's changes onto the accumulation
// branch (the single branch the instance deploys from), purely locally: no
// SSH, no push, no deploy, no auto-commit. Dirty changes land as an unstaged
// patch; committed changes land via cherry-pick.
func RunPromote(ctx context.Context, opts PromoteOpts) error {
	started := time.Now()
	p, err := parsePromoteArgs(opts.Args)
	if err != nil {
		return err
	}

	// --set-branch is config-only: persist [promote] branch and exit.
	if p.setBranch != "" {
		if err := config.SavePromoteBranch(p.setBranch); err != nil {
			return fmt.Errorf("save promote branch: %w", err)
		}
		if opts.Cfg != nil {
			opts.Cfg.PromoteBranch = p.setBranch
		}
		opts.log("INFO", "", "promote branch set", "", [2]string{"branch", p.setBranch})
		return nil
	}

	srcRoot, err := gitToplevel(ctx, opts.Root)
	if err != nil {
		return err
	}
	wts, err := gitWorktrees(ctx, srcRoot)
	if err != nil {
		return fmt.Errorf("list worktrees: %w", err)
	}

	dest, destBranch, err := resolveDest(ctx, opts, p, srcRoot, wts)
	if err != nil {
		return err
	}
	if sameWorktree(dest.path, srcRoot) {
		return fmt.Errorf("%w: source and destination are the same worktree (%s) — run promote from the feature worktree, not from %q",
			ErrUsage, srcRoot, destBranch)
	}

	mode, srcBranch, err := resolveMode(ctx, opts, p, srcRoot, wts, destBranch)
	if err != nil {
		return err
	}

	sourceLabel := "dirty (this worktree)"
	if mode == "commits" {
		sourceLabel = srcBranch
	}
	opts.log("INFO", "", "promoting", "",
		[2]string{"source", sourceLabel},
		[2]string{"dest", destBranch},
		[2]string{"dest_path", dest.path})

	var sum promoteSummary
	if mode == "dirty" {
		sum, err = promoteDirty(ctx, opts, p, srcRoot, dest, destBranch)
	} else {
		sum, err = promoteCommits(ctx, opts, p, srcRoot, dest, destBranch, srcBranch)
	}
	if err != nil {
		return err
	}
	if !sum.dryRun && sum.count > 0 {
		recordPromoteLog(opts, srcRoot, destBranch, sum, started)
	}
	return nil
}

// promoteDirty applies the selected modules' dirty patch into the destination
// worktree, leaving it dirty.
func promoteDirty(ctx context.Context, opts PromoteOpts, p promoteArgs, srcRoot string, dest gitWorktree, destBranch string) (promoteSummary, error) {
	dm, err := gitDirtyModules(ctx, srcRoot)
	if err != nil {
		return promoteSummary{}, fmt.Errorf("scan dirty modules: %w", err)
	}
	if len(dm) == 0 {
		opts.log("INFO", "", "nothing to promote — no dirty modules", "")
		return promoteSummary{mode: "dirty"}, nil
	}
	selected, err := selectDirtyModules(opts, p, dm)
	if err != nil {
		return promoteSummary{}, err
	}
	if len(selected) == 0 {
		opts.log("INFO", "", "nothing to promote — no modules selected", "")
		return promoteSummary{mode: "dirty"}, nil
	}

	// Files that already carry uncommitted work on the destination will be
	// overwritten by the source's version — surface that before it happens.
	destDirty := destDirtyPaths(ctx, dest.path, selected)

	changes, err := applyDirty(ctx, srcRoot, dest.path, selected, p.dryRun)
	if err != nil {
		return promoteSummary{}, err
	}
	if opts.OnSync != nil {
		opts.OnSync(changes)
	}
	var clobbered []string
	for _, c := range changes {
		if c.Op != "deleted" && destDirty[c.Path] {
			clobbered = append(clobbered, c.Path)
		}
	}
	if len(clobbered) > 0 {
		msg := "overwrote files that had uncommitted changes on the destination"
		if p.dryRun {
			msg = "would overwrite files that have uncommitted changes on the destination"
		}
		opts.log("WARNING", "", msg, "", [2]string{"files", strings.Join(clobbered, ",")})
	}
	verb := "promote complete"
	if p.dryRun {
		verb = "dry-run — nothing applied"
	}
	newN, chgN, delN := countChanges(changes)
	fields := [][2]string{
		{"dest", destBranch}, {"modules", itoa(len(selected))},
		{"files", itoa(len(changes))}, {"new", itoa(newN)}, {"changed", itoa(chgN)},
	}
	if delN > 0 {
		fields = append(fields, [2]string{"deleted", itoa(delN)})
	}
	opts.log("INFO", "", verb, "", fields...)
	return promoteSummary{mode: "dirty", what: strings.Join(selected, ","), count: len(changes), dryRun: p.dryRun}, nil
}

// promoteCommits cherry-picks the selected commits (not already on the
// destination) onto the destination worktree.
func promoteCommits(ctx context.Context, opts PromoteOpts, p promoteArgs, srcRoot string, dest gitWorktree, destBranch, srcBranch string) (promoteSummary, error) {
	eligible, err := eligibleCommits(ctx, srcRoot, destBranch, srcBranch)
	if err != nil {
		return promoteSummary{}, err
	}
	if len(eligible) == 0 {
		opts.log("INFO", "", "nothing to promote — no commits beyond the destination", "",
			[2]string{"source", srcBranch}, [2]string{"dest", destBranch})
		return promoteSummary{mode: "commits"}, nil
	}
	selected, err := selectCommits(opts, p, eligible)
	if err != nil {
		return promoteSummary{}, err
	}
	if len(selected) == 0 {
		opts.log("INFO", "", "nothing to promote — no commits selected", "")
		return promoteSummary{mode: "commits"}, nil
	}

	for _, c := range selected {
		opts.log("INFO", "commit", "picking", "",
			[2]string{"sha", c.short()}, [2]string{"subject", c.subject})
	}
	if p.dryRun {
		opts.log("INFO", "", "dry-run — nothing applied", "",
			[2]string{"dest", destBranch}, [2]string{"commits", itoa(len(selected))})
		return promoteSummary{mode: "commits", what: shaList(selected), count: len(selected), dryRun: true}, nil
	}

	shas := make([]string, len(selected))
	for i, c := range selected {
		shas[i] = c.sha
	}
	if err := cherryPickInto(ctx, dest.path, shas); err != nil {
		return promoteSummary{}, err
	}
	opts.log("INFO", "", "promote complete", "",
		[2]string{"dest", destBranch}, [2]string{"commits", itoa(len(selected))})
	return promoteSummary{mode: "commits", what: shaList(selected), count: len(selected)}, nil
}

// recordPromoteLog persists a local `promote` cmd-log record (root = source
// worktree) so logview shows what was funneled. Best-effort, mirrors
// saveWatchDeployRecord's guards.
func recordPromoteLog(opts PromoteOpts, srcRoot, destBranch string, sum promoteSummary, started time.Time) {
	if opts.Cfg == nil || opts.Cfg.CmdLogsDisabled {
		return
	}
	kind := "modules"
	if sum.mode == "commits" {
		kind = "commits"
	}
	rec := config.CmdLogRecord{
		Cmd:        fmt.Sprintf("promote %s → %s", sum.what, destBranch),
		Command:    "promote",
		Exit:       0,
		Started:    started,
		DurationMS: time.Since(started).Milliseconds(),
		Lines: []config.ReportLine{{Level: "INFO", Text: fmt.Sprintf(
			"promote %s — %s=%s dest=%s count=%d", sum.mode, kind, sum.what, destBranch, sum.count)}},
	}
	_ = config.SaveCmdLog(srcRoot, rec)
	_, _ = config.PruneCmdLogs(srcRoot, opts.Cfg.CmdLogsRetentionDays, opts.Cfg.CmdLogsMaxRuns)
}

// itoa is a tiny local helper to keep the field lists readable.
func itoa(n int) string { return fmt.Sprintf("%d", n) }

// shaList joins commits' short SHAs with commas.
func shaList(cs []deployCommit) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.short()
	}
	return strings.Join(parts, ",")
}
