package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/config"
)

// promoteDirtyRow is the fixed source-picker row for the current worktree's
// uncommitted changes.
const promoteDirtyRow = "◆ this worktree · uncommitted changes"

// shortWorktreePath renders a worktree path relative to the directory that
// holds the source checkout, so sibling worktrees of the same repo show as
// their leaf name (e.g. "muutrade-develop") instead of a long, mostly-shared
// absolute path. It falls back to a "~"-collapsed home path, then the absolute
// path, when the worktree lives outside that parent.
func shortWorktreePath(path, srcRoot string) string {
	clean := filepath.Clean(path)
	parent := filepath.Dir(filepath.Clean(srcRoot))
	if rel, err := filepath.Rel(parent, clean); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, clean); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + rel
		}
	}
	return clean
}

// promoteDestBranch applies the destination-branch precedence: --to ›
// [promote] branch. There is NO hardcoded default — an empty result means
// "not configured", which the caller resolves by prompting for a selection
// (TTY) or failing closed (headless). Pure.
func promoteDestBranch(p promoteArgs, cfg *config.Config) string {
	if p.to != "" {
		return p.to
	}
	if cfg != nil && cfg.PromoteBranch != "" {
		return cfg.PromoteBranch
	}
	return ""
}

// resolveDest resolves the destination branch and its worktree following the
// cascade: --to › [promote] branch › develop, then locate the worktree. A
// missing worktree is handled by --create-dest (headless) or an interactive
// create/pick prompt; headless without --create-dest fails closed.
func resolveDest(ctx context.Context, opts PromoteOpts, p promoteArgs, srcRoot string, wts []gitWorktree) (gitWorktree, string, error) {
	branch := promoteDestBranch(p, opts.Cfg)

	// No branch configured and none passed → the user must select one; there is
	// no default. Interactive picks a destination worktree (and offers to
	// remember its branch); headless fails closed.
	if branch == "" {
		if !stdinIsTTY() {
			return gitWorktree{}, "", fmt.Errorf(
				"%w: no promote branch configured — pass --to <branch> or save one with `promote --set-branch <name>`",
				ErrUsage)
		}
		return pickDestWorktree(opts, srcRoot, wts)
	}

	if w, ok := worktreeForBranch(wts, branch); ok {
		return w, branch, nil
	}

	// The destination branch is not checked out anywhere.
	if p.createDest != "" {
		if err := addWorktree(ctx, srcRoot, p.createDest, branch); err != nil {
			return gitWorktree{}, "", fmt.Errorf("create worktree for %q: %w", branch, err)
		}
		opts.log("INFO", "", "created destination worktree", "",
			[2]string{"branch", branch}, [2]string{"path", p.createDest})
		wts, err := gitWorktrees(ctx, srcRoot)
		if err != nil {
			return gitWorktree{}, "", err
		}
		if w, ok := worktreeForBranch(wts, branch); ok {
			return w, branch, nil
		}
		return gitWorktree{}, "", fmt.Errorf("worktree for %q not found after create", branch)
	}
	if !stdinIsTTY() {
		return gitWorktree{}, "", fmt.Errorf(
			"%w: no worktree has %q checked out — create one with `git worktree add <path> %s`, pass --create-dest <path>, or set the branch with `promote --set-branch <name>`",
			ErrUsage, branch, branch)
	}
	return promptMissingDest(ctx, opts, srcRoot, wts, branch)
}

