package cmd

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParseActionsArgs(t *testing.T) {
	t.Run("default list", func(t *testing.T) {
		p, err := parseActionsArgs(nil)
		if err != nil || p.sub != "list" {
			t.Fatalf("got sub=%q err=%v", p.sub, err)
		}
	})
	t.Run("add", func(t *testing.T) {
		p, _ := parseActionsArgs([]string{"add"})
		if p.sub != "add" {
			t.Errorf("sub = %q, want add", p.sub)
		}
	})
	t.Run("rm with name + force", func(t *testing.T) {
		p, err := parseActionsArgs([]string{"rm", "build-image", "--force"})
		if err != nil || p.sub != "rm" || p.name != "build-image" || !p.force {
			t.Fatalf("got %+v err=%v", p, err)
		}
	})
	t.Run("edit with --from consumes value not name", func(t *testing.T) {
		p, err := parseActionsArgs([]string{"edit", "--from", "prod"})
		if err != nil || p.sub != "edit" || p.from != "prod" || p.name != "" {
			t.Fatalf("got %+v err=%v", p, err)
		}
	})
	t.Run("json", func(t *testing.T) {
		p, _ := parseActionsArgs([]string{"list", "--json"})
		if !p.jsonOut {
			t.Error("want jsonOut")
		}
	})
	t.Run("unknown subcommand errors", func(t *testing.T) {
		if _, err := parseActionsArgs([]string{"frobnicate"}); !errors.Is(err, ErrUsage) {
			t.Errorf("err = %v, want ErrUsage", err)
		}
	})
	t.Run("unknown flag errors", func(t *testing.T) {
		if _, err := parseActionsArgs([]string{"list", "--nope"}); !errors.Is(err, ErrUsage) {
			t.Errorf("err = %v, want ErrUsage", err)
		}
	})
}

func TestResolveActionDir(t *testing.T) {
	tests := []struct {
		root, execPath, want string
	}{
		{"/srv/odoo", "", "/srv/odoo"},          // empty → root
		{"/srv/odoo", "docker", "/srv/odoo/docker"}, // relative → joined
		{"/srv/odoo", "./build/", "/srv/odoo/build"},
		{"/srv/odoo", "/opt/build", "/opt/build"}, // absolute → as-is
	}
	for _, tc := range tests {
		if got := resolveActionDir(tc.root, tc.execPath, path.Join, path.IsAbs); got != tc.want {
			t.Errorf("resolveActionDir(%q, %q) = %q, want %q", tc.root, tc.execPath, got, tc.want)
		}
	}
}

func TestActionDir(t *testing.T) {
	rsc := remoteShellContext{remotePath: "/srv/odoo"}
	remote := config.DeployAction{Where: config.WhereRemote, ExecPath: "docker"}
	if got := actionDir(rsc, "/local/root", remote); got != "/srv/odoo/docker" {
		t.Errorf("remote actionDir = %q, want /srv/odoo/docker", got)
	}
	local := config.DeployAction{Where: config.WhereLocal, ExecPath: "sub"}
	if got := actionDir(rsc, "/local/root", local); got != filepath.Join("/local/root", "sub") {
		t.Errorf("local actionDir = %q", got)
	}
}

func TestFirstRelAddons(t *testing.T) {
	if got := firstRelAddons([]string{"/mnt/extra", ".", "custom", "addons"}); got != "custom" {
		t.Errorf("got %q, want custom (first relative)", got)
	}
	if got := firstRelAddons([]string{"/only/abs"}); got != "addons" {
		t.Errorf("got %q, want addons fallback", got)
	}
	if got := firstRelAddons(nil); got != "addons" {
		t.Errorf("got %q, want addons fallback", got)
	}
}

func TestTruncateMiddle(t *testing.T) {
	if got := truncateMiddle("short", 48); got != "short" {
		t.Errorf("short string changed: %q", got)
	}
	long := "docker build -t myodoo:latest -f docker/Dockerfile . --no-cache"
	got := truncateMiddle(long, 20)
	if len([]rune(got)) != 20 {
		t.Errorf("truncated len = %d, want 20 (%q)", len([]rune(got)), got)
	}
}

func TestActionPathLabel(t *testing.T) {
	if actionPathLabel("") != "(root)" || actionPathLabel("  ") != "(root)" {
		t.Error("empty exec_path should label as (root)")
	}
	if actionPathLabel("docker") != "docker" {
		t.Error("non-empty exec_path should pass through")
	}
}

