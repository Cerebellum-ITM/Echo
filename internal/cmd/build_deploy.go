package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
)

// runDeployBuild is the build-mode flow for `deploy`. Deploy's interactive
// heart is its local commit + dirty-module picker, which otherwise blocks at
// run time — bad inside a sequence. This captures that selection up front and
// bakes it into non-interactive `--commits`/`--modules` flags (plus an
// optional flag pass), so the composed line is shown in review, saved by
// `--last`, and runs unattended. The remote target is not resolved for
// EXECUTION here — a sequence bakes it (`--from`) and a standalone
// `deploy --build` lets deploy resolve it at run time — but it IS resolved
// read-only (non-interactively) to load the deployed-SHA history, so the
// picker mutes already-deployed commits exactly like the standalone deploy.
func runDeployBuild(ctx context.Context, opts BuildOpts) (BuildResult, error) {
	commits, err := gitRecentCommits(ctx, opts.Root, 20)
	if err != nil {
		return BuildResult{}, err
	}
	dirty, derr := gitDirtyModules(ctx, opts.Root)
	if derr != nil {
		warn(opts, "dirty detection skipped: "+derr.Error())
	}
	if len(commits) == 0 && len(dirty) == 0 {
		return BuildResult{}, fmt.Errorf("%w for %q — no commits or dirty modules to deploy",
			ErrNothingToBuild, opts.Command)
	}

	// Build mode does no remote work, so manual ctrl+d / ctrl+a marks must NOT
	// persist here (allowMark=false): the sequence's commit-point is its
	// post-build review, not this picker, and persisting now would mark
	// commits for a sequence the operator may still cancel. Muting, however,
	// only READS the history — and the target IS known up front in a sequence
	// (--from or the project [connect] binding). Resolve it non-interactively
	// (no target picker at build time) so already-deployed commits render
	// muted just like the standalone deploy picker; an unresolved target
	// (no --from, no [connect]) degrades to an empty set — no muting.
	var deployedSet map[string]bool
	if sshHost, remotePath, rerr := resolvePullRemote(opts.Cfg, opts.From); rerr == nil {
		deployedSet = config.LoadDeployedSHAs(config.ProjectKey(opts.Root),
			config.DeployTargetKey(sshHost, remotePath))
	}
	selected, selectedDirty, _, err := pickDeployItems(commits, dirty, deployedSet, false, opts.Palette)
	if err != nil {
		return BuildResult{}, err
	}

	var flags []chosenFlag
	if len(selected) > 0 {
		shas := make([]string, len(selected))
		for i, c := range selected {
			shas[i] = c.short()
		}
		flags = append(flags, chosenFlag{name: "--commits", value: strings.Join(shas, ","), sep: "="})
	}
	if len(selectedDirty) > 0 {
		names := make([]string, len(selectedDirty))
		for i, dm := range selectedDirty {
			names[i] = dm.name
		}
		flags = append(flags, chosenFlag{name: "--modules", value: strings.Join(names, ","), sep: "="})
	}

	// Optional boolean flags for the run, mirroring the generic builder's
	// flag step. --from/--limit/--commits/--modules are handled elsewhere.
	picked, _, canceled, err := runFuzzyPickerCore(
		"Deploy flags (Tab to toggle, Enter to confirm)",
		[]string{"--dry-run", "--force", "--i18n", "--no-i18n"},
		nil, nil, nil, opts.Palette, opts.Cfg.Stage)
	if err != nil {
		return BuildResult{}, err
	}
	if canceled {
		return BuildResult{}, ErrCancelled
	}
	for _, f := range picked {
		flags = append(flags, chosenFlag{name: f})
	}

	args := composeArgs(nil, flags)
	if opts.SkipDecide {
		return BuildResult{Args: args, Action: BuildRun}, nil
	}
	action, err := decideAction(opts, args)
	if err != nil {
		return BuildResult{}, err
	}
	return BuildResult{Args: args, Action: action}, nil
}