// promptMissingDest asks how to resolve a destination branch with no worktree:
// create one, pick a different existing worktree, or cancel.
func promptMissingDest(ctx context.Context, opts PromoteOpts, srcRoot string, wts []gitWorktree, branch string) (gitWorktree, string, error) {
	const (
		optCreate = "create"
		optPick   = "pick"
		optCancel = "cancel"
	)
	choice := optCreate
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(fmt.Sprintf("No worktree has %q checked out", branch)).
			Description("promote needs a checkout to land changes in.").
			Options(
				huh.NewOption("Create a worktree for "+branch, optCreate),
				huh.NewOption("Use a different existing worktree", optPick),
				huh.NewOption("Cancel", optCancel),
			).
			Value(&choice),
	)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return gitWorktree{}, "", ErrCancelled
	}

	switch choice {
	case optCreate:
		path := ""
		in := huh.NewForm(huh.NewGroup(
			huh.NewInput().
				Title("Path for the new worktree").
				Description(fmt.Sprintf("git worktree add <path> %s", branch)).
				Value(&path),
		)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
		if err := in.Run(); err != nil || strings.TrimSpace(path) == "" {
			return gitWorktree{}, "", ErrCancelled
		}
		if err := addWorktree(ctx, srcRoot, strings.TrimSpace(path), branch); err != nil {
			return gitWorktree{}, "", fmt.Errorf("create worktree for %q: %w", branch, err)
		}
		opts.log("INFO", "", "created destination worktree", "",
			[2]string{"branch", branch}, [2]string{"path", strings.TrimSpace(path)})
		wts2, err := gitWorktrees(ctx, srcRoot)
		if err != nil {
			return gitWorktree{}, "", err
		}
		if w, ok := worktreeForBranch(wts2, branch); ok {
			return w, branch, nil
		}
		return gitWorktree{}, "", fmt.Errorf("worktree for %q not found after create", branch)

	case optPick:
		return pickDestWorktree(opts, srcRoot, wts)

	default:
		return gitWorktree{}, "", ErrCancelled
	}
}

// pickDestWorktree lets the user choose an existing worktree (other than the
// source) as the destination, then offers to remember its branch as the
// default [promote] branch.
func pickDestWorktree(opts PromoteOpts, srcRoot string, wts []gitWorktree) (gitWorktree, string, error) {
	byLabel := map[string]gitWorktree{}
	var labels []string
	for _, w := range wts {
		if w.branch == "" || sameWorktree(w.path, srcRoot) {
			continue // detached, or the source itself — never a destination
		}
		lbl := fmt.Sprintf("%s  (%s)", shortWorktreePath(w.path, srcRoot), w.branch)
		labels = append(labels, lbl)
		byLabel[lbl] = w
	}
	if len(labels) == 0 {
		return gitWorktree{}, "", fmt.Errorf(
			"%w: no other worktree to promote into — create the deploy branch's worktree (`git worktree add <path> <branch>`) or save it with `promote --set-branch <name>`",
			ErrUsage)
	}
	picked, err := PickOne("Destination worktree", labels, opts.Palette)
	if err != nil {
		return gitWorktree{}, "", err
	}
	w := byLabel[picked]
	if confirmRememberBranch(opts, w.branch) {
		if serr := config.SavePromoteBranch(w.branch); serr != nil {
			opts.log("WARNING", "", "could not save promote branch", "", [2]string{"err", serr.Error()})
		} else {
			if opts.Cfg != nil {
				opts.Cfg.PromoteBranch = w.branch
			}
			opts.log("INFO", "", "promote branch set", "", [2]string{"branch", w.branch})
		}
	}
	return w, w.branch, nil
}

// confirmRememberBranch asks whether to persist the chosen branch as the
// default promote destination.
func confirmRememberBranch(opts PromoteOpts, branch string) bool {
	if !stdinIsTTY() {
		return false
	}
	save := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("Remember %q as the promote branch?", branch)).
			Description("The next promote uses it without asking.").
			Affirmative("Remember").
			Negative("Just this run").
			Value(&save),
	)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return false
	}
	return save
}

