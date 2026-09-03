package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// runPromoteReset moves the accumulation branch back onto its base (Unit 112)
// — the operation that closes promote's cycle: once a feature has been merged
// into the base, the branch that fed the instance is history nobody wants to
// keep building on, and every further promote makes straightening it out more
// expensive.
//
// It is purely local, like the rest of promote: it moves a branch and a
// worktree and never contacts the server. Bringing the target's code along is
// `deploy --set-code <base>` (or `--with-local`, which runs this same core
// first).
func runPromoteReset(ctx context.Context, opts PromoteOpts, p promoteArgs, srcRoot string, dest gitWorktree, destBranch string, started time.Time) error {
	base, err := resolvePromoteBase(ctx, opts, p, srcRoot)
	if err != nil {
		return err
	}
	res, err := resolveLocalRef(ctx, opts.log, srcRoot, base, false, p.noFetch)
	if err != nil {
		return err
	}

	plan, err := planBranchReset(ctx, dest.path, destBranch, base, res.sha)
	if err != nil {
		return err
	}
	if plan.prevSHA == plan.baseSHA {
		opts.log("INFO", "", "nothing to reset — branch is already at the base", "",
			[2]string{"branch", destBranch}, [2]string{"base", base}, [2]string{"sha", shortSHA(plan.baseSHA)})
		return nil
	}

	ahead, behind, aerr := gitAheadBehind(ctx, dest.path, plan.baseSHA, plan.prevSHA)
	fields := [][2]string{
		{"branch", destBranch}, {"base", base}, {"sha", shortSHA(plan.baseSHA)},
		{"prev", shortSHA(plan.prevSHA)},
	}
	if aerr == nil {
		fields = append(fields, [2]string{"ahead", itoa(ahead)}, [2]string{"behind", itoa(behind)})
	}
	opts.log("INFO", "", "resetting", "", fields...)
	if opts.OnSync != nil {
		opts.OnSync(plan.changes)
	}

	// Commits on the branch that the base does not contain are the only thing a
	// reset can really cost: after a merge they are already in the base under
	// other hashes, but nothing here can prove that, so they are reported.
	if aerr == nil && ahead > 0 {
		opts.log("WARNING", "", "branch has commits the base does not contain — they leave the branch (recoverable from the reflog)", "",
			[2]string{"commits", itoa(ahead)}, [2]string{"prev", shortSHA(plan.prevSHA)})
	}
	if len(plan.collisions) > 0 {
		verb := "would be lost by the move"
		if p.discard {
			verb = "will be discarded"
		}
		opts.log("WARNING", "", "uncommitted files on the destination "+verb, "",
			[2]string{"files", strings.Join(plan.collisions, ",")})
	}

	if p.dryRun {
		opts.log("INFO", "", "dry-run — nothing reset", "",
			[2]string{"branch", destBranch}, [2]string{"files", itoa(len(plan.changes))},
			[2]string{"kept", itoa(plan.kept)})
		return nil
	}

	// A reset that only rewinds the branch onto commits the base already
	// contains destroys nothing and stays scriptable. A reset that drops
	// commits, or that discards uncommitted work, asks first.
	needsConfirm := p.discard || aerr != nil || ahead > 0
	if needsConfirm && !p.force {
		if err := confirmPromoteReset(opts.Palette, destBranch, base, ahead, plan, p.discard); err != nil {
			return err
		}
	}

	if err := applyBranchReset(ctx, dest.path, plan, p.discard); err != nil {
		return err
	}

	done := [][2]string{
		{"branch", destBranch}, {"base", base}, {"sha", shortSHA(plan.baseSHA)},
		{"prev", shortSHA(plan.prevSHA)},
	}
	if p.discard {
		done = append(done, [2]string{"discarded", itoa(plan.discardCount())})
	} else {
		done = append(done, [2]string{"kept", itoa(plan.kept)})
	}
	opts.log("INFO", "", "reset complete", "", done...)
	opts.log("INFO", "", "code on the server is unchanged — move it with deploy --set-code", "",
		[2]string{"hint", fmt.Sprintf("deploy --set-code %s --from <target>", base)})

	recordPromoteLog(opts, srcRoot, destBranch,
		promoteSummary{mode: "reset", what: "--reset " + base, count: len(plan.changes)}, started)
	return nil
}

