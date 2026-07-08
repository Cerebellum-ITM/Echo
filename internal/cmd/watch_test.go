package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
		{"missing branch", []string{"--from", "prod"}, "", 0, "", false, false, true},
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
