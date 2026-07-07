package cmd

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParsePushArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		modules []string
		dirty   bool
		dryRun  bool
		del     bool
		from    string
		remote  bool
		wantErr bool
	}{
		{"empty", nil, nil, false, false, false, "", false, false},
		{"modules", []string{"sale", "account"}, []string{"sale", "account"}, false, false, false, "", false, false},
		{"from with value not a module", []string{"sale", "--from", "prod"}, []string{"sale"}, false, false, false, "prod", false, false},
		{"from equals", []string{"--from=prod", "sale"}, []string{"sale"}, false, false, false, "prod", false, false},
		{"bare remote", []string{"--remote", "sale"}, []string{"sale"}, false, false, false, "", true, false},
		{"dirty dry delete", []string{"--dirty", "--dry-run", "--delete"}, nil, true, true, true, "", false, false},
		{"force consumed", []string{"sale", "--force"}, []string{"sale"}, false, false, false, "", false, false},
		{"unknown flag", []string{"sale", "--nope"}, nil, false, false, false, "", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parsePushArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parsePushArgs(%v) err = nil, want error", tc.args)
				}
				if !errors.Is(err, ErrUsage) {
					t.Errorf("parsePushArgs(%v) err = %v, want ErrUsage", tc.args, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePushArgs(%v) err = %v", tc.args, err)
			}
			if !reflect.DeepEqual(p.modules, tc.modules) || p.dirty != tc.dirty ||
				p.dryRun != tc.dryRun || p.del != tc.del || p.from != tc.from || p.remote != tc.remote {
				t.Errorf("parsePushArgs(%v) = %+v; want modules=%v dirty=%v dry=%v del=%v from=%q remote=%v",
					tc.args, p, tc.modules, tc.dirty, tc.dryRun, tc.del, tc.from, tc.remote)
			}
		})
	}
}

func TestRsyncArgs(t *testing.T) {
	// Baseline: excludes present, trailing slashes on both endpoints, no
	// -n / --delete.
	got := rsyncArgs("/local/addons/sale", "staging", "/srv/odoo/addons/sale", false, false)
	want := []string{
		"-az", "--itemize-changes",
		"--exclude", "__pycache__", "--exclude", "*.pyc", "--exclude", ".git",
		"-e", "ssh -o BatchMode=yes",
		"/local/addons/sale/", "staging:/srv/odoo/addons/sale/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rsyncArgs baseline =\n%v\nwant\n%v", got, want)
	}

	// Dry-run adds -n; delete adds --delete; both before the endpoints.
	got = rsyncArgs("/l/sale", "h", "/r/sale", true, true)
	if !containsInOrder(got, "-n", "--delete") {
		t.Errorf("rsyncArgs dry+delete missing flags: %v", got)
	}
	if got[len(got)-2] != "/l/sale/" || got[len(got)-1] != "h:/r/sale/" {
		t.Errorf("rsyncArgs endpoints wrong: %v", got[len(got)-2:])
	}

	// No -n / --delete when not requested.
	got = rsyncArgs("/l/sale", "h", "/r/sale", false, false)
	for _, a := range got {
		if a == "-n" || a == "--delete" {
			t.Errorf("rsyncArgs baseline should not contain %q: %v", a, got)
		}
	}
}