// resolveMode decides dirty vs commits and, when neither --dirty nor a source
// branch was given, opens the interactive source picker (this worktree's dirty
// changes, or a branch to cherry-pick from).
func resolveMode(ctx context.Context, opts PromoteOpts, p promoteArgs, srcRoot string, wts []gitWorktree, destBranch string) (mode, srcBranch string, err error) {
	if p.dirty {
		return "dirty", "", nil
	}
	if p.branch != "" {
		return "commits", p.branch, nil
	}
	if !stdinIsTTY() {
		return "", "", fmt.Errorf("%w: specify --dirty or a source branch", ErrUsage)
	}

	branches, err := gitLocalBranches(ctx, srcRoot)
	if err != nil {
		return "", "", fmt.Errorf("list branches: %w", err)
	}
	wtByBranch := map[string]string{}
	for _, w := range wts {
		if w.branch != "" {
			wtByBranch[w.branch] = w.path
		}
	}
	// The branch checked out at srcRoot is "this worktree" too — its committed
	// history, as opposed to the dirty row's uncommitted changes. Mark it so the
	// two rows read as distinct sources of the same checkout, not duplicates.
	curBranch := ""
	for _, w := range wts {
		if sameWorktree(w.path, srcRoot) {
			curBranch = w.branch
			break
		}
	}
	labels := []string{promoteDirtyRow}
	byLabel := map[string]string{}
	// "This worktree" leads the list: the dirty row (above), then this branch's
	// committed history — both sources of the checkout you're standing in — before
	// any other branch.
	if curBranch != "" && curBranch != destBranch {
		lbl := fmt.Sprintf("◆ this worktree · committed history (%s)", curBranch)
		labels = append(labels, lbl)
		byLabel[lbl] = curBranch
	}
	// Other branches follow in git's recency order (most recent commit first),
	// which surfaces the branches you're likely promoting from.
	for _, b := range branches {
		if b == destBranch || b == curBranch {
			continue // dest never funnels into itself; curBranch is already listed
		}
		lbl := b
		if wt := wtByBranch[b]; wt != "" {
			lbl = fmt.Sprintf("%s  (wt: %s)", b, shortWorktreePath(wt, srcRoot))
		}
		labels = append(labels, lbl)
		byLabel[lbl] = b
	}
	picked, err := PickOne("Promote from", labels, opts.Palette)
	if err != nil {
		return "", "", err
	}
	if picked == promoteDirtyRow {
		return "dirty", "", nil
	}
	return "commits", byLabel[picked], nil
}

// selectDirtyModules resolves which dirty modules to promote: the --folder
// positionals (validated against the dirty set) or an interactive multi-select.
func selectDirtyModules(opts PromoteOpts, p promoteArgs, dm []dirtyModule) ([]string, error) {
	names := map[string]bool{}
	for _, d := range dm {
		names[d.name] = true
	}
	if len(p.folders) > 0 {
		for _, f := range p.folders {
			if !names[f] {
				return nil, fmt.Errorf("%w: %q has no uncommitted changes (dirty modules: %s)",
					ErrUsage, f, strings.Join(dirtyNames(dm), ", "))
			}
		}
		return p.folders, nil
	}
	byLabel := map[string]string{}
	labels := make([]string, 0, len(dm))
	for _, d := range dm {
		lbl := dirtyLabel(d)
		labels = append(labels, lbl)
		byLabel[lbl] = d.name
	}
	picked, err := runFuzzyPicker("Modules to promote", labels, opts.Palette)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(picked))
	for _, l := range picked {
		if n := byLabel[l]; n != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

// selectCommits resolves which eligible commits to promote, preserving the
// chronological (cherry-pick) order. --commits matches by SHA prefix against
// the eligible set; interactive opens a multi-select.
func selectCommits(opts PromoteOpts, p promoteArgs, eligible []deployCommit) ([]deployCommit, error) {
	if len(p.commits) > 0 {
		wanted := map[string]bool{}
		for _, c := range p.commits {
			matched := false
			for _, e := range eligible {
				if strings.HasPrefix(e.sha, c) || e.short() == c {
					wanted[e.sha] = true
					matched = true
				}
			}
			if !matched {
				return nil, fmt.Errorf("%w: commit %q is not on the source beyond the destination (or already promoted)", ErrUsage, c)
			}
		}
		return filterCommits(eligible, wanted), nil
	}

	byLabel := map[string]string{}
	// Display newest-first (eligible is oldest-first).
	labels := make([]string, 0, len(eligible))
	for i := len(eligible) - 1; i >= 0; i-- {
		lbl := eligible[i].short() + "  " + eligible[i].subject
		labels = append(labels, lbl)
		byLabel[lbl] = eligible[i].sha
	}
	picked, err := runFuzzyPicker("Commits to promote", labels, opts.Palette)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, l := range picked {
		if sha := byLabel[l]; sha != "" {
			wanted[sha] = true
		}
	}
	return filterCommits(eligible, wanted), nil
}

// filterCommits keeps the eligible commits whose SHA is wanted, in the
// original (chronological) order.
func filterCommits(eligible []deployCommit, wanted map[string]bool) []deployCommit {
	var out []deployCommit
	for _, e := range eligible {
		if wanted[e.sha] {
			out = append(out, e)
		}
	}
	return out
}
