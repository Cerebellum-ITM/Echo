package cmd

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// WatchOpts configures a `watch` run.
type WatchOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	Log     func(level, sub, msg, db string, fields ...[2]string)
	// StreamOut receives the push/deploy remote lines as they stream.
	StreamOut func(string)
	// OnSync, when set, receives each pushed module's file changes so the
	// caller can render the change tree (same as the standalone `push`).
	OnSync func(changes []FileChange)
}

func (o WatchOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

const (
	defaultWatchInterval = 10 * time.Second
	minWatchInterval     = 2 * time.Second
)

// watchArgs is the parsed shape of the watch input.
type watchArgs struct {
	branch   string
	interval time.Duration
	from     string
	remote   bool
	force    bool
}

// parseWatchArgs extracts the required branch positional plus
// --interval/--from/--remote/--force. The interval is in seconds, clamped to
// a 2s floor; remote-mode switches are consumed so a bare `--from` value is
// not read as the branch.
func parseWatchArgs(args []string) (watchArgs, error) {
	out := watchArgs{interval: defaultWatchInterval}
	out.from, out.remote = remoteFlagsIn(args)
	setInterval := func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("%w: --interval takes a positive number of seconds, got %q", ErrUsage, v)
		}
		d := time.Duration(n) * time.Second
		if d < minWatchInterval {
			d = minWatchInterval
		}
		out.interval = d
		return nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--force":
			out.force = true
		case a == "--interval":
			if i+1 >= len(args) {
				return watchArgs{}, fmt.Errorf("%w: --interval requires a number", ErrUsage)
			}
			if err := setInterval(args[i+1]); err != nil {
				return watchArgs{}, err
			}
			i++
		case strings.HasPrefix(a, "--interval="):
			if err := setInterval(strings.TrimPrefix(a, "--interval=")); err != nil {
				return watchArgs{}, err
			}
		case a == "--from":
			i++ // skip the target value; captured by remoteFlagsIn
		case strings.HasPrefix(a, "--from="), a == "--remote":
			// consumed by remoteFlagsIn
		case strings.HasPrefix(a, "-"):
			return watchArgs{}, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			if out.branch != "" {
				return watchArgs{}, fmt.Errorf("%w: watch takes a single branch name", ErrUsage)
			}
			out.branch = a
		}
	}
	if out.branch == "" {
		return watchArgs{}, fmt.Errorf("%w: watch needs a branch name", ErrUsage)
	}
	return out, nil
}

// RunWatch polls the local ref for the given branch and, on each fast-forward
// advance, pushes the affected modules' committed content and runs a headless
// deploy of the new commits. It blocks until ctx is cancelled (Ctrl+C).
func RunWatch(ctx context.Context, opts WatchOpts) error {
	if err := requireRsync(); err != nil {
		return err
	}
	p, err := parseWatchArgs(opts.Args)
	if err != nil {
		return err
	}
	ref := "refs/heads/" + p.branch
	tip, err := gitRevParse(ctx, opts.Root, ref)
	if err != nil {
		return fmt.Errorf("%w: branch %q not found", ErrUsage, p.branch)
	}

	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, opts.Log)
	if err != nil {
		return err
	}
	// An unattended auto-deployer must not touch prod on a whim: require the
	// explicit --force to even start.
	if strings.EqualFold(rsc.target.stage, "prod") && !p.force {
		return fmt.Errorf("%w: target is prod — refusing to auto-deploy without --force", ErrUsage)
	}

	// Ctrl+C ends the watcher cleanly: the SIGINT handler cancels a derived
	// context, the loop's ctx.Done() fires, and RunWatch returns nil (the
	// summary frame, not an error) — stopping a watcher is its natural end.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	defer func() {
		signal.Stop(sigChan)
		close(sigChan)
	}()
	go func() {
		for range sigChan {
			cancel()
		}
	}()

	opts.log("INFO", "", "watching branch", rsc.prof.DBName,
		[2]string{"branch", p.branch}, [2]string{"tip", shortSHA(tip)},
		[2]string{"target", targetLabel(rsc)}, [2]string{"interval", p.interval.String()})

	baseline := tip
	cycles, deployed := 0, 0
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			opts.log("INFO", "", "watch stopped", rsc.prof.DBName,
				[2]string{"cycles", strconv.Itoa(cycles)},
				[2]string{"deployed", strconv.Itoa(deployed)})
			return nil
		case <-ticker.C:
			newTip, err := gitRevParse(ctx, opts.Root, ref)
			if err != nil {
				// The branch vanished (deleted/renamed) — unrecoverable.
				return fmt.Errorf("branch %q is gone: %w", p.branch, err)
			}
			if newTip == baseline {
				continue
			}
			n, cerr := watchCycle(ctx, opts, rsc, p.from, baseline, newTip)
			baseline = newTip // always re-baseline, even on failure
			if cerr != nil {
				opts.log("ERROR", "cycle", "cycle failed", rsc.prof.DBName,
					[2]string{"err", cerr.Error()})
				continue
			}
			if n > 0 {
				cycles++
				deployed += n
			}
		}
	}
}

