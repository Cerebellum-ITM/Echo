package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

// remoteDirtyEntry is one path in the server checkout's dirty overlay, with
// whether git considers it untracked (cleaned by `git clean`) vs tracked
// (reverted by `git checkout --`).
type remoteDirtyEntry struct {
	path      string
	untracked bool
}

// addonsDirNames are the conventional container directories a module can live
// under; the segment right after the last such directory is the module name.
var addonsDirNames = map[string]bool{
	"addons": true, "custom": true, "extra_addons": true,
	"enterprise": true, "external_addons": true,
}

// runPushClean reverts the remote dirty overlay on a git-deploy target: the
// counterpart to a `push --dirty` overlay, run once its content has been
// promoted to commits and deployed. It scopes to the named modules (or every
// module with --all, or a picker), previews with --dry-run, and gates a real
// run behind the prod check and a destructive confirm.
func runPushClean(ctx context.Context, opts PushOpts, p pushArgs) error {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, opts.Log)
	if err != nil {
		return err
	}
	g := resolveGitDeploy(opts.Cfg, rsc.fromName, rsc.sshHost, rsc.remotePath)
	if !g.enabled {
		return fmt.Errorf("%w: push --clean needs a git-deploy target (set git_deploy on it)", ErrUsage)
	}
	absDir := absGitDir(rsc.remotePath, g.path)

	entries := remoteDirtyEntries(ctx, rsc, absDir)
	if len(entries) == 0 {
		opts.log("INFO", "clean", "nothing to clean — remote overlay is empty", rsc.prof.DBName)
		return nil
	}

	var scoped []remoteDirtyEntry
	switch {
	case p.all:
		scoped = entries
	case len(p.modules) > 0:
		scoped = filterDirtyByModules(entries, p.modules)
		if len(scoped) == 0 {
			opts.log("INFO", "clean", "nothing to clean — no overlay files for the given modules", rsc.prof.DBName,
				[2]string{"modules", strings.Join(p.modules, ",")})
			return nil
		}
	default:
		candidates := dirtyModuleCandidates(entries)
		if len(candidates) == 0 {
			return fmt.Errorf("%w: could not map the remote overlay to modules — pass module names or --all", ErrUsage)
		}
		picked, perr := runFuzzyPicker("Modules to clean on "+targetLabel(rsc), candidates, opts.Palette)
		if perr != nil {
			return perr
		}
		scoped = filterDirtyByModules(entries, picked)
	}

	if p.dryRun {
		if opts.OnSync != nil {
			opts.OnSync(dirtyEntriesToChanges(scoped))
		}
		opts.log("INFO", "clean", "dry-run — nothing reverted", rsc.prof.DBName,
			[2]string{"files", strconv.Itoa(len(scoped))})
		return nil
	}

	if err := confirmRemoteProd(opts.Palette, "push --clean", rsc, opts.Args); err != nil {
		return err
	}
	if !argsHaveForce(opts.Args) {
		if err := confirmPushClean(opts.Palette, rsc.prof.DBName, len(scoped)); err != nil {
			return err
		}
	}

	if err := runRemoteClean(ctx, rsc, absDir, scoped); err != nil {
		return err
	}
	if opts.OnSync != nil {
		opts.OnSync(dirtyEntriesToChanges(scoped))
	}
	opts.log("INFO", "clean", "clean complete", rsc.prof.DBName, [2]string{"files", strconv.Itoa(len(scoped))})
	return nil
}

// remoteDirtyEntries lists the server checkout's dirty paths (porcelain).
func remoteDirtyEntries(ctx context.Context, rsc remoteShellContext, absDir string) []remoteDirtyEntry {
	var out []remoteDirtyEntry
	for _, l := range remoteGitLines(ctx, rsc, absDir, "status", "--porcelain") {
		code, pth, ok := parsePorcelainStatus(l)
		if !ok || pth == "" {
			continue
		}
		out = append(out, remoteDirtyEntry{path: pth, untracked: code == "??"})
	}
	return out
}

// filterDirtyByModules keeps the entries whose path maps to one of modules.
func filterDirtyByModules(entries []remoteDirtyEntry, modules []string) []remoteDirtyEntry {
	want := make(map[string]bool, len(modules))
	for _, m := range modules {
		want[m] = true
	}
	var out []remoteDirtyEntry
	for _, e := range entries {
		if want[moduleOfPath(e.path)] {
			out = append(out, e)
		}
	}
	return out
}

// dirtyModuleCandidates is the sorted, deduped set of modules the dirty overlay
// maps to (the picker's options).
func dirtyModuleCandidates(entries []remoteDirtyEntry) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if m := moduleOfPath(e.path); m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// moduleOfPath maps a repo-relative path to its module: the segment right after
// the last addons-container directory, or the first segment when the module
// lives at the repo root. A top-level file (no directory) maps to "".
func moduleOfPath(p string) string {
	segs := strings.Split(p, "/")
	if len(segs) < 2 {
		return ""
	}
	idx := 0
	for i := 0; i < len(segs)-1; i++ {
		if addonsDirNames[segs[i]] {
			idx = i + 1
		}
	}
	if idx > len(segs)-1 {
		idx = len(segs) - 1
	}
	return segs[idx]
}

// runRemoteClean reverts the scoped overlay: tracked paths via `git checkout
// --`, untracked paths via `git clean -fd`.
func runRemoteClean(ctx context.Context, rsc remoteShellContext, absDir string, scoped []remoteDirtyEntry) error {
	var tracked, untracked []string
	for _, e := range scoped {
		if e.untracked {
			untracked = append(untracked, e.path)
		} else {
			tracked = append(tracked, e.path)
		}
	}
	if len(tracked) > 0 {
		args := append([]string{"checkout", "--"}, tracked...)
		if _, err := gitRunSSH(ctx, rsc.sshHost, remoteGitCmd(absDir, args...), nil); err != nil {
			return fmt.Errorf("push --clean: revert tracked overlay: %w", err)
		}
	}
	if len(untracked) > 0 {
		args := append([]string{"clean", "-fd", "--"}, untracked...)
		if _, err := gitRunSSH(ctx, rsc.sshHost, remoteGitCmd(absDir, args...), nil); err != nil {
			return fmt.Errorf("push --clean: remove untracked overlay: %w", err)
		}
	}
	return nil
}

// dirtyEntriesToChanges renders the overlay entries as a change tree: untracked
// files are removed (deleted glyph), tracked files revert (changed glyph).
func dirtyEntriesToChanges(entries []remoteDirtyEntry) []FileChange {
	out := make([]FileChange, 0, len(entries))
	for _, e := range entries {
		op := "changed"
		if e.untracked {
			op = "deleted"
		}
		out = append(out, FileChange{Op: op, Path: e.path})
	}
	return out
}

// argsHaveForce reports whether --force is present in the raw args.
func argsHaveForce(args []string) bool {
	for _, a := range args {
		if a == "--force" {
			return true
		}
	}
	return false
}

// confirmPushClean is the destructive confirm for a real `push --clean`,
// bypassed by --force and TTY-guarded otherwise.
func confirmPushClean(palette theme.Palette, db string, n int) error {
	if err := requireTTY("pass --force to clean without a prompt"); err != nil {
		return err
	}
	what := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).
		Render(strconv.Itoa(n) + " overlay file(s)")
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  Revert " + what).
			Description("This reverts uncommitted overlay changes on " + db + "'s checkout (git checkout / clean).").
			Affirmative("Revert").
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
