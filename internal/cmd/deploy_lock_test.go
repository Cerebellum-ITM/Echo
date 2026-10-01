package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

// fakeLockRemote keeps the remote lock file in memory behind lockRunSSH.
type fakeLockRemote struct {
	file     string
	tracked  string
	writes   int
	readErr  error
	writeErr error
}

func (f *fakeLockRemote) install(t *testing.T) {
	t.Helper()
	orig := lockRunSSH
	lockRunSSH = func(_ context.Context, _ string, remoteCmd string, stdin []byte) ([]byte, error) {
		if strings.HasPrefix(remoteCmd, "cat ") {
			return []byte(f.file), f.readErr
		}
		if f.writeErr != nil {
			return nil, f.writeErr
		}
		f.file = string(stdin)
		f.writes++
		return []byte(f.tracked), nil
	}
	t.Cleanup(func() { lockRunSSH = orig })
}

type logRecorder struct{ lines []string }

func (r *logRecorder) log(level, sub, msg, db string, fields ...[2]string) {
	var b strings.Builder
	b.WriteString(level + " " + sub + " " + msg)
	for _, f := range fields {
		b.WriteString(" " + f[0] + "=" + f[1])
	}
	r.lines = append(r.lines, b.String())
}

func (r *logRecorder) has(sub string) bool {
	for _, l := range r.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func lockTestRSC() remoteShellContext {
	return remoteShellContext{sshHost: "host", remotePath: "/srv/odoo", fromName: "staging"}
}

func TestReadDeployLockMissingAndUnparsable(t *testing.T) {
	ctx := context.Background()
	fake := &fakeLockRemote{}
	fake.install(t)

	lock, found := readDeployLock(ctx, lockTestRSC(), nil)
	if found || lock.Modules == nil || lock.Schema != lockSchema {
		t.Fatalf("missing lock: found=%v lock=%+v", found, lock)
	}

	fake.file = "{not json"
	rec := &logRecorder{}
	if _, found := readDeployLock(ctx, lockTestRSC(), rec.log); found {
		t.Error("unparsable lock reported as found")
	}
	if !rec.has("unreadable") {
		t.Errorf("no warning for an unparsable lock: %v", rec.lines)
	}
}

func TestUpdateDeployLockRoundTrip(t *testing.T) {
	ctx := context.Background()
	fake := &fakeLockRemote{}
	fake.install(t)
	rsc := lockTestRSC()

	shipped := map[string]LockModule{
		"sale":  {Source: lockSourceWorktree, SHA: "aaa"},
		"stock": {Source: lockSourceBranch, SHA: "bbb"},
	}
	updateDeployLock(ctx, rsc, nil, func(l *DeployLock) { l.record(shipped) })
	updateDeployLock(ctx, rsc, nil, func(l *DeployLock) { l.markVerified(shipped) })

	lock, found := readDeployLock(ctx, rsc, nil)
	if !found || lock.Target != "staging" || len(lock.Modules) != 2 {
		t.Fatalf("after record: found=%v lock=%+v", found, lock)
	}
	if !lock.Modules["sale"].Verified || !lock.Modules["stock"].Verified {
		t.Errorf("markVerified did not flip the shipped modules: %+v", lock.Modules)
	}

	updateDeployLock(ctx, rsc, nil, func(l *DeployLock) { l.forget([]string{"sale"}) })
	lock, _ = readDeployLock(ctx, rsc, nil)
	if _, ok := lock.Modules["sale"]; ok {
		t.Error("forget left the module in the lock")
	}
}

func TestDeployLockRebase(t *testing.T) {
	fresh := func() DeployLock {
		l := newDeployLock("dev")
		l.record(map[string]LockModule{
			"sale":  {Source: lockSourceBranch},
			"stock": {Source: lockSourceWorktree},
		})
		return l
	}
	base := LockBase{Branch: "echo/deploy", SHA: "ccc"}

	kept := fresh()
	kept.rebase(base, true)
	if _, ok := kept.Modules["sale"]; ok {
		t.Error("rebase kept a branch entry the move superseded")
	}
	if _, ok := kept.Modules["stock"]; !ok {
		t.Error("rebase with keepOverlay dropped an overlay entry")
	}
	if kept.Base == nil || kept.Base.SHA != "ccc" {
		t.Errorf("base not recorded: %+v", kept.Base)
	}

	cleaned := fresh()
	cleaned.rebase(base, false)
	if len(cleaned.Modules) != 0 {
		t.Errorf("rebase without keepOverlay left entries: %+v", cleaned.Modules)
	}
}

func TestWriteDeployLockWarnings(t *testing.T) {
	ctx := context.Background()

	fake := &fakeLockRemote{tracked: ".echo/lock.json\n"}
	fake.install(t)
	rec := &logRecorder{}
	writeDeployLock(ctx, lockTestRSC(), newDeployLock("staging"), rec.log)
	if !rec.has("git rm --cached -r .echo") {
		t.Errorf("tracked .echo/ not reported: %v", rec.lines)
	}

	fake.writeErr = errors.New("permission denied")
	rec = &logRecorder{}
	writeDeployLock(ctx, lockTestRSC(), newDeployLock("staging"), rec.log)
	if !rec.has("could not write the deploy lock") {
		t.Errorf("write failure not reported as a warning: %v", rec.lines)
	}
}

// TestLockWriteScriptIgnoresItself runs the real write script against a local
// git repository standing in for remote_path.
func TestLockWriteScriptIgnoresItself(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	mustWrite(t, filepath.Join(repo, "README"), "x")
	git("add", "-A")
	git("commit", "-qm", "init")

	run := func(body string) string {
		c := exec.Command("sh", "-c", lockWriteScript(repo))
		c.Stdin = strings.NewReader(body)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("write script: %v\n%s", err, out)
		}
		return string(out)
	}

	if out := run(`{"schema":1}`); strings.TrimSpace(out) != "" {
		t.Errorf("untracked lock reported as tracked: %q", out)
	}
	got, err := os.ReadFile(filepath.Join(repo, lockDirName, "lock.json"))
	if err != nil || string(got) != `{"schema":1}` {
		t.Fatalf("lock content = %q, %v", got, err)
	}
	ignore, _ := os.ReadFile(filepath.Join(repo, lockDirName, ".gitignore"))
	if string(ignore) != "*\n" {
		t.Errorf(".gitignore = %q, want \"*\\n\"", ignore)
	}
	if st := git("status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("git status not clean after the write: %q", st)
	}

	git("add", "-f", lockDirName+"/lock.json")
	git("commit", "-qm", "track the lock by mistake")
	if out := run(`{"schema":1,"target":"x"}`); !strings.Contains(out, ".echo/lock.json") {
		t.Errorf("tracked lock not listed: %q", out)
	}
}

