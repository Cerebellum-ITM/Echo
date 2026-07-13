package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParseWatchArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		branch   string
		interval time.Duration
		from     string
		remote   bool
		force    bool
		wantErr  bool
	}{
		{"branch only", []string{"dev"}, "dev", defaultWatchInterval, "", false, false, false},
		{"from + interval", []string{"dev", "--from", "prod", "--interval", "30"}, "dev", 30 * time.Second, "prod", false, false, false},
		{"interval clamped to min", []string{"dev", "--interval", "1"}, "dev", minWatchInterval, "", false, false, false},
		{"remote + force", []string{"dev", "--remote", "--force"}, "dev", defaultWatchInterval, "", true, true, false},
		{"from-value not the branch", []string{"--from", "prod", "dev"}, "dev", defaultWatchInterval, "prod", false, false, false},
		{"omitted branch → picker", []string{"--from", "prod"}, "", defaultWatchInterval, "prod", false, false, false},
		{"no args → picker", nil, "", defaultWatchInterval, "", false, false, false},
		{"two branches", []string{"dev", "main"}, "", 0, "", false, false, true},
		{"bad interval", []string{"dev", "--interval", "x"}, "", 0, "", false, false, true},
		{"unknown flag", []string{"dev", "--nope"}, "", 0, "", false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parseWatchArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseWatchArgs(%v) err = nil, want error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWatchArgs(%v) err = %v", tc.args, err)
			}
			if p.branch != tc.branch || p.interval != tc.interval || p.from != tc.from ||
				p.remote != tc.remote || p.force != tc.force {
				t.Errorf("parseWatchArgs(%v) = %+v; want branch=%q interval=%v from=%q remote=%v force=%v",
					tc.args, p, tc.branch, tc.interval, tc.from, tc.remote, tc.force)
			}
		})
	}
}

// gitScratchRepo builds a throwaway repo with an addon module and returns its
// path plus a commit helper.
func gitScratchRepo(t *testing.T) (root string, commit func(msg string) string) {
	t.Helper()
	root = t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	mustWrite(t, filepath.Join(root, "addons", "sale", "__manifest__.py"), "{'name': 'sale'}")
	run("add", "-A")
	run("commit", "-qm", "init")

	commit = func(msg string) string {
		mustWrite(t, filepath.Join(root, "addons", "sale", "models.py"), msg)
		run("add", "-A")
		run("commit", "-qm", msg)
		out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatalf("rev-parse: %v", err)
		}
		return string(out[:len(out)-1])
	}
	return root, commit
}

func TestGitLocalBranches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	root, commit := gitScratchRepo(t)
	commit("[ADD] sale: base")
	// Create a feature branch with a newer commit so it sorts before main.
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("branch", "feature/x")
	run("checkout", "-q", "feature/x")
	commit("[ADD] sale: feature work")

	branches, err := gitLocalBranches(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 2 {
		t.Fatalf("gitLocalBranches = %v, want 2 branches", branches)
	}
	// feature/x has the most recent commit → sorted first.
	if branches[0] != "feature/x" {
		t.Errorf("most recent branch first: got %v", branches)
	}
	want := map[string]bool{"main": true, "feature/x": true}
	for _, b := range branches {
		if !want[b] {
			t.Errorf("unexpected branch %q in %v", b, branches)
		}
	}
}

func TestIsFastForwardAndRangeCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	root, commit := gitScratchRepo(t)

	old, err := gitRevParse(ctx, root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	a := commit("[ADD] sale: one")
	b := commit("[ADD] sale: two")

	// Linear advance: old is an ancestor of b, range old..b has 2 commits.
	if !isFastForward(ctx, root, old, b) {
		t.Error("isFastForward(old, b) = false, want true (linear)")
	}
	commits, err := rangeCommits(ctx, root, old, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("rangeCommits = %d commits, want 2", len(commits))
	}
	if commits[0].sha != b || commits[1].sha != a {
		t.Errorf("rangeCommits order = [%s %s], want newest-first [%s %s]",
			commits[0].short(), commits[1].short(), b[:7], a[:7])
	}

	// Amend rewrites the tip: old is no longer an ancestor of the new HEAD.
	amendRun := exec.Command("git", "-C", root, "commit", "--amend", "-qm", "amended")
	amendRun.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := amendRun.CombinedOutput(); err != nil {
		t.Fatalf("amend: %v\n%s", err, out)
	}
	amended, _ := gitRevParse(ctx, root, "HEAD")
	if isFastForward(ctx, root, b, amended) {
		t.Error("isFastForward(b, amended) = true, want false (rewritten)")
	}
}

