package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParsePromoteArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    promoteArgs
		wantErr bool
	}{
		{
			name: "dirty with folders",
			args: []string{"--dirty", "mod_a", "mod_b"},
			want: promoteArgs{dirty: true, folders: []string{"mod_a", "mod_b"}},
		},
		{
			name: "dirty with --to",
			args: []string{"--dirty", "--to", "pruebas"},
			want: promoteArgs{dirty: true, to: "pruebas"},
		},
		{
			name: "branch with commits",
			args: []string{"feature-x", "--commits", "abc123,def456"},
			want: promoteArgs{branch: "feature-x", commits: []string{"abc123", "def456"}},
		},
		{
			name: "set-branch standalone",
			args: []string{"--set-branch", "develop"},
			want: promoteArgs{setBranch: "develop"},
		},
		{
			name: "dry-run branch",
			args: []string{"feature-x", "--dry-run"},
			want: promoteArgs{branch: "feature-x", dryRun: true},
		},
		{
			name:    "dirty + commits rejected",
			args:    []string{"--dirty", "--commits", "abc"},
			wantErr: true,
		},
		{
			name:    "commits without branch rejected",
			args:    []string{"--commits", "abc"},
			wantErr: true,
		},
		{
			name:    "set-branch with mode rejected",
			args:    []string{"--set-branch", "develop", "--dirty"},
			wantErr: true,
		},
		{
			name:    "two source branches rejected",
			args:    []string{"feature-x", "feature-y"},
			wantErr: true,
		},
		{
			name:    "unknown flag rejected",
			args:    []string{"--nope"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePromoteArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parsePromoteArgs(%v) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestParseWorktrees(t *testing.T) {
	out := "worktree /repo/main\nHEAD aaaa\nbranch refs/heads/develop\n\n" +
		"worktree /repo/feat\nHEAD bbbb\nbranch refs/heads/feature-x\n\n" +
		"worktree /repo/detached\nHEAD cccc\ndetached\n"
	got := parseWorktrees(out)
	want := []gitWorktree{
		{path: "/repo/main", branch: "develop", head: "aaaa"},
		{path: "/repo/feat", branch: "feature-x", head: "bbbb"},
		{path: "/repo/detached", branch: "", head: "cccc"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseWorktrees = %+v, want %+v", got, want)
	}

	if w, ok := worktreeForBranch(got, "develop"); !ok || w.path != "/repo/main" {
		t.Errorf("worktreeForBranch(develop) = %+v, %v", w, ok)
	}
	if _, ok := worktreeForBranch(got, "missing"); ok {
		t.Errorf("worktreeForBranch(missing) should be false")
	}
}

func TestPromoteDestBranch(t *testing.T) {
	tests := []struct {
		name string
		p    promoteArgs
		cfg  *config.Config
		want string
	}{
		{"flag wins", promoteArgs{to: "flagbr"}, &config.Config{PromoteBranch: "cfgbr"}, "flagbr"},
		{"config next", promoteArgs{}, &config.Config{PromoteBranch: "cfgbr"}, "cfgbr"},
		{"none → empty (prompt)", promoteArgs{}, &config.Config{}, ""},
		{"nil cfg → empty", promoteArgs{}, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promoteDestBranch(tt.p, tt.cfg); got != tt.want {
				t.Errorf("promoteDestBranch = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterCommits(t *testing.T) {
	eligible := []deployCommit{
		{sha: "a1", subject: "first"},
		{sha: "b2", subject: "second"},
		{sha: "c3", subject: "third"},
	}
	got := filterCommits(eligible, map[string]bool{"c3": true, "a1": true})
	// Preserves chronological (eligible) order, not selection order.
	if len(got) != 2 || got[0].sha != "a1" || got[1].sha != "c3" {
		t.Fatalf("filterCommits = %+v, want [a1 c3] in order", got)
	}
}

func TestSameWorktree(t *testing.T) {
	if !sameWorktree("/a/b", "/a/b/") {
		t.Error("trailing slash should compare equal")
	}
	if sameWorktree("/a/b", "/a/c") {
		t.Error("different paths should not be equal")
	}
}

// --- integration: real temp git repo with worktrees ---

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// setupPromoteRepo builds a repo with a committed module on `develop`, then a
// `feature` worktree branched off it. Returns (mainDir, featureDir).
func setupPromoteRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	base := t.TempDir()
	main := filepath.Join(base, "main")
	if err := os.MkdirAll(filepath.Join(main, "my_mod"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, base, "init", "-b", "develop", "main")
	writeFile(t, filepath.Join(main, "my_mod", "__manifest__.py"), "{'name': 'my_mod'}\n")
	writeFile(t, filepath.Join(main, "my_mod", "models.py"), "x = 1\n")
	gitRun(t, main, "add", "-A")
	gitRun(t, main, "commit", "-m", "init module")

	feat := filepath.Join(base, "feature")
	gitRun(t, main, "worktree", "add", "-b", "feature", feat)
	return main, feat
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteDirtyIntegration(t *testing.T) {
	main, feat := setupPromoteRepo(t)
	ctx := context.Background()

	// Dirty the feature worktree: modify a tracked file + add an untracked one.
	writeFile(t, filepath.Join(feat, "my_mod", "models.py"), "x = 2\n")
	writeFile(t, filepath.Join(feat, "my_mod", "views.xml"), "<odoo/>\n")

	// git worktree list from the feature worktree finds `develop` in main.
	wts, err := gitWorktrees(ctx, feat)
	if err != nil {
		t.Fatal(err)
	}
	dest, ok := worktreeForBranch(wts, "develop")
	if !ok {
		t.Fatalf("develop worktree not found in %+v", wts)
	}

	changes, err := applyDirty(ctx, feat, dest.path, []string{"my_mod"}, false)
	if err != nil {
		t.Fatalf("applyDirty: %v", err)
	}

	// The destination (main/develop) now carries both changes, uncommitted.
	if got := readFile(t, filepath.Join(main, "my_mod", "models.py")); got != "x = 2\n" {
		t.Errorf("tracked change not applied: %q", got)
	}
	if got := readFile(t, filepath.Join(main, "my_mod", "views.xml")); got != "<odoo/>\n" {
		t.Errorf("untracked file not copied: %q", got)
	}
	var ops []string
	for _, c := range changes {
		ops = append(ops, c.Op+":"+filepath.Base(c.Path))
	}
	sort.Strings(ops)
	want := []string{"changed:models.py", "new:views.xml"}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("changes = %v, want %v", ops, want)
	}

	// develop was never committed to — it should be dirty now.
	st := gitStatus(t, main)
	if st == "" {
		t.Error("expected develop worktree to be dirty after promote")
	}
}

func TestPromoteCommitsIntegration(t *testing.T) {
	main, feat := setupPromoteRepo(t)
	ctx := context.Background()

	// Two commits on feature.
	writeFile(t, filepath.Join(feat, "my_mod", "models.py"), "x = 10\n")
	gitRun(t, feat, "add", "-A")
	gitRun(t, feat, "commit", "-m", "feat one")
	writeFile(t, filepath.Join(feat, "my_mod", "data.xml"), "<data/>\n")
	gitRun(t, feat, "add", "-A")
	gitRun(t, feat, "commit", "-m", "feat two")

	eligible, err := eligibleCommits(ctx, feat, "develop", "feature")
	if err != nil {
		t.Fatal(err)
	}
	if len(eligible) != 2 {
		t.Fatalf("eligible = %d commits, want 2", len(eligible))
	}
	// Oldest-first: "feat one" then "feat two".
	if eligible[0].subject != "feat one" || eligible[1].subject != "feat two" {
		t.Errorf("order wrong: %q, %q", eligible[0].subject, eligible[1].subject)
	}

	shas := []string{eligible[0].sha, eligible[1].sha}
	if err := cherryPickInto(ctx, main, shas); err != nil {
		t.Fatalf("cherryPickInto: %v", err)
	}
	if got := readFile(t, filepath.Join(main, "my_mod", "data.xml")); got != "<data/>\n" {
		t.Errorf("cherry-picked file missing: %q", got)
	}

	// Re-running eligibleCommits now finds nothing (both are on develop).
	again, err := eligibleCommits(ctx, feat, "develop", "feature")
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("expected 0 eligible after promote, got %d", len(again))
	}
}

// promote is a file-level move: the source's current version of a changed file
// overwrites the destination's, whatever git state the destination is in
// (untracked, dirty, or divergent). No index consulted, so the "does not exist
// in index" / "does not match index" failures a `git apply` produced can't
// happen. Last-write-wins is the deliberate trade-off (the caller warns when it
// clobbers uncommitted destination work — see destDirtyPaths).
func TestApplyDirtyOverwritesDivergentDest(t *testing.T) {
	main, feat := setupPromoteRepo(t)
	ctx := context.Background()

	// The destination has committed a different version of the file …
	writeFile(t, filepath.Join(main, "my_mod", "models.py"), "x = 999\n")
	gitRun(t, main, "add", "-A")
	gitRun(t, main, "commit", "-m", "diverge on develop")
	// … and the module the source promotes is even brand-new to the destination
	// (no index entry) — exactly what broke git apply.
	writeFile(t, filepath.Join(feat, "my_mod", "models.py"), "x = 2\n")
	writeFile(t, filepath.Join(feat, "new_mod", "__manifest__.py"), "{'name':'new_mod'}\n")
	writeFile(t, filepath.Join(feat, "new_mod", "models.py"), "y = 1\n")

	wts, _ := gitWorktrees(ctx, feat)
	dest, _ := worktreeForBranch(wts, "develop")

	if _, err := applyDirty(ctx, feat, dest.path, []string{"my_mod", "new_mod"}, false); err != nil {
		t.Fatalf("applyDirty overwrite: %v", err)
	}
	// Modified file: source version wins (overwrite).
	if got := readFile(t, filepath.Join(main, "my_mod", "models.py")); got != "x = 2\n" {
		t.Errorf("modified file not overwritten: %q", got)
	}
	// Brand-new module (untracked in dest) landed too.
	if got := readFile(t, filepath.Join(main, "new_mod", "__manifest__.py")); got != "{'name':'new_mod'}\n" {
		t.Errorf("new module not copied: %q", got)
	}
}

func TestApplyDirtyDeletionRemovesDest(t *testing.T) {
	main, feat := setupPromoteRepo(t)
	ctx := context.Background()

	// Source deletes a tracked file; promote removes it from the destination.
	if err := os.Remove(filepath.Join(feat, "my_mod", "models.py")); err != nil {
		t.Fatal(err)
	}
	wts, _ := gitWorktrees(ctx, feat)
	dest, _ := worktreeForBranch(wts, "develop")

	if _, err := applyDirty(ctx, feat, dest.path, []string{"my_mod"}, false); err != nil {
		t.Fatalf("applyDirty deletion: %v", err)
	}
	if _, err := os.Stat(filepath.Join(main, "my_mod", "models.py")); !os.IsNotExist(err) {
		t.Errorf("deleted file still present on destination (err=%v)", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	c := exec.Command("git", "status", "--porcelain")
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	return string(out)
}
