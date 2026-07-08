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

func TestParseItemize(t *testing.T) {
	tests := []struct {
		line string
		op   string
		path string
		ok   bool
	}{
		{"<f+++++++++ __init__.py", "new", "__init__.py", true},
		{">f+++++++++ data/x.xml", "new", "data/x.xml", true},
		{"<f.st...... security/s.xml", "changed", "security/s.xml", true},
		{"*deleting   old/gone.py", "deleted", "old/gone.py", true},
		{"cd+++++++++ data/", "", "", false},   // directory
		{".d..t...... ./", "", "", false},      // directory (root)
		{"created directory /srv/x", "", "", false}, // rsync noise
		{"", "", "", false},
		{"*deleting   stale/", "", "", false}, // whole-dir deletion skipped
	}
	for _, tc := range tests {
		fc, ok := parseItemize(tc.line)
		if ok != tc.ok {
			t.Errorf("parseItemize(%q) ok = %v, want %v", tc.line, ok, tc.ok)
			continue
		}
		if ok && (fc.Op != tc.op || fc.Path != tc.path) {
			t.Errorf("parseItemize(%q) = {%s %s}, want {%s %s}", tc.line, fc.Op, fc.Path, tc.op, tc.path)
		}
	}
}

func TestBuildSyncTree(t *testing.T) {
	changes := []FileChange{
		{Op: "new", Path: "__init__.py"},
		{Op: "new", Path: "__manifest__.py"},
		{Op: "new", Path: "data/mail_template_data.xml"},
		{Op: "new", Path: "report/a.xml"},
		{Op: "new", Path: "report/b.xml"},
		{Op: "changed", Path: "security/s.xml"},
	}
	rows := BuildSyncTree(changes)

	// Root files come first as ├─ leaves; the last top-level entry uses └─.
	if rows[0].Prefix != "├─ " || rows[0].Name != "__init__.py" || rows[0].Kind != "new" {
		t.Fatalf("row 0 = %+v", rows[0])
	}
	if rows[0].Glyph != "+" {
		t.Errorf("new glyph = %q, want +", rows[0].Glyph)
	}

	// Find the security dir node — it's the last group, so └─, children indented
	// with plain spaces (no │).
	var secIdx = -1
	for i, r := range rows {
		if r.Kind == "dir" && r.Name == "security/" {
			secIdx = i
		}
	}
	if secIdx < 0 {
		t.Fatal("security/ dir node not found")
	}
	if rows[secIdx].Prefix != "└─ " {
		t.Errorf("last dir prefix = %q, want └─ ", rows[secIdx].Prefix)
	}
	child := rows[secIdx+1]
	if child.Prefix != "     " || child.Glyph != "~" || child.Name != "s.xml" || child.Kind != "changed" {
		t.Errorf("security child = %+v, want plain-indented ~ s.xml", child)
	}

	// A non-last dir (data/ or report/) indents children with the │ connector.
	for i, r := range rows {
		if r.Kind == "dir" && r.Name == "report/" {
			if rows[i+1].Prefix != "│    " {
				t.Errorf("non-last dir child prefix = %q, want │ connector", rows[i+1].Prefix)
			}
		}
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