func TestArchiveModules(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	root, commit := gitScratchRepo(t)
	commit("first content")
	sha := commit("[ADD] sale: second content")

	cfg := &config.Config{AddonsPaths: []string{"addons"}}
	dir, cleanup, err := archiveModules(ctx, cfg, root, sha, []string{"sale"})
	if err != nil {
		t.Fatalf("archiveModules: %v", err)
	}
	defer cleanup()

	// The extracted tree holds the module at its repo-relative path with the
	// content committed at <sha>.
	got, err := os.ReadFile(filepath.Join(dir, "addons", "sale", "models.py"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(got) != "[ADD] sale: second content" {
		t.Errorf("extracted content = %q, want the committed content at sha", string(got))
	}
	// The manifest ships too.
	if _, err := os.Stat(filepath.Join(dir, "addons", "sale", "__manifest__.py")); err != nil {
		t.Errorf("manifest missing from archive: %v", err)
	}
	// moduleSrcDir resolves the module inside the scratch root.
	if _, err := moduleSrcDir(cfg, dir, "sale"); err != nil {
		t.Errorf("moduleSrcDir on scratch root: %v", err)
	}
}

func TestParseWatchArgsNoLogs(t *testing.T) {
	p, err := parseWatchArgs([]string{"dev", "--no-logs"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.noLogs || p.branch != "dev" {
		t.Fatalf("--no-logs: noLogs=%v branch=%q, want true/dev", p.noLogs, p.branch)
	}
	if q, _ := parseWatchArgs([]string{"dev"}); q.noLogs {
		t.Fatal("noLogs should default to false")
	}
}

func TestParseWatchArgsNoCheckpoint(t *testing.T) {
	p, err := parseWatchArgs([]string{"dev", "--no-checkpoint"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.noCheckpoint || p.branch != "dev" {
		t.Fatalf("--no-checkpoint: noCheckpoint=%v branch=%q, want true/dev", p.noCheckpoint, p.branch)
	}
	if q, _ := parseWatchArgs([]string{"dev"}); q.noCheckpoint {
		t.Fatal("noCheckpoint should default to false")
	}
}

func noopWatchOpts() WatchOpts {
	return WatchOpts{
		Log:       func(string, string, string, string, ...[2]string) {},
		StreamOut: func(string) {},
	}
}

func fakeRemoteShell() remoteShellContext {
	return remoteShellContext{
		remotePath: "/srv/app",
		target:     connectTarget{composeCmd: "docker compose", odooContainer: "odoo"},
	}
}

// TestStartWatchLogsLifecycle: the follower opens the stream with the given
// --tail, and stop() cancels it and blocks until the stream goroutine has
// actually returned (the "no interleaving" guarantee).
func TestStartWatchLogsLifecycle(t *testing.T) {
	orig := watchLogStream
	defer func() { watchLogStream = orig }()

	started := make(chan string, 1)
	exited := make(chan struct{})
	watchLogStream = func(ctx context.Context, host, remoteCmd string, stdin []byte, onLine func(string)) error {
		started <- remoteCmd
		<-ctx.Done()
		close(exited)
		return ctx.Err()
	}

	stop := startWatchLogs(context.Background(), noopWatchOpts(), fakeRemoteShell(), 20*time.Millisecond, "20")
	cmd := <-started
	if !strings.Contains(cmd, "'--tail' '20'") || !strings.Contains(cmd, "'odoo'") {
		t.Fatalf("first follow cmd = %q, want --tail 20 on the odoo service", cmd)
	}

	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("stop() did not cancel the stream")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop() returned before the stream goroutine exited")
	}
}

// TestStartWatchLogsRetry: an unexpected stream end (nil return, live ctx)
// re-opens the follow with --tail 0 after the interval.
func TestStartWatchLogsRetry(t *testing.T) {
	orig := watchLogStream
	defer func() { watchLogStream = orig }()

	calls := make(chan string, 4)
	var mu sync.Mutex
	n := 0
	watchLogStream = func(ctx context.Context, host, remoteCmd string, stdin []byte, onLine func(string)) error {
		calls <- remoteCmd
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if first {
			return nil // ended on its own → should trigger a retry
		}
		<-ctx.Done()
		return ctx.Err()
	}

	stop := startWatchLogs(context.Background(), noopWatchOpts(), fakeRemoteShell(), 10*time.Millisecond, "20")
	c1 := <-calls
	c2 := <-calls
	stop()

	if !strings.Contains(c1, "'--tail' '20'") {
		t.Fatalf("first call = %q, want --tail 20", c1)
	}
	if !strings.Contains(c2, "'--tail' '0'") {
		t.Fatalf("retry call = %q, want --tail 0", c2)
	}
}

// TestStartWatchLogsCancelNoRetry: a cancelled context ends the follower
// without a retry.
func TestStartWatchLogsCancelNoRetry(t *testing.T) {
	orig := watchLogStream
	defer func() { watchLogStream = orig }()

	blocked := make(chan struct{}, 2)
	watchLogStream = func(ctx context.Context, host, remoteCmd string, stdin []byte, onLine func(string)) error {
		blocked <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}

	stop := startWatchLogs(context.Background(), noopWatchOpts(), fakeRemoteShell(), 10*time.Millisecond, "20")
	<-blocked
	stop() // cancel while the (only) stream is live → no retry
	// Give a couple intervals for any erroneous retry to fire.
	time.Sleep(40 * time.Millisecond)
	select {
	case <-blocked:
		t.Fatal("follower retried after a cancelled context")
	default:
	}
}
