package cmd

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"reflect"
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