// promoteLineState is the divergence readout `promote --show-branch` appends
// (Unit 112): where the line's base is, how far the branch has drifted from it,
// and how much uncommitted work is sitting on it. This is the signal that a
// reset is due — without it, "has develop drifted from main?" is a question
// only a hand-run git command answers.
//
// Every field is best-effort: no base configured reports base=none and no
// counts (rather than a fabricated zero), and any git failure simply drops the
// counts. The query never fails because of them.
func promoteLineState(ctx context.Context, opts PromoteOpts, branch, destPath string) [][2]string {
	base := ""
	if opts.Cfg != nil {
		base = opts.Cfg.PromoteBase
	}
	if base == "" {
		return [][2]string{{"base", "none"}}
	}
	fields := [][2]string{{"base", base}}
	if opts.Cfg != nil && opts.Cfg.PromoteBaseSource != "" {
		fields = append(fields, [2]string{"base_source", opts.Cfg.PromoteBaseSource})
	}
	if destPath == "" {
		return fields
	}
	if ahead, behind, err := gitAheadBehind(ctx, destPath, base, branch); err == nil {
		fields = append(fields, [2]string{"ahead", itoa(ahead)}, [2]string{"behind", itoa(behind)})
	}
	if out, err := gitOutput(ctx, destPath, "status", "--porcelain"); err == nil {
		fields = append(fields, [2]string{"dirty", itoa(len(nonEmptyLines(string(out))))})
	}
	return fields
}

// resolvePromoteBase applies the base precedence: the positional › [promote]
// base › (TTY) a pick among the refs a repo re-bases onto in practice ›
// (headless) fail closed. A picked base is offered for persistence, so the
// question is asked once per repo rather than once per reset.
func resolvePromoteBase(ctx context.Context, opts PromoteOpts, p promoteArgs, srcRoot string) (string, error) {
	if b := strings.TrimSpace(p.base); b != "" {
		return b, nil
	}
	if opts.Cfg != nil && opts.Cfg.PromoteBase != "" {
		return opts.Cfg.PromoteBase, nil
	}
	if !stdinIsTTY() {
		return "", fmt.Errorf(
			"%w: no promote base configured — pass it (`promote --reset <base>`) or save one with `promote --set-base <ref>`",
			ErrUsage)
	}
	candidates := baseCandidates(ctx, srcRoot)
	if len(candidates) == 0 {
		return "", fmt.Errorf("%w: no candidate base ref found — pass one: promote --reset <base>", ErrUsage)
	}
	picked, err := PickOne("Base to reset onto", candidates, opts.Palette)
	if err != nil {
		return "", err
	}
	if confirmRememberBase(opts, picked) {
		if serr := config.SavePromoteBase(picked); serr != nil {
			opts.log("WARNING", "", "could not save the promote base", "", [2]string{"reason", serr.Error()})
		} else {
			if opts.Cfg != nil {
				opts.Cfg.PromoteBase = picked
			}
			opts.log("INFO", "", "promote base set", "", [2]string{"base", picked})
		}
	}
	return picked, nil
}

// baseCandidates lists the refs a deploy line is plausibly re-based onto, in
// the order they are usually wanted: the tracked integration branches first,
// then their local counterparts. Only refs that actually resolve are offered.
func baseCandidates(ctx context.Context, root string) []string {
	var out []string
	for _, ref := range []string{"origin/main", "origin/master", "origin/develop", "main", "master", "develop"} {
		if refExists(ctx, root, ref) {
			out = append(out, ref)
		}
	}
	return out
}

// refExists reports whether ref resolves to a commit in the repo at root.
func refExists(ctx context.Context, root, ref string) bool {
	out, err := gitOutput(ctx, root, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil && firstLine(string(out)) != ""
}

// confirmRememberBase asks whether to persist the chosen base as the default
// [promote] base. Mirrors confirmRememberBranch; a declined prompt just means
// the base is passed per call.
func confirmRememberBase(opts PromoteOpts, base string) bool {
	remember := true
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Remember " + base + " as the promote base?").
			Description("Saved to [promote] base — `promote --reset` will use it by default.").
			Affirmative("Remember").
			Negative("Not now").
			Value(&remember),
	)).
		WithTheme(BuildHuhTheme(opts.Palette)).
		WithInput(os.Stdin).
		WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return false
	}
	return remember
}

// confirmPromoteReset is the gate for a reset that can cost something: it drops
// commits the base does not contain, or it discards uncommitted work. Bypassed
// by --force and TTY-guarded otherwise.
func confirmPromoteReset(palette theme.Palette, branch, base string, ahead int, plan resetPlan, discard bool) error {
	if err := requireTTY("pass --force to reset without a prompt"); err != nil {
		return err
	}
	danger := lipgloss.NewStyle().Foreground(palette.Error).Bold(true)
	title := "⚠  Reset " + danger.Render(branch) + " onto " + base
	var what []string
	if ahead > 0 {
		what = append(what, fmt.Sprintf("%d commit(s) leave the branch (reflog keeps them)", ahead))
	}
	if discard {
		what = append(what, fmt.Sprintf("%d uncommitted file(s) discarded, %d untracked file(s) removed",
			len(plan.collisions), len(plan.untracked)))
	}
	if len(what) == 0 {
		what = append(what, "the branch moves to "+shortSHA(plan.baseSHA))
	}
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(title).
			Description(strings.Join(what, "\n") + ".").
			Affirmative("Reset").
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