func TestPushDest(t *testing.T) {
	origBase, origDir := probeRemoteBase, probeRemoteDir
	defer func() { probeRemoteBase, probeRemoteDir = origBase, origDir }()

	// Profile addons paths: one absolute (container, ignored for the host FS)
	// and one relative ("custom").
	rv := remoteView{rsc: remoteShellContext{
		sshHost:    "h",
		remotePath: "/srv/odoo",
		prof: config.RemoteProfile{
			AddonsPaths: []string{"/mnt/extra-addons", "custom"},
		},
	}}
	cfg := &config.Config{AddonsPaths: []string{"addons"}}
	// The local cwd must NOT influence the destination — vary it and expect
	// the same result. "/inside/addons" stands for running from within addons/.
	local := PushOpts{Cfg: cfg, Root: "/inside/addons"}

	t.Run("existing host location wins", func(t *testing.T) {
		probeRemoteBase = func(context.Context, remoteView, string) (string, bool, error) {
			return "addons", false, nil // found on the host FS under addons/
		}
		probeRemoteDir = func(context.Context, string, string) bool { return false }
		dest, err := pushDest(context.Background(), rv, local, "sale")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if dest != "/srv/odoo/addons/sale" {
			t.Errorf("dest = %q, want /srv/odoo/addons/sale", dest)
		}
	})

	t.Run("container-only remote fails closed", func(t *testing.T) {
		probeRemoteBase = func(context.Context, remoteView, string) (string, bool, error) {
			return "/mnt/extra-addons", true, nil // exists, but in-container
		}
		_, err := pushDest(context.Background(), rv, local, "sale")
		if err == nil {
			t.Fatal("want error for container-internal remote, got nil")
		}
	})

	t.Run("new module lands in the remote addons dir, not the local cwd", func(t *testing.T) {
		probeRemoteBase = func(context.Context, remoteView, string) (string, bool, error) {
			return "", false, errors.New("not found")
		}
		// Only <remotePath>/custom exists on the remote.
		probeRemoteDir = func(_ context.Context, _ string, dir string) bool {
			return dir == "/srv/odoo/custom"
		}
		dest, err := pushDest(context.Background(), rv, local, "newmod")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if dest != "/srv/odoo/custom/newmod" {
			t.Errorf("dest = %q, want /srv/odoo/custom/newmod (remote layout, not cwd)", dest)
		}
	})

	t.Run("root-placed module (base .) re-homes into addons", func(t *testing.T) {
		// A prior mis-push left the module at the docker root; base "." must be
		// ignored and the module re-homed in a real addons dir.
		probeRemoteBase = func(context.Context, remoteView, string) (string, bool, error) {
			return ".", false, nil
		}
		probeRemoteDir = func(_ context.Context, _ string, dir string) bool {
			return dir == "/srv/odoo/custom"
		}
		dest, err := pushDest(context.Background(), rv, local, "sale")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if dest != "/srv/odoo/custom/sale" {
			t.Errorf("dest = %q, want /srv/odoo/custom/sale (never the root)", dest)
		}
	})

	t.Run("no addons dir on the remote errors", func(t *testing.T) {
		probeRemoteBase = func(context.Context, remoteView, string) (string, bool, error) {
			return "", false, errors.New("not found")
		}
		probeRemoteDir = func(context.Context, string, string) bool { return false }
		if _, err := pushDest(context.Background(), rv, local, "ghost"); err == nil {
			t.Fatal("want error when no addons dir exists on the remote, got nil")
		}
	})
}

func TestRemoteAddonsCandidates(t *testing.T) {
	// Absolute paths and "." are dropped; relative ones kept in order.
	got := remoteAddonsCandidates([]string{"/mnt/extra-addons", ".", "custom", "addons"})
	want := []string{"custom", "addons"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("remoteAddonsCandidates = %v, want %v", got, want)
	}
	// No usable relative paths → conventional fallback.
	got = remoteAddonsCandidates([]string{"/only/abs", "."})
	if !reflect.DeepEqual(got, []string{"addons", "custom"}) {
		t.Errorf("fallback = %v, want [addons custom]", got)
	}
}

func TestMergeModules(t *testing.T) {
	got := mergeModules([]string{"sale", "account"}, []string{"account", "stock", ""})
	want := []string{"sale", "account", "stock"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeModules = %v, want %v", got, want)
	}
}

func containsInOrder(haystack []string, needles ...string) bool {
	i := 0
	for _, h := range haystack {
		if i < len(needles) && h == needles[i] {
			i++
		}
	}
	return i == len(needles)
}
