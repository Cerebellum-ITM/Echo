package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

func TestParseSideloadArgs(t *testing.T) {
	t.Run("source and flags", func(t *testing.T) {
		got, err := parseSideloadArgs([]string{"./my_addon", "--from", "prod", "--dry-run", "--resync"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got.sources) != 1 || got.sources[0] != "./my_addon" || got.from != "prod" || !got.dryRun || !got.resync || got.allowProd {
			t.Fatalf("parsed = %+v", got)
		}
	})
	t.Run("from equals form", func(t *testing.T) {
		got, err := parseSideloadArgs([]string{"--from=staging", "path"})
		if err != nil || got.from != "staging" || len(got.sources) != 1 || got.sources[0] != "path" {
			t.Fatalf("parsed = %+v err = %v", got, err)
		}
	})
	t.Run("no source opens the library", func(t *testing.T) {
		got, err := parseSideloadArgs([]string{"--from", "stg"})
		if err != nil || len(got.sources) != 0 {
			t.Fatalf("parsed = %+v err = %v", got, err)
		}
	})
	t.Run("several modules", func(t *testing.T) {
		got, err := parseSideloadArgs([]string{"a,b", "c"})
		if err != nil || strings.Join(got.sources, " ") != "a b c" {
			t.Fatalf("parsed = %+v err = %v", got, err)
		}
	})
	t.Run("library flags stand alone", func(t *testing.T) {
		if _, err := parseSideloadArgs([]string{"--library-add", "x", "--from", "stg"}); !errors.Is(err, ErrUsage) {
			t.Fatalf("err = %v, want ErrUsage", err)
		}
		if _, err := parseSideloadArgs([]string{"--library", "--library-rm", "x"}); !errors.Is(err, ErrUsage) {
			t.Fatalf("err = %v, want ErrUsage", err)
		}
		if _, err := parseSideloadArgs([]string{"--available", "mod"}); !errors.Is(err, ErrUsage) {
			t.Fatalf("--available with a module: err = %v, want ErrUsage", err)
		}
	})
	t.Run("unknown flag", func(t *testing.T) {
		if _, err := parseSideloadArgs([]string{"path", "--nope"}); err == nil {
			t.Fatal("expected error for unknown flag")
		}
	})
	t.Run("force is not a sideload flag", func(t *testing.T) {
		if _, err := parseSideloadArgs([]string{"path", "--force"}); !errors.Is(err, ErrUsage) {
			t.Fatalf("err = %v, want ErrUsage", err)
		}
	})
	t.Run("list takes only from and json", func(t *testing.T) {
		got, err := parseSideloadArgs([]string{"--list", "--from", "stg", "--json"})
		if err != nil || !got.list || !got.jsonOut || got.from != "stg" {
			t.Fatalf("parsed = %+v err = %v", got, err)
		}
		if _, err := parseSideloadArgs([]string{"--list", "path"}); !errors.Is(err, ErrUsage) {
			t.Fatalf("--list with a path: err = %v, want ErrUsage", err)
		}
	})
	t.Run("from without value", func(t *testing.T) {
		if _, err := parseSideloadArgs([]string{"path", "--from"}); err == nil {
			t.Fatal("expected error for --from without value")
		}
	})
}

// writeModule builds a minimal addon tree under root/name and returns its dir.
func writeModule(t *testing.T, root, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if files == nil {
		files = map[string]string{}
	}
	if _, ok := files["__manifest__.py"]; !ok {
		files["__manifest__.py"] = "{'name': '" + name + "'}"
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCollectAddons(t *testing.T) {
	t.Run("single module dir", func(t *testing.T) {
		root := t.TempDir()
		writeModule(t, root, "sale_ext", nil)
		got, err := collectAddons(filepath.Join(root, "sale_ext"))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].name != "sale_ext" {
			t.Fatalf("got = %+v", got)
		}
	})
	t.Run("folder of modules, sorted, skips non-modules", func(t *testing.T) {
		root := t.TempDir()
		writeModule(t, root, "zeta", nil)
		writeModule(t, root, "alpha", nil)
		// a plain dir without a manifest must be ignored
		if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := collectAddons(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].name != "alpha" || got[1].name != "zeta" {
			t.Fatalf("got = %+v", got)
		}
	})
	t.Run("no module", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := collectAddons(root); err == nil {
			t.Fatal("expected error when no addon present")
		}
	})
	t.Run("missing path", func(t *testing.T) {
		if _, err := collectAddons(filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Fatal("expected error for missing path")
		}
	})
}

func TestHashAddonTree(t *testing.T) {
	root := t.TempDir()
	dir := writeModule(t, root, "mod", map[string]string{
		"models/a.py": "x = 1",
		"views/v.xml": "<odoo/>",
	})

	h1, err := hashAddonTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Identical rebuild → identical hash.
	root2 := t.TempDir()
	dir2 := writeModule(t, root2, "mod", map[string]string{
		"models/a.py": "x = 1",
		"views/v.xml": "<odoo/>",
	})
	h2, err := hashAddonTree(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("identical trees hashed differently: %s vs %s", h1, h2)
	}

	// A content change flips the hash.
	if err := os.WriteFile(filepath.Join(dir, "models/a.py"), []byte("x = 2"), 0o644); err != nil {
		t.Fatal(err)
	}
	h3, err := hashAddonTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h1 {
		t.Fatal("content change did not change the hash")
	}

	// Excluded noise (a .pyc / __pycache__) must not affect the hash.
	if err := os.MkdirAll(filepath.Join(dir2, "__pycache__"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "__pycache__", "a.pyc"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "models", "a.pyc"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	h4, err := hashAddonTree(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if h4 != h2 {
		t.Fatal("excluded files changed the hash")
	}
}

func TestTarAddon(t *testing.T) {
	root := t.TempDir()
	dir := writeModule(t, root, "mod", map[string]string{
		"models/a.py":       "x = 1",
		"__pycache__/a.pyc": "junk",
		"models/b.pyc":      "junk",
	})

	blob, err := tarAddon(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := tarEntries(t, blob)

	// Manifest + real file present at addon-relative paths.
	if !contains(names, "__manifest__.py") {
		t.Errorf("manifest missing from archive: %v", names)
	}
	if !contains(names, "models/a.py") {
		t.Errorf("models/a.py missing from archive: %v", names)
	}
	// Excluded noise absent.
	for _, n := range names {
		if strings.HasSuffix(n, ".pyc") || strings.Contains(n, "__pycache__") {
			t.Errorf("excluded entry leaked into archive: %s", n)
		}
	}
}

func tarEntries(t *testing.T, blob []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, strings.TrimSuffix(hdr.Name, "/"))
	}
	sort.Strings(names)
	return names
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestPickMountBase(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"prefers non-usr dev dir", []string{"/usr/lib/python3/dist-packages/odoo/addons", "/mnt/extra-addons", "/opt/odoo/addons"}, "/mnt/extra-addons"},
		{"trims trailing slash", []string{"/opt/odoo/addons/"}, "/opt/odoo/addons"},
		{"falls back to first when all usr", []string{"/usr/a", "/usr/b"}, "/usr/a"},
		{"default when empty", nil, "/mnt/extra-addons"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickMountBase(tc.in); got != tc.want {
				t.Fatalf("pickMountBase(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRenderSideloadOverride(t *testing.T) {
	got := renderSideloadOverride("odoo", "/mnt/extra-addons/", []string{"zeta", "alpha"})
	want := "# managed by echo sideload — do not edit by hand\n" +
		"services:\n" +
		"  odoo:\n" +
		"    volumes:\n" +
		"      - ./.echo/sideload/alpha:/mnt/extra-addons/alpha\n" +
		"      - ./.echo/sideload/zeta:/mnt/extra-addons/zeta\n"
	if got != want {
		t.Fatalf("override mismatch:\n got=%q\nwant=%q", got, want)
	}
	if !strings.Contains(got, sideloadOverrideMarker) {
		t.Fatal("marker missing from rendered override")
	}
}

func TestUnionKeys(t *testing.T) {
	a := map[string]string{"b": "1", "a": "1"}
	b := map[string]string{"c": "1", "a": "1"}
	got := unionKeys(a, b)
	want := []string{"a", "b", "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unionKeys = %v, want %v", got, want)
	}
}

func TestCheckAddonSeries(t *testing.T) {
	cases := []struct {
		version, serie string
		wantErr        bool
	}{
		{"18.0.1.0.0", "18.0", false},
		{"17.0.1.0.0", "18.0", true},
		{"1.0.0", "18.0", false},
		{"", "18.0", false},
		{"17.0.1.0.0", "", false},
	}
	for _, c := range cases {
		err := checkAddonSeries("mod", c.version, c.serie)
		if c.wantErr != errors.Is(err, ErrUsage) {
			t.Errorf("checkAddonSeries(%q, %q) = %v, wantErr %v", c.version, c.serie, err, c.wantErr)
		}
	}
}

func TestSideloadTargetRules(t *testing.T) {
	prod := remoteShellContext{target: connectTarget{stage: "prod", rawStage: "prod"}}
	staging := remoteShellContext{target: connectTarget{stage: "staging"}}
	reverb := remoteShellContext{target: connectTarget{stage: "dev"}, prof: config.RemoteProfile{Reverb: &config.ReverbMarker{}}}

	if err := sideloadTargetError(prod, false); !errors.Is(err, ErrUsage) {
		t.Errorf("prod without --allow-prod: %v, want ErrUsage", err)
	}
	if err := sideloadTargetError(prod, true); err != nil {
		t.Errorf("prod with --allow-prod: %v", err)
	}
	if err := sideloadTargetError(staging, false); err != nil {
		t.Errorf("staging: %v", err)
	}
	if err := sideloadTargetError(reverb, true); !errors.Is(err, ErrUsage) {
		t.Errorf("reverb: %v, want ErrUsage", err)
	}

	old := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = old })
	if err := confirmSideloadProd(theme.Palette{}, prod); !errors.Is(err, ErrNonInteractive) {
		t.Errorf("headless prod confirm: %v, want ErrNonInteractive", err)
	}
	if err := confirmSideloadProd(theme.Palette{}, staging); err != nil {
		t.Errorf("staging confirm: %v", err)
	}
}

func TestDeployLockKeepsSideloadEntries(t *testing.T) {
	sideloaded := LockModule{Source: lockSourceSideload, Tree: sideloadHashPrefix + "abcdef0123456789"}
	l := newDeployLock("stg")
	l.record(map[string]LockModule{
		"pinned":   sideloaded,
		"overlay":  {Source: lockSourceWorktree},
		"on_trunk": {Source: lockSourceBranch},
	})
	l.rebase(LockBase{Branch: "echo/deploy", SHA: "abc"}, false)
	if _, ok := l.Modules["pinned"]; !ok || len(l.Modules) != 1 {
		t.Fatalf("after rebase modules = %v, want only the sideloaded entry", l.Modules)
	}
	if got := sideloaded.label(); !strings.HasPrefix(got, "sideload@abcdef0") {
		t.Errorf("label = %q", got)
	}
}

func TestRunSideloadFakeRemote(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	src := t.TempDir()
	addon := writeModule(t, src, "forced_config_parameter", map[string]string{
		"__manifest__.py":  "{'name': 'Forced', 'version': '18.0.1.0.0', 'depends': ['base']}",
		"forced_params.py": "FORCED_PARAMS = {'database.expiration_date': '2200-10-15 12:00:00'}\n",
	})
	root := t.TempDir()
	run := func(args ...string) (SideloadResult, error) {
		return RunSideload(ctx, SideloadOpts{Cfg: remote.cfg(), Root: root, Args: append(args, "--from", "stg")})
	}
	sideloaded := filepath.Join(remote.dir, ".echo/sideload/forced_config_parameter")
	override := filepath.Join(remote.dir, sideloadOverrideFile)

	res, err := run(addon, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !res.Planned || len(res.Addons) != 1 || res.Addons[0].Action != "install" {
		t.Errorf("dry-run result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(remote.dir, ".echo")); !os.IsNotExist(err) {
		t.Fatal("the dry-run wrote to the server")
	}

	if _, err := run(addon); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(sideloaded, "forced_params.py")); !strings.Contains(string(b), "2200-10-15 12:00:00") {
		t.Errorf("addon not shipped: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(remote.dir, ".echo/.gitignore")); string(b) != "*\n" {
		t.Errorf(".echo/.gitignore = %q", b)
	}
	// The fake ssh records anything naming "compose" instead of running it,
	// the override path included; its content is covered by
	// TestRenderSideloadOverride.
	log := remote.composeLog()
	for _, want := range []string{"cat > " + shellQuote(override), "'up' '-d'", "'-i' 'forced_config_parameter'"} {
		if !strings.Contains(log, want) {
			t.Errorf("compose log lacks %q:\n%s", want, log)
		}
	}
	lock, _ := readDeployLock(ctx, remoteShellContext{sshHost: "fakehost", remotePath: remote.dir}, nil)
	e := lock.Modules["forced_config_parameter"]
	if e.Source != lockSourceSideload || !e.Verified || e.Version != "18.0.1.0.0" || !strings.HasPrefix(e.Tree, sideloadHashPrefix) {
		t.Errorf("lock entry = %+v", e)
	}

	t.Setenv("FAKE_MODULE_STATES", "forced_config_parameter|installed|18.0.1.0.0")
	before := remote.composeLog()
	res, err = run(addon)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Addons[0].Synced || res.Addons[0].Action != "none" {
		t.Errorf("unchanged addon result = %+v", res.Addons[0])
	}
	if after := strings.TrimPrefix(remote.composeLog(), before); strings.Contains(after, "'up' '-d'") || strings.Contains(after, "stop-after-init") {
		t.Errorf("an unchanged addon restarted the server: %q", after)
	}

	listed, err := run("--list")
	if err != nil || len(listed.Addons) != 1 || listed.Addons[0].Name != "forced_config_parameter" || !*listed.Addons[0].Verified {
		t.Errorf("--list = %+v err = %v", listed, err)
	}
}

func TestRunSideloadRefusesWrongSeries(t *testing.T) {
	remote := newFakeRemote(t)
	addon := writeModule(t, t.TempDir(), "old_mod", map[string]string{
		"__manifest__.py": "{'name': 'Old', 'version': '17.0.1.0.0'}",
	})
	_, err := RunSideload(context.Background(), SideloadOpts{Cfg: remote.cfg(), Root: t.TempDir(), Args: []string{addon, "--from", "stg"}})
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
	if _, serr := os.Stat(filepath.Join(remote.dir, ".echo")); !os.IsNotExist(serr) {
		t.Fatal("a refused sideload wrote to the server")
	}
}