// watchCycle runs one advance: fast-forward check → range commits → modules →
// push committed content → headless deploy. Returns the number of commits
// deployed (0 when the range had nothing deployable or the branch was
// rewritten).
func watchCycle(ctx context.Context, opts WatchOpts, rsc remoteShellContext, from, old, new string) (int, error) {
	if !isFastForward(ctx, opts.Root, old, new) {
		opts.log("WARNING", "cycle", "branch rewritten — re-baselining, nothing deployed", rsc.prof.DBName,
			[2]string{"from", shortSHA(old)}, [2]string{"to", shortSHA(new)})
		return 0, nil
	}
	commits, err := rangeCommits(ctx, opts.Root, old, new)
	if err != nil {
		return 0, err
	}
	if len(commits) == 0 {
		return 0, nil
	}

	seen := map[string]bool{}
	var modules, shas []string
	for _, c := range commits {
		mod, _, reason, _ := resolveCommitModule(ctx, opts.Root, c)
		if mod == "" {
			opts.log("WARNING", "cycle", "skipped", rsc.prof.DBName,
				[2]string{"commit", c.short()}, [2]string{"reason", reason})
			continue
		}
		shas = append(shas, c.sha)
		if !seen[mod] {
			seen[mod] = true
			modules = append(modules, mod)
		}
	}
	if len(modules) == 0 {
		opts.log("WARNING", "cycle", "no deployable modules in range", rsc.prof.DBName,
			[2]string{"commits", strconv.Itoa(len(commits))})
		return 0, nil
	}
	opts.log("INFO", "cycle", "commits detected", rsc.prof.DBName,
		[2]string{"commits", strconv.Itoa(len(commits))},
		[2]string{"modules", strings.Join(modules, ",")})

	// Push the committed content at <new>, not the working tree — the watcher
	// may sit on a different branch/worktree.
	srcRoot, cleanup, err := archiveModules(ctx, opts.Cfg, opts.Root, new, modules)
	if err != nil {
		return 0, fmt.Errorf("archive: %w", err)
	}
	defer cleanup()

	pushOpts := PushOpts{
		Cfg: opts.Cfg, Root: opts.Root, Palette: opts.Palette,
		Log: opts.Log, StreamOut: opts.StreamOut, OnSync: opts.OnSync,
	}
	if _, err := pushModuleSet(ctx, rsc, pushOpts, modules, srcRoot, false, false); err != nil {
		return 0, fmt.Errorf("push: %w", err)
	}

	if err := deployCommitsHeadless(ctx, opts, from, shas); err != nil {
		return 0, fmt.Errorf("deploy: %w", err)
	}
	opts.log("INFO", "cycle", "cycle ok", rsc.prof.DBName,
		[2]string{"modules", strconv.Itoa(len(modules))},
		[2]string{"commits", strconv.Itoa(len(shas))})
	return len(shas), nil
}

// deployCommitsHeadless runs the Unit 78 non-interactive deploy for the given
// SHAs against the same target, with --force (the watcher already gated prod
// at startup). Deploy's history marks the SHAs, so re-runs never redeploy.
func deployCommitsHeadless(ctx context.Context, opts WatchOpts, from string, shas []string) error {
	args := []string{"--commits", strings.Join(shas, ","), "--force"}
	if from != "" {
		args = append(args, "--from", from)
	}
	_, err := RunDeploy(ctx, DeployOpts{
		Cfg: opts.Cfg, Root: opts.Root, Args: args, Palette: opts.Palette,
		Log: opts.Log, StreamOut: opts.StreamOut,
	})
	return err
}

// gitRevParse resolves a ref to its full SHA, erroring if it doesn't exist.
func gitRevParse(ctx context.Context, root, ref string) (string, error) {
	out, err := gitOutput(ctx, root, "rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// isFastForward reports whether old is an ancestor of new (a clean advance).
// A rebase/amend/reset makes old no longer an ancestor.
func isFastForward(ctx context.Context, root, old, new string) bool {
	_, err := gitOutput(ctx, root, "merge-base", "--is-ancestor", old, new)
	return err == nil
}

// rangeCommits lists the commits in old..new, newest first.
func rangeCommits(ctx context.Context, root, old, new string) ([]deployCommit, error) {
	out, err := gitOutput(ctx, root, "log", old+".."+new, "--pretty=format:%H%x1f%s")
	if err != nil {
		return nil, fmt.Errorf("git log range: %w", err)
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

// archiveModules extracts the given modules' trees at sha into a fresh temp
// dir (via `git archive`), returning that dir as a push source root plus a
// cleanup func. The committed content — not the working tree — is what ships.
func archiveModules(ctx context.Context, cfg *config.Config, root, sha string, modules []string) (string, func(), error) {
	var paths []string
	for _, m := range modules {
		sub, err := localAddonsSubpath(cfg, root, m)
		if err != nil {
			return "", nil, fmt.Errorf("locate module %q: %w", m, err)
		}
		p := m
		if sub != "." && sub != "" {
			p = sub + "/" + m
		}
		paths = append(paths, p)
	}
	dir, err := os.MkdirTemp("", "echo-watch-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	tarBytes, err := gitOutput(ctx, root, append([]string{"archive", "--format=tar", sha, "--"}, paths...)...)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := extractTar(tarBytes, dir); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

// extractTar unpacks a tar byte stream into dir, creating parent directories
// as needed. Only regular files and directories are handled (git archive
// emits nothing else for a tree).
func extractTar(data []byte, dir string) error {
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// Guard against path traversal from a crafted archive.
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || clean == ".." {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(clean))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}

// targetLabel is the remote's display name for log frames: the named target,
// else the ssh host (a bare --remote resolves the link binding, no name).
func targetLabel(rsc remoteShellContext) string {
	if rsc.fromName != "" {
		return rsc.fromName
	}
	return rsc.sshHost
}

// shortSHA truncates a full SHA to its 7-char prefix.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