func TestListLocalDirs(t *testing.T) {
	tmp := t.TempDir()
	for _, d := range []string{"addons", "custom", ".hidden"} {
		if err := os.Mkdir(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := listLocalDirs(tmp)
	if err != nil {
		t.Fatal(err)
	}
	// Dotdirs and files excluded, sorted.
	if !reflect.DeepEqual(got, []string{"addons", "custom"}) {
		t.Errorf("listLocalDirs = %v, want [addons custom]", got)
	}
}

func TestActionsRemoteScoped(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"add"}, false},
		{[]string{"add", "--from", "prod"}, true},
		{[]string{"add", "--from=prod"}, true},
		{[]string{"add", "--remote"}, true},
	}
	for _, c := range cases {
		p, err := parseActionsArgs(c.args)
		if err != nil {
			t.Fatalf("parse %v: %v", c.args, err)
		}
		if got := p.remoteScoped(); got != c.want {
			t.Errorf("remoteScoped(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

// scopedActions must read the SERVER's list when a remote flag is present and
// the local list otherwise — the whole point of Unit 105.
func TestScopedActionsPicksTheRightList(t *testing.T) {
	local := []config.DeployAction{{Name: "local-one", Phase: "post_push", Where: "remote", Run: "./local.sh"}}
	server := []config.DeployAction{{Name: "server-one", Phase: "post_push", Where: "remote", Run: "./server.sh"}}
	opts := ActionsOpts{Cfg: &config.Config{DeployActions: local}}

	rsc := testRSC()
	rsc.prof.DeployActions = server
	resolve := func() (remoteShellContext, error) { return rsc, nil }

	t.Run("local scope never resolves a remote", func(t *testing.T) {
		called := false
		got, gotRSC, err := scopedActions(opts, actionsArgs{}, func() (remoteShellContext, error) {
			called = true
			return rsc, nil
		})
		if err != nil {
			t.Fatalf("scopedActions: %v", err)
		}
		if called {
			t.Error("local scope must not resolve a remote (no SSH)")
		}
		if gotRSC != nil {
			t.Error("local scope must return a nil remote")
		}
		if len(got) != 1 || got[0].Name != "local-one" {
			t.Errorf("got %+v, want the local list", got)
		}
	})

	t.Run("server scope reads the target's list", func(t *testing.T) {
		got, gotRSC, err := scopedActions(opts, actionsArgs{from: "prod"}, resolve)
		if err != nil {
			t.Fatalf("scopedActions: %v", err)
		}
		if gotRSC == nil {
			t.Fatal("server scope must return the resolved remote")
		}
		if len(got) != 1 || got[0].Name != "server-one" {
			t.Errorf("got %+v, want the server list", got)
		}
	})
}

// A server-scoped write must compose the remote profile and never touch the
// local list.
func TestCommitActionsServerScopeWritesProfile(t *testing.T) {
	var wrote []byte
	var cmds []string
	orig := actionsRunSSH
	defer func() { actionsRunSSH = orig }()
	actionsRunSSH = func(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error) {
		cmds = append(cmds, remoteCmd)
		if strings.HasPrefix(remoteCmd, "cat > ") {
			wrote = stdin
		}
		return []byte(""), nil
	}

	local := []config.DeployAction{{Name: "local-one", Phase: "post_push", Where: "remote", Run: "./local.sh"}}
	opts := ActionsOpts{Cfg: &config.Config{DeployActions: local}}
	rsc := testRSC()
	next := []config.DeployAction{{Name: "server-only", Phase: "post_push", Where: "remote", Run: "./build.sh dev"}}

	if err := commitActions(context.Background(), opts, &rsc, next); err != nil {
		t.Fatalf("commitActions: %v", err)
	}
	if len(wrote) == 0 {
		t.Fatalf("nothing written to the remote profile; cmds=%v", cmds)
	}
	if !strings.Contains(string(wrote), "server-only") || !strings.Contains(string(wrote), "./build.sh dev") {
		t.Errorf("composed profile missing the action:\n%s", wrote)
	}
	if strings.Contains(string(wrote), "local-one") {
		t.Errorf("server profile must not carry the local list:\n%s", wrote)
	}
	// The local list stays untouched — no cross-contamination.
	if len(opts.Cfg.DeployActions) != 1 || opts.Cfg.DeployActions[0].Name != "local-one" {
		t.Errorf("local list mutated: %+v", opts.Cfg.DeployActions)
	}
	// It must target the remote path's key, not the local project's.
	wantFile := "~/.config/echo/projects/" + config.ProjectKey(rsc.remotePath) + ".toml"
	if !strings.Contains(strings.Join(cmds, "\n"), wantFile) {
		t.Errorf("expected a write to %s, got %v", wantFile, cmds)
	}
}

func TestPickActionIndexScoped(t *testing.T) {
	list := []config.DeployAction{{Name: "a"}, {Name: "b"}}
	opts := ActionsOpts{Cfg: &config.Config{}}

	t.Run("resolves by name within the scoped list", func(t *testing.T) {
		idx, err := pickActionIndex(opts, list, actionsArgs{from: "prod"}, "b", "")
		if err != nil || idx != 1 {
			t.Fatalf("idx=%d err=%v", idx, err)
		}
	})
	t.Run("unknown name errors with the scope named", func(t *testing.T) {
		_, err := pickActionIndex(opts, list, actionsArgs{from: "prod"}, "zzz", "")
		if !errors.Is(err, ErrUsage) {
			t.Fatalf("err = %v, want ErrUsage", err)
		}
		if !strings.Contains(err.Error(), "server profile") {
			t.Errorf("error should name the server scope: %v", err)
		}
	})
	t.Run("empty scoped list errors", func(t *testing.T) {
		if _, err := pickActionIndex(opts, nil, actionsArgs{}, "", ""); !errors.Is(err, ErrUsage) {
			t.Errorf("err = %v, want ErrUsage", err)
		}
	})
}
