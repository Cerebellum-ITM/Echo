package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
)

// gitDeployConfig is a resolved per-target git-deploy topology (Unit 102).
// enabled=false means the target uses the legacy rsync push path unchanged.
type gitDeployConfig struct {
	enabled bool
	branch  string // deploy branch on the server (default "echo/deploy")
	path    string // git worktree dir relative to remotePath ("" / "." = root)
}

// incomingRef is the scratch ref a deploy pushes commit objects to before
// advancing the deploy branch. It is never checked out — so pushing to it can
// never be rejected by receive.denyCurrentBranch — and it is deleted right
// after the advance.
const incomingRef = "refs/echo/incoming"

const defaultDeployBranch = "echo/deploy"

// Package-level seams so tests can script the git transport in place of the
// real binaries. gitRunSSH runs a remote git/shell command over SSH
// (buffered); gitPushCommand builds the LOCAL `git push` that ships objects to
// the server checkout.
var (
	gitRunSSH      = runSSH
	gitPushCommand = func(ctx context.Context, root, sshHost, absDir, refspec string) *exec.Cmd {
		return exec.CommandContext(ctx, "git", "-C", root, "push", "--force", sshHost+":"+absDir, refspec)
	}
)

// resolveGitDeploy resolves the git-deploy topology for the target the deploy
// resolved to. Precedence:
//
//  1. An explicit named connect target is authoritative (its GitDeploy=false
//     disables git mode even if a binding would enable it).
//  2. Otherwise the resolved PHYSICAL target (sshHost+remotePath) is matched
//     against the named targets — so a project [connect] link binding that
//     points at a git-deploy target inherits its git config even though the
//     interactive deploy resolved with no name (the common case: `deploy`
//     without --from on a linked directory).
//  3. Otherwise the project's own [connect] git fields.
//
// The branch defaults to echo/deploy when left blank.
func resolveGitDeploy(cfg *config.Config, fromName, sshHost, remotePath string) gitDeployConfig {
	withDefault := func(g gitDeployConfig) gitDeployConfig {
		if strings.TrimSpace(g.branch) == "" {
			g.branch = defaultDeployBranch
		}
		return g
	}
	if cfg == nil {
		return gitDeployConfig{}
	}
	if fromName != "" {
		for _, t := range cfg.ConnectTargets {
			if t.Name == fromName {
				if !t.GitDeploy {
					return gitDeployConfig{}
				}
				return withDefault(gitDeployConfig{enabled: true, branch: t.GitBranch, path: t.GitPath})
			}
		}
	}
	if sshHost != "" && remotePath != "" {
		for _, t := range cfg.ConnectTargets {
			if t.GitDeploy && t.SSHHost == sshHost && t.RemotePath == remotePath {
				return withDefault(gitDeployConfig{enabled: true, branch: t.GitBranch, path: t.GitPath})
			}
		}
	}
	if cfg.ConnectGitDeploy {
		return withDefault(gitDeployConfig{enabled: true, branch: cfg.ConnectGitBranch, path: cfg.ConnectGitPath})
	}
	return gitDeployConfig{}
}

// absGitDir resolves the absolute git worktree dir on the server from the
// target's remote path and its (possibly relative) git_path.
func absGitDir(remotePath, gitPath string) string {
	gitPath = strings.TrimSpace(gitPath)
	if gitPath == "" || gitPath == "." {
		return remotePath
	}
	if path.IsAbs(gitPath) {
		return gitPath
	}
	return path.Join(remotePath, gitPath)
}

// remoteGitCmd builds a shell-quoted `git -C <absDir> <args...>` command for
// SSH execution.
func remoteGitCmd(absDir string, args ...string) string {
	var b strings.Builder
	b.WriteString("git -C ")
	b.WriteString(shellQuote(absDir))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(shellQuote(a))
	}
	return b.String()
}

// remoteGitOut runs a remote git command and returns its trimmed stdout.
func remoteGitOut(ctx context.Context, rsc remoteShellContext, absDir string, args ...string) (string, error) {
	out, err := gitRunSSH(ctx, rsc.sshHost, remoteGitCmd(absDir, args...), nil)
	return strings.TrimSpace(string(out)), err
}

