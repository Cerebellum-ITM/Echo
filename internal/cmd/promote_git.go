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
// dryRun, applies the tracked patch (via `git apply`, all-or-nothing) and
// copies the untracked files into the destination worktree — leaving it dirty.
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

	patchArgs := append([]string{"diff", "--binary", "HEAD", "--"}, dirs...)
	patch, err := gitOutput(ctx, srcRoot, patchArgs...)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(patch)) > 0 {
		if err := gitApplyStdin(ctx, destPath, patch); err != nil {
			return nil, err
		}
	}
	for _, u := range untracked {
		if err := copyFileInto(srcRoot, destPath, u); err != nil {
			return nil, fmt.Errorf("copy %s: %w", u, err)
		}
	}
	return changes, nil
}

// gitApplyStdin pipes a patch into `git -C dest apply`. Without --3way, git
// apply is all-or-nothing: on any hunk that doesn't apply it fails and leaves
// the working tree untouched → a clean conflict abort. Failure maps to
// ErrPromoteConflict with git's stderr.
func gitApplyStdin(ctx context.Context, dest string, patch []byte) error {
	c := exec.CommandContext(ctx, "git", "-C", dest, "apply", "--whitespace=nowarn")
	c.Stdin = bytes.NewReader(patch)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", ErrPromoteConflict, msg)
		}
		return fmt.Errorf("%w: %v", ErrPromoteConflict, err)
	}
	return nil
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
