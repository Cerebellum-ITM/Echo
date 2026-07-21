package cmd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParseDeployGitFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
		check   func(t *testing.T, a deployArgs)
	}{
		{name: "no-git", args: []string{"--no-git"}, check: func(t *testing.T, a deployArgs) {
			if !a.noGit {
				t.Error("noGit not set")
			}
		}},
		{name: "restore-code space", args: []string{"--restore-code", "abc123"}, check: func(t *testing.T, a deployArgs) {
			if a.restoreCode != "abc123" {
				t.Errorf("restoreCode = %q", a.restoreCode)
			}
		}},
		{name: "restore-code equals", args: []string{"--restore-code=deadbeef"}, check: func(t *testing.T, a deployArgs) {
			if a.restoreCode != "deadbeef" {
				t.Errorf("restoreCode = %q", a.restoreCode)
			}
		}},
		{name: "restore-code needs value", args: []string{"--restore-code"}, wantErr: true},
		{name: "restore-code empty value", args: []string{"--restore-code="}, wantErr: true},
		{name: "restore-code with selection rejected", args: []string{"--restore-code", "x", "--push"}, wantErr: true},
		{name: "restore-code with commits rejected", args: []string{"--restore-code", "x", "--commits", "a,b"}, wantErr: true},
		{name: "no-git and restore-code exclusive", args: []string{"--no-git", "--restore-code", "x"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := parseDeployArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !errors.Is(err, ErrUsage) {
					t.Errorf("error not ErrUsage: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, a)
			}
		})
	}
}

func TestParsePushCleanFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
		check   func(t *testing.T, a pushArgs)
	}{
		{name: "clean bare", args: []string{"--clean"}, check: func(t *testing.T, a pushArgs) {
			if !a.clean {
				t.Error("clean not set")
			}
		}},
		{name: "clean all", args: []string{"--clean", "--all"}, check: func(t *testing.T, a pushArgs) {
			if !a.clean || !a.all {
				t.Error("clean/all not set")
			}
		}},
		{name: "clean with modules", args: []string{"--clean", "sale", "account"}, check: func(t *testing.T, a pushArgs) {
			if !reflect.DeepEqual(a.modules, []string{"sale", "account"}) {
				t.Errorf("modules = %v", a.modules)
			}
		}},
		{name: "clean and dirty exclusive", args: []string{"--clean", "--dirty"}, wantErr: true},
		{name: "clean and delete exclusive", args: []string{"--clean", "--delete"}, wantErr: true},
		{name: "clean and dest exclusive", args: []string{"--clean", "--dest", "/x"}, wantErr: true},
		{name: "all without clean", args: []string{"--all"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := parsePushArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !errors.Is(err, ErrUsage) {
					t.Errorf("error not ErrUsage: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, a)
			}
		})
	}
}