// remoteGitLines runs a remote git command and splits its stdout into
// non-empty lines; a failure yields nil (the caller treats it as "no rows").
func remoteGitLines(ctx context.Context, rsc remoteShellContext, absDir string, args ...string) []string {
	out, err := gitRunSSH(ctx, rsc.sshHost, remoteGitCmd(absDir, args...), nil)
	if err != nil {
		return nil
	}
	return nonEmptyLines(string(out))
}

// gitPreflight fails closed before any transfer when the target can't host an
// identical-hash git deploy: git missing on the server, the path isn't a
// checkout, or the checkout is a different repository (its object store lacks
// this repo's root commit).
func gitPreflight(ctx context.Context, rsc remoteShellContext, localRoot, absDir string) error {
	if _, err := gitRunSSH(ctx, rsc.sshHost, "git --version", nil); err != nil {
		return fmt.Errorf("%w: git deploy needs git on the remote host — not found: %v", ErrUsage, err)
	}
	isWT, err := remoteGitOut(ctx, rsc, absDir, "rev-parse", "--is-inside-work-tree")
	if err != nil || isWT != "true" {
		return fmt.Errorf("%w: git deploy needs a git checkout at %s on the remote — it is not one", ErrUsage, absDir)
	}
	rootOut, err := gitOutput(ctx, localRoot, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return fmt.Errorf("git deploy: read local root commit: %w", err)
	}
	root := firstLine(string(rootOut))
	if root == "" {
		return fmt.Errorf("git deploy: could not determine the local repository's root commit")
	}
	if _, err := gitRunSSH(ctx, rsc.sshHost, remoteGitCmd(absDir, "cat-file", "-e", root), nil); err != nil {
		return fmt.Errorf("%w: the checkout at %s is not a clone of this repository (root commit %s absent) — refusing to git-deploy (use --no-git for the legacy rsync push)",
			ErrUsage, absDir, shortSHA(root))
	}
	return nil
}

// gitBootstrap ensures the deploy branch exists and is checked out, returning
// its HEAD SHA as captured BEFORE any advance (the pre-deploy code SHA). A
// missing branch is created at the checkout's current HEAD and a foreign
// checked-out branch is switched — both at the same commit, so the working
// tree and its dirty overlay are untouched at bootstrap.
func gitBootstrap(ctx context.Context, rsc remoteShellContext, absDir, branch string, log logFn) (string, error) {
	if _, err := gitRunSSH(ctx, rsc.sshHost,
		remoteGitCmd(absDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch), nil); err != nil {
		if _, cerr := gitRunSSH(ctx, rsc.sshHost, remoteGitCmd(absDir, "branch", branch, "HEAD"), nil); cerr != nil {
			return "", fmt.Errorf("git deploy: create branch %s: %w", branch, cerr)
		}
		ckptLog(log, "INFO", "git", "created deploy branch", rsc.prof.DBName, [2]string{"branch", branch})
	}
	cur, _ := remoteGitOut(ctx, rsc, absDir, "rev-parse", "--abbrev-ref", "HEAD")
	if cur != branch {
		if _, err := gitRunSSH(ctx, rsc.sshHost, remoteGitCmd(absDir, "checkout", branch), nil); err != nil {
			return "", fmt.Errorf("git deploy: checkout %s: %w", branch, err)
		}
		ckptLog(log, "INFO", "git", "checked out deploy branch", rsc.prof.DBName, [2]string{"branch", branch})
	}
	return remoteGitOut(ctx, rsc, absDir, "rev-parse", "HEAD")
}

