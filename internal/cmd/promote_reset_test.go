package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// newTestRepo creates a git repo with one commit on the default branch and
// returns its path. Identity and hooks are set locally so the test never
// depends on the developer's git config.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Echo Test")
	writeRepoFile(t, dir, "addons/foo/__manifest__.py", "{'name': 'foo'}\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "root")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeRepoFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// resetFixture builds the shape a re-baseline faces: a base branch that moved
// on (touching a.py), and a deploy branch with its own commit plus uncommitted
// work — some of it colliding with the base's changes, some not.
func resetFixture(t *testing.T) (dir, baseSHA string) {
	t.Helper()
	dir = newTestRepo(t)
	writeRepoFile(t, dir, "addons/foo/a.py", "base\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "base moves on")
	baseSHA = runGit(t, dir, "rev-parse", "HEAD")

	runGit(t, dir, "checkout", "-q", "-b", "develop", "HEAD~1")
	writeRepoFile(t, dir, "addons/foo/own.py", "develop only\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "develop's own commit")
	return dir, baseSHA
}

func TestPlanBranchResetSeparatesCollisionsFromKeepers(t *testing.T) {
	dir, baseSHA := resetFixture(t)
	// a.py is what the base changed → colliding. keep.py is untracked work the
	// base knows nothing about → survives. scratch.txt sits outside any module.
	writeRepoFile(t, dir, "addons/foo/a.py", "local edit\n")
	runGit(t, dir, "add", "addons/foo/a.py")
	writeRepoFile(t, dir, "addons/foo/keep.py", "untracked work\n")
	writeRepoFile(t, dir, "scratch.txt", "not a module\n")

	plan, err := planBranchReset(context.Background(), dir, "develop", "main", baseSHA)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !reflect.DeepEqual(plan.collisions, []string{"addons/foo/a.py"}) {
		t.Errorf("collisions = %v, want [addons/foo/a.py]", plan.collisions)
	}
	if !reflect.DeepEqual(plan.untracked, []string{"addons/foo/keep.py"}) {
		t.Errorf("untracked = %v, want only the module-scoped file", plan.untracked)
	}
	if plan.kept != 2 { // keep.py + scratch.txt
		t.Errorf("kept = %d, want 2", plan.kept)
	}
	if len(plan.changes) == 0 {
		t.Error("changes must describe the HEAD → base diff")
	}
}

func TestApplyBranchResetKeepAbortsOnCollision(t *testing.T) {
	dir, baseSHA := resetFixture(t)
	writeRepoFile(t, dir, "addons/foo/a.py", "local edit\n")
	runGit(t, dir, "add", "addons/foo/a.py")
	before := runGit(t, dir, "rev-parse", "HEAD")

	plan, err := planBranchReset(context.Background(), dir, "develop", "main", baseSHA)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	err = applyBranchReset(context.Background(), dir, plan, false)
	if !errors.Is(err, ErrPromoteConflict) {
		t.Fatalf("expected ErrPromoteConflict, got %v", err)
	}
	if now := runGit(t, dir, "rev-parse", "HEAD"); now != before {
		t.Error("a refused reset must not move the branch")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "addons/foo/a.py")); string(got) != "local edit\n" {
		t.Errorf("uncommitted work was destroyed: %q", got)
	}
}

func TestApplyBranchResetKeepPreservesNonCollidingWork(t *testing.T) {
	dir, baseSHA := resetFixture(t)
	writeRepoFile(t, dir, "addons/foo/keep.py", "untracked work\n")

	plan, err := planBranchReset(context.Background(), dir, "develop", "main", baseSHA)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := applyBranchReset(context.Background(), dir, plan, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if now := runGit(t, dir, "rev-parse", "HEAD"); now != baseSHA {
		t.Errorf("HEAD = %s, want the base %s", now, baseSHA)
	}
	if _, err := os.Stat(filepath.Join(dir, "addons/foo/keep.py")); err != nil {
		t.Error("non-colliding untracked work must survive a default reset")
	}
	// develop's own commit is gone from the branch (still in the reflog).
	if runGit(t, dir, "log", "--oneline", "-n", "20") == "" {
		t.Error("unexpected empty log")
	}
	if strings.Contains(runGit(t, dir, "log", "--pretty=%s", "-n", "5"), "develop's own commit") {
		t.Error("the branch should no longer carry its own commit after a reset onto the base")
	}
}

func TestApplyBranchResetDiscardScopesCleanToModules(t *testing.T) {
	dir, baseSHA := resetFixture(t)
	writeRepoFile(t, dir, "addons/foo/a.py", "local edit\n")
	runGit(t, dir, "add", "addons/foo/a.py")
	writeRepoFile(t, dir, "addons/foo/keep.py", "untracked module work\n")
	writeRepoFile(t, dir, "scratch.txt", "not a module\n")

	plan, err := planBranchReset(context.Background(), dir, "develop", "main", baseSHA)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := applyBranchReset(context.Background(), dir, plan, true); err != nil {
		t.Fatalf("apply --discard: %v", err)
	}
	if now := runGit(t, dir, "rev-parse", "HEAD"); now != baseSHA {
		t.Errorf("HEAD = %s, want the base %s", now, baseSHA)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "addons/foo/a.py")); string(got) != "base\n" {
		t.Errorf("a.py = %q, want the base's content", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "addons/foo/keep.py")); !os.IsNotExist(err) {
		t.Error("module-scoped untracked file should have been removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "scratch.txt")); err != nil {
		t.Error("a file outside any module is not this command's business — it must survive")
	}
}

func TestGitAheadBehind(t *testing.T) {
	dir, baseSHA := resetFixture(t)
	ahead, behind, err := gitAheadBehind(context.Background(), dir, baseSHA, "develop")
	if err != nil {
		t.Fatalf("ahead/behind: %v", err)
	}
	if ahead != 1 || behind != 1 {
		t.Errorf("ahead=%d behind=%d, want 1/1", ahead, behind)
	}
}

func TestDescribeMove(t *testing.T) {
	dir, baseSHA := resetFixture(t)
	head := runGit(t, dir, "rev-parse", "HEAD")
	root := runGit(t, dir, "rev-parse", "HEAD~1")

	if got := describeMove(context.Background(), dir, root, head); got != "ahead" {
		t.Errorf("root → head = %q, want ahead", got)
	}
	if got := describeMove(context.Background(), dir, head, root); got != "behind" {
		t.Errorf("head → root = %q, want behind", got)
	}
	if got := describeMove(context.Background(), dir, head, baseSHA); got != "diverged" {
		t.Errorf("develop → base = %q, want diverged", got)
	}
	if got := describeMove(context.Background(), dir, head, head); got != "same" {
		t.Errorf("head → head = %q, want same", got)
	}
	if got := describeMove(context.Background(), dir, "0000000000000000000000000000000000000000", head); got != "unknown" {
		t.Errorf("absent commit = %q, want unknown", got)
	}
}

func TestResolveLocalRefNoFetch(t *testing.T) {
	dir, _ := resetFixture(t)
	nop := func(level, sub, msg, db string, fields ...[2]string) {}

	res, err := resolveLocalRef(context.Background(), nop, dir, "main", false, true)
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	if res.sha != runGit(t, dir, "rev-parse", "main") {
		t.Errorf("sha = %s", res.sha)
	}
	if res.fetched != "" {
		t.Errorf("no fetch expected, got %q", res.fetched)
	}
	if _, err := resolveLocalRef(context.Background(), nop, dir, "nope/nothing", false, true); !errors.Is(err, ErrUsage) {
		t.Errorf("unresolvable ref should be ErrUsage, got %v", err)
	}
}

func TestRemoteOfRef(t *testing.T) {
	dir, _ := resetFixture(t)
	if got := remoteOfRef(context.Background(), dir, "origin/main"); got != "" {
		t.Errorf("no remote configured yet, got %q", got)
	}
	runGit(t, dir, "remote", "add", "origin", "https://example.invalid/repo.git")
	if got := remoteOfRef(context.Background(), dir, "origin/main"); got != "origin" {
		t.Errorf("got %q, want origin", got)
	}
	if got := remoteOfRef(context.Background(), dir, "main"); got != "" {
		t.Errorf("a local branch names no remote, got %q", got)
	}
}

func TestParsePromoteResetFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    promoteArgs
		wantErr bool
	}{
		{name: "bare reset", args: []string{"--reset"}, want: promoteArgs{reset: true}},
		{
			name: "reset with base and discard",
			args: []string{"--reset", "origin/main", "--discard"},
			want: promoteArgs{reset: true, base: "origin/main", discard: true},
		},
		{
			name: "reset targets an explicit branch",
			args: []string{"--reset", "main", "--to", "develop", "--no-fetch"},
			want: promoteArgs{reset: true, base: "main", to: "develop", noFetch: true},
		},
		{name: "set-base", args: []string{"--set-base", "origin/main"}, want: promoteArgs{setBase: "origin/main"}},
		{name: "reset with --dirty", args: []string{"--reset", "--dirty"}, wantErr: true},
		{name: "reset with --commits", args: []string{"--reset", "--commits", "abc"}, wantErr: true},
		{name: "reset with two bases", args: []string{"--reset", "main", "develop"}, wantErr: true},
		{name: "set-base with reset", args: []string{"--set-base", "main", "--reset"}, wantErr: true},
		{name: "discard without reset", args: []string{"--discard"}, wantErr: true},
		{name: "show-branch with reset", args: []string{"--show-branch", "--reset"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePromoteArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestResetPlanDiscardCountDedupes(t *testing.T) {
	// An untracked file the base also introduces shows up in both lists; a
	// discard removes one file, not two.
	plan := resetPlan{
		collisions: []string{"addons/foo/a.py", "addons/foo/b.py"},
		untracked:  []string{"addons/foo/a.py", "addons/foo/c.py"},
	}
	if got := plan.discardCount(); got != 3 {
		t.Errorf("discardCount = %d, want 3", got)
	}
}