func TestGitCollisions(t *testing.T) {
	cases := []struct {
		name      string
		porcelain []string
		diff      []string
		tree      []string
		want      []string
	}{
		{
			name:      "tracked dirty in diff collides",
			porcelain: []string{" M addons/foo/a.py", " M addons/foo/b.py"},
			diff:      []string{"addons/foo/a.py"},
			tree:      []string{"addons/foo/a.py", "addons/foo/b.py"},
			want:      []string{"addons/foo/a.py"},
		},
		{
			name:      "tracked dirty NOT in diff survives",
			porcelain: []string{" M addons/foo/b.py"},
			diff:      []string{"addons/foo/a.py"},
			tree:      []string{"addons/foo/a.py", "addons/foo/b.py"},
			want:      nil,
		},
		{
			name:      "untracked in tree collides",
			porcelain: []string{"?? addons/foo/new.py"},
			diff:      nil,
			tree:      []string{"addons/foo/new.py"},
			want:      []string{"addons/foo/new.py"},
		},
		{
			name:      "untracked NOT in tree survives",
			porcelain: []string{"?? addons/foo/scratch.log"},
			diff:      nil,
			tree:      []string{"addons/foo/a.py"},
			want:      nil,
		},
		{
			name:      "mixed sorted deduped",
			porcelain: []string{"?? z/new.py", " M a/mod.py", " M a/mod.py"},
			diff:      []string{"a/mod.py"},
			tree:      []string{"z/new.py"},
			want:      []string{"a/mod.py", "z/new.py"},
		},
		{name: "empty", porcelain: nil, diff: nil, tree: nil, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gitCollisions(tc.porcelain, tc.diff, tc.tree)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("gitCollisions = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveGitDeploy(t *testing.T) {
	cfg := &config.Config{
		ConnectTargets: []config.ConnectTarget{
			{Name: "develop", SSHHost: "h1", RemotePath: "/srv/a", GitDeploy: true, GitBranch: "echo/dev", GitPath: "src"},
			{Name: "plain", SSHHost: "h2", RemotePath: "/srv/b", GitDeploy: false},
			{Name: "defbranch", SSHHost: "h3", RemotePath: "/srv/c", GitDeploy: true},
		},
		ConnectGitDeploy: true,
		ConnectGitBranch: "echo/link",
	}
	t.Run("named enabled", func(t *testing.T) {
		g := resolveGitDeploy(cfg, "develop", "", "")
		if !g.enabled || g.branch != "echo/dev" || g.path != "src" {
			t.Errorf("got %+v", g)
		}
	})
	t.Run("named disabled wins over link", func(t *testing.T) {
		if g := resolveGitDeploy(cfg, "plain", "h2", "/srv/b"); g.enabled {
			t.Errorf("expected disabled, got %+v", g)
		}
	})
	t.Run("default branch", func(t *testing.T) {
		if g := resolveGitDeploy(cfg, "defbranch", "", ""); g.branch != defaultDeployBranch {
			t.Errorf("branch = %q", g.branch)
		}
	})
	t.Run("unnamed inherits by host+path match", func(t *testing.T) {
		// A [connect] link binding resolves with no name but the same host+path
		// as `develop` → inherits develop's git config (the interactive case).
		g := resolveGitDeploy(cfg, "", "h1", "/srv/a")
		if !g.enabled || g.branch != "echo/dev" || g.path != "src" {
			t.Errorf("got %+v", g)
		}
	})
	t.Run("unnamed no host match falls to link binding", func(t *testing.T) {
		g := resolveGitDeploy(cfg, "", "hX", "/srv/other")
		if !g.enabled || g.branch != "echo/link" {
			t.Errorf("got %+v", g)
		}
	})
	t.Run("unknown name falls through to link", func(t *testing.T) {
		if g := resolveGitDeploy(cfg, "nope", "hX", "/srv/other"); !g.enabled {
			t.Error("expected link fallthrough enabled")
		}
	})
}

func TestResolveGitTip(t *testing.T) {
	// Non-linear selection: with no real repo, merge-base --is-ancestor fails for
	// every pair, so no candidate is the tip → ErrUsage.
	if _, err := resolveGitTip(context.Background(), t.TempDir(), []string{"aaa", "bbb"}); !errors.Is(err, ErrUsage) {
		t.Errorf("expected ErrUsage for non-linear selection, got %v", err)
	}
	// Single SHA is trivially the tip (no ancestry checks run).
	tip, err := resolveGitTip(context.Background(), t.TempDir(), []string{"onlysha"})
	if err != nil || tip != "onlysha" {
		t.Errorf("single-sha tip = %q, err %v", tip, err)
	}
}

// scriptedGit installs a gitRunSSH stub that answers by matching a substring of
// the remote command, and records every command run. Restores the seam on
// cleanup.
func scriptedGit(t *testing.T, answers map[string]scriptResp) *[]string {
	t.Helper()
	orig := gitRunSSH
	var calls []string
	gitRunSSH = func(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error) {
		calls = append(calls, remoteCmd)
		for sub, r := range answers {
			if strings.Contains(remoteCmd, sub) {
				return []byte(r.out), r.err
			}
		}
		return nil, nil
	}
	t.Cleanup(func() { gitRunSSH = orig })
	return &calls
}

type scriptResp struct {
	out string
	err error
}

func TestGitAdvanceFFGateReject(t *testing.T) {
	calls := scriptedGit(t, map[string]scriptResp{
		"merge-base": {err: errors.New("not an ancestor")},
	})
	err := gitAdvance(context.Background(), testRSC(), "/srv/odoo", "echo/deploy", "tip123", true, nil)
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("expected ErrUsage on diverged branch, got %v", err)
	}
	for _, c := range *calls {
		if strings.Contains(c, "reset") {
			t.Fatal("reset must not run when the FF gate rejects")
		}
	}
}

func TestGitAdvanceDiscardsCollisionsThenResets(t *testing.T) {
	calls := scriptedGit(t, map[string]scriptResp{
		"merge-base": {}, // FF gate ok
		"status":     {out: " M addons/foo/a.py\n?? addons/foo/new.py\n M addons/foo/keep.py"},
		"diff":       {out: "addons/foo/a.py"},                    // tip changes a.py
		"ls-tree":    {out: "addons/foo/a.py\naddons/foo/new.py"}, // tip introduces new.py
		"reset":      {},
		"update-ref": {},
		"checkout":   {},
	})
	if err := gitAdvance(context.Background(), testRSC(), "/srv/odoo", "echo/deploy", "tip123", true, nil); err != nil {
		t.Fatalf("advance: %v", err)
	}
	joined := strings.Join(*calls, "\n")
	// Tracked collision reverted, untracked collision removed, keep.py untouched.
	if !containsCall(*calls, "checkout", "addons/foo/a.py") {
		t.Error("tracked collision a.py not checked out")
	}
	if strings.Contains(joined, "keep.py") {
		t.Error("non-colliding dirty file keep.py must survive (never referenced)")
	}
	if !containsCall(*calls, "rm -f", "addons/foo/new.py") {
		t.Error("untracked collision new.py not removed")
	}
	// Ordering: discard happens before reset.
	resetIdx, checkoutIdx := -1, -1
	for i, c := range *calls {
		if checkoutIdx == -1 && strings.Contains(c, "checkout") {
			checkoutIdx = i
		}
		if resetIdx == -1 && strings.Contains(c, "reset") {
			resetIdx = i
		}
	}
	if checkoutIdx == -1 || resetIdx == -1 || checkoutIdx > resetIdx {
		t.Errorf("discard must precede reset (checkout=%d reset=%d)", checkoutIdx, resetIdx)
	}
	if !containsCall(*calls, "reset", "tip123") {
		t.Error("reset --keep tip not run")
	}
	if !containsCall(*calls, "update-ref", incomingRef) {
		t.Error("holding ref not deleted")
	}
}

func TestGitBootstrapCreatesMissingBranch(t *testing.T) {
	calls := scriptedGit(t, map[string]scriptResp{
		"'--verify'":         {err: errors.New("missing")}, // branch absent
		"'branch'":           {},
		"'--abbrev-ref'":     {out: "echo/deploy"}, // already on it after create
		"'rev-parse' 'HEAD'": {out: "headsha"},
	})
	pre, err := gitBootstrap(context.Background(), testRSC(), "/srv/odoo", "echo/deploy", nil)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if pre != "headsha" {
		t.Errorf("preHEAD = %q", pre)
	}
	if !containsCall(*calls, "branch", "echo/deploy") {
		t.Error("missing branch not created")
	}
}

func TestGitBootstrapChecksOutWrongBranch(t *testing.T) {
	calls := scriptedGit(t, map[string]scriptResp{
		"'--verify'":         {out: "ok"},   // branch exists
		"'--abbrev-ref'":     {out: "main"}, // on the wrong branch
		"'checkout'":         {},
		"'rev-parse' 'HEAD'": {out: "h2"},
	})
	if _, err := gitBootstrap(context.Background(), testRSC(), "/srv/odoo", "echo/deploy", nil); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !containsCall(*calls, "checkout", "echo/deploy") {
		t.Error("wrong current branch not switched")
	}
}

func TestGitRestoreCodeNoGate(t *testing.T) {
	calls := scriptedGit(t, map[string]scriptResp{
		"status":     {},
		"diff":       {},
		"ls-tree":    {},
		"reset":      {},
		"update-ref": {},
	})
	g := gitDeployConfig{enabled: true, branch: "echo/deploy"}
	if err := gitRestoreCode(context.Background(), testRSC(), g, "oldsha", nil); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, c := range *calls {
		if strings.Contains(c, "merge-base") {
			t.Fatal("restore must NOT run the FF gate")
		}
	}
	if !containsCall(*calls, "reset", "oldsha") {
		t.Error("reset to oldsha not run")
	}
}

func TestGitRestoreCodeDisabledNoop(t *testing.T) {
	calls := scriptedGit(t, map[string]scriptResp{})
	if err := gitRestoreCode(context.Background(), testRSC(), gitDeployConfig{}, "x", nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("disabled restore must be a no-op, ran %v", *calls)
	}
}

func TestGitPreflightRemoteFailures(t *testing.T) {
	t.Run("git missing", func(t *testing.T) {
		scriptedGit(t, map[string]scriptResp{"git --version": {err: errors.New("not found")}})
		err := gitPreflight(context.Background(), testRSC(), t.TempDir(), "/srv/odoo")
		if !errors.Is(err, ErrUsage) {
			t.Errorf("expected ErrUsage, got %v", err)
		}
	})
	t.Run("not a work tree", func(t *testing.T) {
		scriptedGit(t, map[string]scriptResp{
			"git --version":       {out: "git version 2.40"},
			"is-inside-work-tree": {out: "false"},
		})
		err := gitPreflight(context.Background(), testRSC(), t.TempDir(), "/srv/odoo")
		if !errors.Is(err, ErrUsage) {
			t.Errorf("expected ErrUsage, got %v", err)
		}
	})
}

func TestModuleOfPath(t *testing.T) {
	cases := map[string]string{
		"addons/custom/foo/models/x.py": "foo",
		"addons/foo/__manifest__.py":    "foo",
		"foo/models/x.py":               "foo",
		"toplevel.txt":                  "",
	}
	for p, want := range cases {
		if got := moduleOfPath(p); got != want {
			t.Errorf("moduleOfPath(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestFilterDirtyByModules(t *testing.T) {
	entries := []remoteDirtyEntry{
		{path: "addons/custom/foo/a.py"},
		{path: "addons/custom/bar/b.py", untracked: true},
		{path: "addons/custom/foo/c.py"},
	}
	got := filterDirtyByModules(entries, []string{"foo"})
	if len(got) != 2 {
		t.Fatalf("want 2 foo entries, got %d (%v)", len(got), got)
	}
	if cands := dirtyModuleCandidates(entries); !reflect.DeepEqual(cands, []string{"bar", "foo"}) {
		t.Errorf("candidates = %v", cands)
	}
}

// containsCall reports whether any recorded command contains every substring.
func containsCall(calls []string, subs ...string) bool {
	for _, c := range calls {
		all := true
		for _, s := range subs {
			if !strings.Contains(c, s) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