// gitPushObjects ships the tip commit's objects to the server checkout's
// object store under the scratch incoming ref, over the user's ~/.ssh/config
// alias (same SSH surface as every other remote call). After this the tip SHA
// exists on the server verbatim.
func gitPushObjects(ctx context.Context, root, sshHost, absDir, sha string) error {
	cmd := gitPushCommand(ctx, root, sshHost, absDir, sha+":"+incomingRef)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git deploy: push objects to remote: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitAdvance moves the deploy branch (and its working tree) to tip while
// preserving the dirty overlay: it discards only the overlay files the move
// would clobber (tracked files tip also changes, untracked files tip
// introduces), then `git reset --keep`. ffGate rejects a diverged branch (a
// forward deploy); it is skipped for a rollback/restore to an older hash.
func gitAdvance(ctx context.Context, rsc remoteShellContext, absDir, branch, tip string, ffGate bool, log logFn) error {
	if ffGate {
		if _, err := gitRunSSH(ctx, rsc.sshHost,
			remoteGitCmd(absDir, "merge-base", "--is-ancestor", "HEAD", tip), nil); err != nil {
			return fmt.Errorf("%w: deploy branch %s has diverged from the deploy target — restore or reset it first", ErrUsage, branch)
		}
	}
	porcelain := remoteGitLines(ctx, rsc, absDir, "status", "--porcelain")
	diff := remoteGitLines(ctx, rsc, absDir, "diff", "--name-only", "HEAD", tip)
	tree := remoteGitLines(ctx, rsc, absDir, "ls-tree", "-r", "--name-only", tip)
	collisions := gitCollisions(porcelain, diff, tree)
	if len(collisions) > 0 {
		untracked := untrackedPorcelainSet(porcelain)
		var tracked []string
		for _, p := range collisions {
			if untracked[p] {
				_, _ = gitRunSSH(ctx, rsc.sshHost, "rm -f "+shellQuote(path.Join(absDir, p)), nil)
			} else {
				tracked = append(tracked, p)
			}
		}
		if len(tracked) > 0 {
			args := append([]string{"checkout", "--"}, tracked...)
			if _, err := gitRunSSH(ctx, rsc.sshHost, remoteGitCmd(absDir, args...), nil); err != nil {
				return fmt.Errorf("git deploy: discard overlay before advance: %w", err)
			}
		}
		ckptLog(log, "WARNING", "git", "overwrote overlay files the deploy touches", rsc.prof.DBName,
			[2]string{"files", strings.Join(collisions, ",")})
	}
	if _, err := gitRunSSH(ctx, rsc.sshHost, remoteGitCmd(absDir, "reset", "--keep", tip), nil); err != nil {
		return fmt.Errorf("git deploy: advance %s to %s: %w", branch, shortSHA(tip), err)
	}
	_, _ = gitRunSSH(ctx, rsc.sshHost, remoteGitCmd(absDir, "update-ref", "-d", incomingRef), nil)
	return nil
}

// gitCollisions returns the dirty overlay paths an advance to tip would
// clobber: a tracked-modified path that tip also changes (appears in the
// HEAD..tip diff), or an untracked path that tip introduces (appears in tip's
// tree). A tracked path tip does NOT touch survives the reset, so it is never
// listed. Pure and order-stable (sorted, deduped) — the unit-tested core.
func gitCollisions(porcelain, diff, tree []string) []string {
	diffSet := toStringSet(diff)
	treeSet := toStringSet(tree)
	seen := map[string]bool{}
	var out []string
	for _, line := range porcelain {
		code, p, ok := parsePorcelainStatus(line)
		if !ok || p == "" || seen[p] {
			continue
		}
		hit := diffSet[p]
		if code == "??" {
			hit = treeSet[p]
		}
		if hit {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// untrackedPorcelainSet is the set of untracked ("??") paths in a porcelain
// status listing.
func untrackedPorcelainSet(porcelain []string) map[string]bool {
	out := map[string]bool{}
	for _, line := range porcelain {
		if code, p, ok := parsePorcelainStatus(line); ok && code == "??" && p != "" {
			out[p] = true
		}
	}
	return out
}

// parsePorcelainStatus splits a `git status --porcelain` v1 line into its
// two-char code and path. A rename ("R  old -> new") yields the new path.
// Quoted paths (non-ASCII) are unquoted at the surrounding double quotes.
func parsePorcelainStatus(line string) (code, p string, ok bool) {
	if len(line) < 4 {
		return "", "", false
	}
	code = line[:2]
	rest := strings.TrimSpace(line[2:])
	if code[0] == 'R' || code[1] == 'R' {
		if i := strings.Index(rest, " -> "); i >= 0 {
			rest = rest[i+len(" -> "):]
		}
	}
	rest = strings.Trim(rest, "\"")
	return code, rest, rest != ""
}

// gitRemoteHasCommit reports whether sha exists in the server checkout's
// object store.
func gitRemoteHasCommit(ctx context.Context, rsc remoteShellContext, absDir, sha string) bool {
	_, err := gitRunSSH(ctx, rsc.sshHost, remoteGitCmd(absDir, "cat-file", "-e", sha), nil)
	return err == nil
}

// gitDeployCommitted delivers the committed content of a git-deploy run:
// preflight, then (real run) bootstrap → push objects → advance the branch to
// tip. In dry-run it only preflights and logs the intended advance. On a real
// run the pre-advance branch HEAD is written to *preCodeSHA for the checkpoint.
func gitDeployCommitted(ctx context.Context, opts DeployOpts, rsc remoteShellContext, g gitDeployConfig, tip string, dryRun bool, preCodeSHA *string) error {
	absDir := absGitDir(rsc.remotePath, g.path)
	if err := gitPreflight(ctx, rsc, opts.Root, absDir); err != nil {
		return err
	}
	if dryRun {
		opts.log("INFO", "git", "would advance deploy branch", rsc.prof.DBName,
			[2]string{"branch", g.branch}, [2]string{"sha", shortSHA(tip)})
		return nil
	}
	pre, err := gitBootstrap(ctx, rsc, absDir, g.branch, opts.Log)
	if err != nil {
		return err
	}
	if preCodeSHA != nil {
		*preCodeSHA = pre
	}
	if err := gitPushObjects(ctx, opts.Root, rsc.sshHost, absDir, tip); err != nil {
		return err
	}
	if err := gitAdvance(ctx, rsc, absDir, g.branch, tip, true, opts.Log); err != nil {
		return err
	}
	opts.log("INFO", "git", "code synced", rsc.prof.DBName,
		[2]string{"branch", g.branch}, [2]string{"sha", shortSHA(tip)})
	return nil
}

// gitRestoreCode moves the deploy branch back to sha (no FF gate — sha is an
// older, already-present hash), used by the rollback paths and
// `deploy --restore-code`. A no-op when git mode is off or sha is empty.
func gitRestoreCode(ctx context.Context, rsc remoteShellContext, g gitDeployConfig, sha string, log logFn) error {
	if !g.enabled || strings.TrimSpace(sha) == "" {
		return nil
	}
	absDir := absGitDir(rsc.remotePath, g.path)
	ckptLog(log, "INFO", "git", "restoring code", rsc.prof.DBName,
		[2]string{"branch", g.branch}, [2]string{"sha", shortSHA(sha)})
	return gitAdvance(ctx, rsc, absDir, g.branch, sha, false, log)
}

// resolveGitTip returns the single tip the selected commit SHAs advance the
// branch to: the one that every other selected SHA is an ancestor of. A
// non-linear selection (no such tip) is an ErrUsage — the branch only advances
// to one point.
func resolveGitTip(ctx context.Context, root string, shas []string) (string, error) {
	if len(shas) == 0 {
		return "", nil
	}
	for _, cand := range shas {
		linear := true
		for _, other := range shas {
			if other == cand {
				continue
			}
			if _, err := gitOutput(ctx, root, "merge-base", "--is-ancestor", other, cand); err != nil {
				linear = false
				break
			}
		}
		if linear {
			return cand, nil
		}
	}
	return "", fmt.Errorf("%w: selected commits are not on a single line of history — deploy the branch tip that contains them (or use --no-git for the rsync path)", ErrUsage)
}

// intersectModules keeps the modules present in the keep set, preserving order.
func intersectModules(mods []string, keep map[string]bool) []string {
	var out []string
	for _, m := range mods {
		if keep[m] {
			out = append(out, m)
		}
	}
	return out
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

// nonEmptyLines splits s into trimmed, non-empty lines.
func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// toStringSet builds a membership set from a slice.
func toStringSet(xs []string) map[string]bool {
	out := make(map[string]bool, len(xs))
	for _, x := range xs {
		out[x] = true
	}
	return out
}
