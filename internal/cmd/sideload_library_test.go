package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// versionedLibrary is a module repo with one branch per Odoo series: 18.0 pins
// the expiration date, 19.0 carries a second module, and the checkout is left
// on 19.0.
func versionedLibrary(t *testing.T) (r *sourceRepo, sha18 string) {
	r = newSourceRepo(t)
	r.write("README.md", "library")
	r.write("forced_config_parameter/__manifest__.py", "{'name': 'Forced', 'version': '18.0.1.0.0'}")
	r.write("forced_config_parameter/forced_params.py", "FORCED_PARAMS = {'database.expiration_date': '2200-10-15 12:00:00'}\n")
	r.write(".hidden/ghost/__manifest__.py", "{'name': 'Ghost'}")
	sha18 = r.commit("[ADD] forced_config_parameter: 18.0")
	r.git("branch", "-m", "main", "18.0")
	r.git("checkout", "-q", "-b", "19.0")
	r.write("forced_config_parameter/__manifest__.py", "{'name': 'Forced', 'version': '19.0.1.0.0'}")
	r.write("tools/web_kiosk/__manifest__.py", "{'name': 'Kiosk', 'version': '19.0.2.0.0'}")
	r.commit("[ADD] web_kiosk: 19.0")
	return r, sha18
}

func TestDiscoverLibrary(t *testing.T) {
	ctx := context.Background()
	repo, sha18 := versionedLibrary(t)
	folder := t.TempDir()
	writeModule(t, folder, "hr_bonus", map[string]string{"__manifest__.py": "{'version': '1.0'}"})
	writeModule(t, folder, "old_report", map[string]string{"__manifest__.py": "{'version': '17.0.1.0.0'}"})
	writeModule(t, filepath.Join(folder, "deep", "deeper"), "too_deep", nil)

	mods, hidden, warnings := discoverLibrary(ctx, []string{repo.root, folder, filepath.Join(folder, "missing")}, "18.0")
	var got []string
	for _, m := range mods {
		got = append(got, m.name+"@"+m.source()+"@"+m.version)
	}
	want := []string{
		"forced_config_parameter@" + filepath.Base(repo.root) + "@18.0@18.0.1.0.0",
		"hr_bonus@" + filepath.Base(folder) + "@1.0",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("modules = %v, want %v", got, want)
	}
	if hidden != 1 || len(warnings) != 1 {
		t.Errorf("hidden = %d warnings = %v", hidden, warnings)
	}
	if mods[0].sha != sha18 || mods[0].branch != "18.0" {
		t.Errorf("branch module = %+v", mods[0])
	}

	mods19, _, _ := discoverLibrary(ctx, []string{repo.root}, "19.0")
	if len(mods19) != 2 || mods19[1].name != "web_kiosk" || mods19[1].relPath != "tools/web_kiosk" {
		t.Errorf("19.0 modules = %+v", mods19)
	}
	if mods20, _, _ := discoverLibrary(ctx, []string{repo.root}, "20.0"); len(mods20) != 0 {
		t.Errorf("a repo without the series branch offered %+v", mods20)
	}
}

func TestLibraryMaterializeReadsTheBranch(t *testing.T) {
	ctx := context.Background()
	repo, _ := versionedLibrary(t)
	repo.write("forced_config_parameter/forced_params.py", "uncommitted experiment")
	mods, _, _ := discoverLibrary(ctx, []string{repo.root}, "18.0")
	dir, err := mods[0].materialize(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "forced_params.py"))
	if !strings.Contains(string(b), "2200-10-15") {
		t.Errorf("materialized content = %q, want the 18.0 commit", b)
	}
	if mods[0].dirtyOnBranch(ctx) {
		t.Error("18.0 is not checked out, so the dirty 19.0 checkout must not count")
	}
	mods19, _, _ := discoverLibrary(ctx, []string{repo.root}, "19.0")
	if !mods19[0].dirtyOnBranch(ctx) {
		t.Error("the checked-out 19.0 branch has uncommitted changes in the module")
	}
}

func TestFindLibraryModule(t *testing.T) {
	mods := []libraryModule{
		{name: "a", absRoot: "/lib/one"},
		{name: "b", absRoot: "/lib/one"},
		{name: "b", absRoot: "/lib/two", branch: "19.0"},
	}
	if m, err := findLibraryModule(mods, "a", []string{"/lib/one"}, "19.0"); err != nil || m.name != "a" {
		t.Errorf("a: %+v %v", m, err)
	}
	if _, err := findLibraryModule(mods, "b", nil, "19.0"); !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "two@19.0") {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := findLibraryModule(mods, "zz", nil, "19.0"); !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "--library-add") {
		t.Errorf("empty library: %v", err)
	}
}

func TestIsSideloadPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("local_mod", 0o755); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]bool{
		"./x": true, "a/b": true, "~/mods": true, ".": true, "local_mod": true, "forced_config_parameter": false,
	} {
		if got := isSideloadPath(in); got != want {
			t.Errorf("isSideloadPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSideloadLibrarySetters(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	lib := filepath.Join(home, "mods")
	if err := os.Mkdir(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		cfg, err := config.LoadGlobal()
		if err != nil {
			t.Fatal(err)
		}
		_, err = RunSideload(context.Background(), SideloadOpts{Cfg: cfg, Args: args})
		return err
	}
	if err := run("--library-add", lib); err != nil {
		t.Fatal(err)
	}
	if err := run("--library-add", lib); err != nil {
		t.Fatalf("adding twice: %v", err)
	}
	cfg, _ := config.LoadGlobal()
	if len(cfg.SideloadLibrary) != 1 || cfg.SideloadLibrary[0] != "~/mods" {
		t.Fatalf("library = %v", cfg.SideloadLibrary)
	}
	if err := run("--library-add", filepath.Join(home, "nope")); !errors.Is(err, ErrUsage) {
		t.Errorf("missing dir: %v, want ErrUsage", err)
	}
	if err := run("--library-rm", "~/mods"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.LoadGlobal(); len(cfg.SideloadLibrary) != 0 {
		t.Errorf("library after rm = %v", cfg.SideloadLibrary)
	}
	if err := run("--library-rm", "~/mods"); !errors.Is(err, ErrUsage) {
		t.Errorf("removing an absent root: %v, want ErrUsage", err)
	}
}

func TestRunSideloadFromLibrary(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	repo, sha18 := versionedLibrary(t)
	cfg := remote.cfg()
	cfg.SideloadLibrary = []string{repo.root}
	root := t.TempDir()
	run := func(args ...string) (SideloadResult, error) {
		return RunSideload(ctx, SideloadOpts{Cfg: cfg, Root: root, Args: append(args, "--from", "stg")})
	}
	statusOf := func() string {
		res, err := run("--available")
		if err != nil || len(res.Addons) != 1 {
			t.Fatalf("--available = %+v err = %v", res, err)
		}
		return res.Addons[0].Status
	}

	if got := statusOf(); got != "new" {
		t.Errorf("status before = %q", got)
	}

	old := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	_, err := run()
	stdinIsTTY = old
	if !errors.Is(err, ErrNonInteractive) {
		t.Errorf("no module and no TTY: %v, want ErrNonInteractive", err)
	}

	if _, err := run("forced_config_parameter"); err != nil {
		t.Fatalf("sideload by name: %v", err)
	}
	shipped := filepath.Join(remote.dir, ".echo/sideload/forced_config_parameter/forced_params.py")
	if b, _ := os.ReadFile(shipped); !strings.Contains(string(b), "2200-10-15") {
		t.Errorf("shipped = %q", b)
	}
	lock, _ := readDeployLock(ctx, remoteShellContext{sshHost: "fakehost", remotePath: remote.dir}, nil)
	if e := lock.Modules["forced_config_parameter"]; e.Ref != "18.0" || e.SHA != sha18 || e.Version != "18.0.1.0.0" {
		t.Errorf("lock entry = %+v", e)
	}
	if got := statusOf(); got != "sideloaded · current" {
		t.Errorf("status after = %q", got)
	}

	var offered, muted []string
	sideloadPicker = func(_ string, available, _, deployed, _ []string, _ theme.Palette, _ string) ([]string, []string, bool, error) {
		offered, muted = available, deployed
		return nil, nil, true, nil
	}
	t.Cleanup(func() { sideloadPicker = runFuzzyPickerCore })
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdinIsTTY = old })
	if _, err := run(); !errors.Is(err, ErrCancelled) {
		t.Errorf("cancelled picker: %v, want ErrCancelled", err)
	}
	if len(offered) != 1 || len(muted) != 1 || !strings.Contains(offered[0], "sideloaded · current") {
		t.Errorf("picker rows = %q muted = %q", offered, muted)
	}

	if _, err := run("nope"); !errors.Is(err, ErrUsage) {
		t.Errorf("unknown module: %v, want ErrUsage", err)
	}
}
