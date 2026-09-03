package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

// nestedRepo builds a repo where each module is given as a repo-relative
// directory, e.g. "oehealth_modules_19/oehealth_consultation_extra".
func nestedRepo(t *testing.T, dirs ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range dirs {
		full := filepath.Join(root, filepath.FromSlash(d))
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "__manifest__.py"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestResolveAddonFindsModuleInSubfolder(t *testing.T) {
	root := nestedRepo(t, "oehealth_modules_19/oehealth_consultation_extra")
	dir, name, err := resolveAddon(&config.Config{}, root, "oehealth_consultation_extra")
	if err != nil {
		t.Fatalf("resolveAddon: %v", err)
	}
	if name != "oehealth_consultation_extra" {
		t.Errorf("name = %q, want oehealth_consultation_extra", name)
	}
	if filepath.Base(dir) != "oehealth_modules_19" {
		t.Errorf("dir = %q, want the oehealth_modules_19 addons path", dir)
	}
}

// A configured addons path resolves without the walk, so a project that
// works today is unaffected by discovery.
func TestResolveAddonPrefersConfiguredPaths(t *testing.T) {
	root := nestedRepo(t, "addons/sale_extra", "vendored/sale_extra")
	cfg := &config.Config{AddonsPaths: []string{"addons"}}
	dir, _, err := resolveAddon(cfg, root, "sale_extra")
	if err != nil {
		t.Fatalf("resolveAddon: %v", err)
	}
	if filepath.Base(dir) != "addons" {
		t.Errorf("dir = %q, want the configured addons path", dir)
	}
}

func TestResolveAddonAmbiguousIsUsageError(t *testing.T) {
	root := nestedRepo(t, "group_a/sale_extra", "group_b/sale_extra")
	_, _, err := resolveAddon(&config.Config{}, root, "sale_extra")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage naming both paths", err)
	}
}

// The Case C regression: a path argument must never reach the remote
// destination with its separator intact.
func TestResolveAddonNormalizesPathArgument(t *testing.T) {
	root := nestedRepo(t, "oehealth_modules_19/oehealth_consultation_extra")
	dir, name, err := resolveAddon(&config.Config{}, root,
		"oehealth_modules_19/oehealth_consultation_extra")
	if err != nil {
		t.Fatalf("resolveAddon: %v", err)
	}
	if name != "oehealth_consultation_extra" {
		t.Errorf("name = %q, want the bare module name", name)
	}
	if filepath.Base(dir) != "oehealth_modules_19" {
		t.Errorf("dir = %q, want the containing addons path", dir)
	}
}

func TestResolveAddonPathWithoutManifestIsUsageError(t *testing.T) {
	root := nestedRepo(t, "oehealth_modules_19/oehealth_consultation_extra")
	_, _, err := resolveAddon(&config.Config{}, root, "oehealth_modules_19/nope")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

func TestListAddonsFallsBackToDiscovery(t *testing.T) {
	root := nestedRepo(t, "oehealth_modules_19/a_mod", "oehealth_modules_19/b_mod")
	got := listAddons(&config.Config{}, root)
	if want := []string{"a_mod", "b_mod"}; !reflect.DeepEqual(got, want) {
		t.Errorf("listAddons = %v, want %v", got, want)
	}
}

func TestAddonFromPath(t *testing.T) {
	root := nestedRepo(t, "group/nested_mod", "flat_mod")
	cases := []struct {
		path     string
		wantMod  string
		wantADir string
	}{
		{"group/nested_mod/models/x.py", "nested_mod", "group/nested_mod"},
		{"flat_mod/views/v.xml", "flat_mod", "flat_mod"},
		{"README.md", "", ""},
		{"group/README.md", "", ""},
	}
	for _, tc := range cases {
		mod, dir := addonFromPath(root, tc.path)
		if mod != tc.wantMod || dir != tc.wantADir {
			t.Errorf("addonFromPath(%q) = (%q, %q), want (%q, %q)",
				tc.path, mod, dir, tc.wantMod, tc.wantADir)
		}
	}
}

// The dirty/auto selection has to see a nested module too, or `deploy
// --dirty` silently deploys nothing in a subfolder layout.
func TestModulesFromPathsNestedLayout(t *testing.T) {
	root := nestedRepo(t, "oehealth_modules_19/oehealth_consultation_extra")
	got := modulesFromPaths(root, []string{
		"oehealth_modules_19/oehealth_consultation_extra/models/x.py",
		"oehealth_modules_19/README.md",
	})
	if want := []string{"oehealth_consultation_extra"}; !reflect.DeepEqual(got, want) {
		t.Errorf("modulesFromPaths = %v, want %v", got, want)
	}
}

func TestDiscoverAddonsRootsSkipsVendored(t *testing.T) {
	root := nestedRepo(t, "node_modules/fake_mod", ".hidden/fake_mod", "real/good_mod")
	got := discoverAddonsRoots(root)
	if len(got) != 1 || filepath.Base(got[0]) != "real" {
		t.Errorf("discoverAddonsRoots = %v, want only the real addons path", got)
	}
}

// An addon is a leaf: a module holding a directory that itself looks like
// an addon must not turn the module into an addons path.
func TestDiscoverAddonsRootsDoesNotDescendIntoAddons(t *testing.T) {
	root := nestedRepo(t, "addons/outer_mod", "addons/outer_mod/inner_mod")
	for _, r := range discoverAddonsRoots(root) {
		if filepath.Base(r) == "outer_mod" {
			t.Fatalf("descended into an addon: %v", r)
		}
	}
}
