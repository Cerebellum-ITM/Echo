package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitToplevel resolves the git worktree root of dir (the directory promote was
// launched in). Fails clearly when dir is not inside a git repo.
func gitToplevel(ctx context.Context, dir string) (string, error) {
	out, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not inside a git repository (%s): %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitWorktree is one entry of `git worktree list` — a checkout on disk with
// the branch it has checked out ("" when detached/bare).
type gitWorktree struct {
	path   string
	branch string
	head   string
}

// gitWorktrees lists the repo's worktrees (the primary checkout included).
func gitWorktrees(ctx context.Context, root string) ([]gitWorktree, error) {
	out, err := gitOutput(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(string(out)), nil
}

// parseWorktrees parses `git worktree list --porcelain`: records are blank-line
// separated, each opening with `worktree <path>` then `HEAD <sha>` and either
// `branch refs/heads/<name>` or `detached`. Pure — the testable core.
func parseWorktrees(out string) []gitWorktree {
	var wts []gitWorktree
	var cur *gitWorktree
	flush := func() {
		if cur != nil {
			wts = append(wts, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &gitWorktree{path: strings.TrimPrefix(line, "worktree ")}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "HEAD "):
			cur.head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "":
			flush()
		}
	}
	flush()
	return wts
}

// worktreeForBranch returns the worktree that has branch checked out.
func worktreeForBranch(wts []gitWorktree, branch string) (gitWorktree, bool) {
	for _, w := range wts {
		if w.branch == branch {
			return w, true
		}
	}
	return gitWorktree{}, false
}

// sameWorktree reports whether two worktree paths are the same directory.
func sameWorktree(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// gitBranchExists reports whether refs/heads/<branch> exists.
func gitBranchExists(ctx context.Context, root, branch string) bool {
	_, err := gitOutput(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// addWorktree creates a worktree for branch at path, creating the branch when
// it doesn't exist yet (`-b`).
func addWorktree(ctx context.Context, root, path, branch string) error {
	if gitBranchExists(ctx, root, branch) {
		_, err := gitOutput(ctx, root, "worktree", "add", path, branch)
		return err
	}
	_, err := gitOutput(ctx, root, "worktree", "add", "-b", branch, path)
	return err
}

// dirtyChangeSet returns the tracked changes (vs HEAD) and the untracked file
// paths under the given module dirs, all repo-relative. Tracked entries carry
// an op (new/changed/deleted); untracked are returned as raw paths (the caller
// tags them "new").
func dirtyChangeSet(ctx context.Context, src string, dirs []string) (tracked []FileChange, untracked []string, err error) {
	nsArgs := append([]string{"diff", "HEAD", "--name-status", "--"}, dirs...)
	out, err := gitOutput(ctx, src, nsArgs...)
	if err != nil {
		return nil, nil, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 2 {
			continue
		}
		status := cols[0]
		path := cols[len(cols)-1] // rename: status\told\tnew → new wins
		op := "changed"
		switch status[0] {
		case 'A':
			op = "new"
		case 'D':
			op = "deleted"
		}
		tracked = append(tracked, FileChange{Op: op, Path: path})
	}
	uArgs := append([]string{"ls-files", "--others", "--exclude-standard", "--"}, dirs...)
	uout, err := gitOutput(ctx, src, uArgs...)
	if err != nil {
		return nil, nil, err
	}
	for _, line := range strings.Split(string(uout), "\n") {
		if l := strings.TrimSpace(line); l != "" {
			untracked = append(untracked, l)
		}
	}
	return tracked, untracked, nil
}

// applyDirty computes the change set for the selected module dirs and, unless
// dryRun, syncs those files into the destination worktree by copying their
// CURRENT source content (new/changed → overwrite, deleted → remove). It never
// goes through git's index, so it works regardless of whether the destination
// tracks the module, is itself dirty, or is missing the file entirely — the
// cases where a `git apply` patch fails ("does not exist in index" / "does not
// match index"). The destination is left dirty (uncommitted), as intended.
func applyDirty(ctx context.Context, srcRoot, destPath string, dirs []string, dryRun bool) ([]FileChange, error) {
	tracked, untracked, err := dirtyChangeSet(ctx, srcRoot, dirs)
	if err != nil {
		return nil, err
	}
	changes := append([]FileChange(nil), tracked...)
	for _, u := range untracked {
		changes = append(changes, FileChange{Op: "new", Path: u})
	}
	if dryRun {
		return changes, nil
	}

	// Snapshot every touched destination path so a mid-sync failure rolls back
	// cleanly (all-or-nothing), without disturbing the rest of the destination's
	// uncommitted work.
	rels := make([]string, 0, len(changes))
	for _, c := range changes {
		rels = append(rels, c.Path)
	}
	snap, serr := snapshotFiles(destPath, rels)
	if serr != nil {
		return nil, serr
	}
	if err := syncFiles(srcRoot, destPath, changes); err != nil {
		restoreFiles(destPath, snap)
		return nil, err
	}
	return changes, nil
}

// syncFiles moves the change set into the destination by file: new/changed
// files are copied from the source worktree (overwriting the destination's
// version), deleted files are removed. A plain file-level move — no git index
// involved, so a dirty or untracked destination is fine.
func syncFiles(srcRoot, destPath string, changes []FileChange) error {
	for _, c := range changes {
		if c.Op == "deleted" {
			p := filepath.Join(destPath, filepath.FromSlash(c.Path))
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove %s: %w", c.Path, err)
			}
			continue
		}
		if err := copyFileInto(srcRoot, destPath, c.Path); err != nil {
			return fmt.Errorf("copy %s: %w", c.Path, err)
		}
	}
	return nil
}

// destDirtyPaths returns the set of repo-relative paths under dirs that already
// have uncommitted changes in the destination worktree. promote uses it to warn
// before overwriting them — their accumulated work is replaced by the source's
// version. Best-effort: a git error yields an empty set (no warning).
func destDirtyPaths(ctx context.Context, destPath string, dirs []string) map[string]bool {
	out, err := gitOutput(ctx, destPath, append([]string{"status", "--porcelain", "--"}, dirs...)...)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, p := range parsePorcelainPaths(string(out)) {
		set[p] = true
	}
	return set
}

// fileSnapshot is a destination file's content and mode captured before a
// sync, or absent==true when the file didn't exist yet.
type fileSnapshot struct {
	data   []byte
	mode   os.FileMode
	absent bool
}

// snapshotFiles records the destination's current bytes+mode for each rel path
// so restoreFiles can undo a partial sync without disturbing whatever else
// the destination worktree already had uncommitted.
func snapshotFiles(root string, rels []string) (map[string]fileSnapshot, error) {
	snap := make(map[string]fileSnapshot, len(rels))
	for _, rel := range rels {
		p := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(p)
		if os.IsNotExist(err) {
			snap[rel] = fileSnapshot{absent: true}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", rel, err)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", rel, err)
		}
		snap[rel] = fileSnapshot{data: data, mode: info.Mode().Perm()}
	}
	return snap, nil
}

// restoreFiles rewinds the snapshotted files to their captured state: rewrite
// the ones that existed, remove the ones that didn't. Best-effort — a conflict
// abort shouldn't itself error out.
func restoreFiles(root string, snap map[string]fileSnapshot) {
	for rel, s := range snap {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if s.absent {
			_ = os.Remove(p)
			continue
		}
		_ = os.WriteFile(p, s.data, s.mode)
	}
}

// copyFileInto copies srcRoot/rel to destRoot/rel, creating parent dirs and
// preserving the file mode. Overwrites an existing destination file (the
// incoming change wins).
func copyFileInto(srcRoot, destRoot, rel string) error {
	srcPath := filepath.Join(srcRoot, filepath.FromSlash(rel))
	destPath := filepath.Join(destRoot, filepath.FromSlash(rel))
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// eligibleCommits returns the commits on srcBranch that are NOT yet on
// destBranch, oldest-first (cherry-pick order), each with its subject. Dedup is
// by patch-id via `git cherry`, so a commit already applied to the destination
// (even with a different SHA) is excluded.
func eligibleCommits(ctx context.Context, root, destBranch, srcBranch string) ([]deployCommit, error) {
	out, err := gitOutput(ctx, root, "cherry", destBranch, srcBranch)
	if err != nil {
		return nil, fmt.Errorf("git cherry %s %s: %w", destBranch, srcBranch, err)
	}
	var commits []deployCommit
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "+ ") {
			continue // "- <sha>" == already on destBranch
		}
		sha := strings.TrimSpace(strings.TrimPrefix(line, "+ "))
		if sha == "" {
			continue
		}
		subj, serr := gitOutput(ctx, root, "show", "-s", "--format=%s", sha)
		commits = append(commits, deployCommit{sha: sha, subject: strings.TrimSpace(string(subj))})
		_ = serr // a missing subject just leaves it blank
	}
	return commits, nil
}

// cherryPickInto applies shas (oldest-first) onto the destination worktree. On
// conflict it aborts the cherry-pick so the destination is left untouched, and
// maps the failure to ErrPromoteConflict.
func cherryPickInto(ctx context.Context, dest string, shas []string) error {
	args := append([]string{"-C", dest, "cherry-pick"}, shas...)
	c := exec.CommandContext(ctx, "git", args...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		abort := exec.CommandContext(ctx, "git", "-C", dest, "cherry-pick", "--abort")
		_ = abort.Run()
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", ErrPromoteConflict, msg)
		}
		return fmt.Errorf("%w: %v", ErrPromoteConflict, err)
	}
	return nil
}