func TestLockEntries(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	root, commit := gitScratchRepo(t)
	mustWrite(t, filepath.Join(root, "addons", "sale", "__manifest__.py"), "{'name': 'sale', 'version': '18.0.1.2.0'}")
	sha := commit("committed content")
	cfg := &config.Config{AddonsPaths: []string{"addons"}}

	mustWrite(t, filepath.Join(root, "addons", "sale", "models.py"), "uncommitted edit")
	wt := lockEntries(ctx, cfg, root, shipSource{kind: lockSourceWorktree, srcRoot: root, via: "push"}, []string{"sale"})["sale"]
	if wt.SHA != sha || !wt.Dirty || wt.Version != "18.0.1.2.0" || wt.Tree != "" || wt.Via != "push" {
		t.Errorf("worktree entry = %+v", wt)
	}

	tree, err := gitOutput(ctx, root, "rev-parse", sha+":addons/sale")
	if err != nil {
		t.Fatal(err)
	}
	dir, cleanup, err := archiveModuleSources(ctx, root, map[string]moduleSource{
		"sale": {kind: lockSourceCommit, sha: sha, path: "addons/sale"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cm := lockEntries(ctx, cfg, root, shipSource{kind: lockSourceCommit, sha: sha, srcRoot: dir, via: "watch"}, []string{"sale"})["sale"]
	if cm.Tree != firstLine(string(tree)) || cm.Dirty || cm.Version != "18.0.1.2.0" {
		t.Errorf("commit entry = %+v, want tree %s", cm, firstLine(string(tree)))
	}

	br := lockEntries(ctx, cfg, root, shipSource{kind: lockSourceBranch, sha: sha, ref: "main", via: "deploy"}, []string{"sale"})["sale"]
	if br.Version != "18.0.1.2.0" || br.Ref != "main" || br.Tree == "" {
		t.Errorf("branch entry = %+v", br)
	}
}

func TestLogCodePlan(t *testing.T) {
	rec := &logRecorder{}
	current := newDeployLock("staging")
	current.record(map[string]LockModule{"sale": {Source: lockSourceWorktree, SHA: "0b6fc41aaaa", Version: "1.0"}})
	shipped := map[string]LockModule{
		"sale":  {Source: lockSourceCommit, SHA: "99f2109bbbb", Version: "1.1"},
		"stock": {Source: lockSourceWorktree, SHA: "99f2109bbbb", Dirty: true},
	}
	logCodePlan(rec.log, "db", shipped, current, map[string]string{"sale": "1.0"})
	want := []string{
		"INFO plan code module=sale ship=commit@99f2109 version=1.1 installed=1.0 locked=worktree@0b6fc41 locked_version=1.0",
		"INFO plan code module=stock ship=worktree@99f2109+dirty locked=none",
	}
	if strings.Join(rec.lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("plan lines:\n%s\nwant:\n%s", strings.Join(rec.lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestParseDeployArgsLock(t *testing.T) {
	a, err := parseDeployArgs([]string{"--lock", "--from", "staging", "--json"})
	if err != nil || !a.lock || a.from != "staging" || !a.jsonOut {
		t.Fatalf("--lock: %+v, %v", a, err)
	}
	for _, args := range [][]string{
		{"--lock", "--modules", "sale"},
		{"--lock", "--push"},
		{"--lock", "--rollback"},
		{"--lock", "--set-code", "main"},
		{"--lock", "--dry-run"},
	} {
		if _, err := parseDeployArgs(args); !errors.Is(err, ErrUsage) {
			t.Errorf("%v: want ErrUsage, got %v", args, err)
		}
	}
}
