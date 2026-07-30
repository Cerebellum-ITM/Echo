package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoolint"
)

// lintFixture builds a project tree with one module and returns its root.
func lintFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("addons/mod_a/__manifest__.py", `{
    'name': 'A',
    'data': ['views/listed.xml'],
}`)
	write("addons/mod_a/views/listed.xml", "<odoo>\n<!-- roto -- aqui -->\n</odoo>\n")
	write("addons/mod_a/views/dead.xml", "<odoo>\n<!-- otro -- roto -->\n</odoo>\n")
	write("addons/mod_b/__manifest__.py", "{'name': 'B', 'data': []}")
	write("addons/mod_b/views/ok.xml", "<odoo>\n<record id=\"a\" model=\"m\"/>\n</odoo>\n")
	return root
}

func lintCfg() *config.Config {
	return &config.Config{OdooVersion: "18", AddonsPaths: []string{"addons"}}
}

func TestParseLintArgs(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "one.xml")
	if err := os.WriteFile(file, []byte("<odoo/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("json flag", func(t *testing.T) {
		p, err := parseLintArgs([]string{"--json"})
		if err != nil {
			t.Fatal(err)
		}
		if !p.jsonOut {
			t.Error("--json did not set jsonOut")
		}
	})

	t.Run("module name", func(t *testing.T) {
		p, err := parseLintArgs([]string{"mod_a"})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(p.modules, []string{"mod_a"}) {
			t.Errorf("modules = %v, want [mod_a]", p.modules)
		}
		if len(p.files) != 0 {
			t.Errorf("files = %v, want none", p.files)
		}
	})

	t.Run("existing path is a file target", func(t *testing.T) {
		p, err := parseLintArgs([]string{file})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(p.files, []string{file}) {
			t.Errorf("files = %v, want [%s]", p.files, file)
		}
		if len(p.modules) != 0 {
			t.Errorf("modules = %v, want none", p.modules)
		}
	})

	t.Run("unknown flag is a usage error", func(t *testing.T) {
		if _, err := parseLintArgs([]string{"--nope"}); !errors.Is(err, ErrUsage) {
			t.Errorf("err = %v, want ErrUsage", err)
		}
	})
}

// An unknown module must fail loudly. The alternative — an empty run
// reporting "0 problems" — reads exactly like success.
func TestRunLintUnknownModuleIsUsageError(t *testing.T) {
	root := lintFixture(t)
	_, err := RunLint(LintOpts{Cfg: lintCfg(), Root: root, Args: []string{"nope"}})
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

func TestRunLintScopesToNamedModule(t *testing.T) {
	root := lintFixture(t)
	res, err := RunLint(LintOpts{Cfg: lintCfg(), Root: root, Args: []string{"mod_b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("linting a clean module reported: %+v", res.Findings)
	}
	if !reflect.DeepEqual(res.Modules, []string{"mod_b"}) {
		t.Errorf("Modules = %v, want [mod_b]", res.Modules)
	}
}

func TestRunLintSeverityFromManifest(t *testing.T) {
	root := lintFixture(t)
	res, err := RunLint(LintOpts{Cfg: lintCfg(), Root: root, Args: nil})
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors() != 1 {
		t.Errorf("Errors() = %d, want 1 (only listed.xml is in a manifest)", res.Errors())
	}
	if res.Warnings() != 1 {
		t.Errorf("Warnings() = %d, want 1 (dead.xml is listed nowhere)", res.Warnings())
	}

	for _, f := range res.Findings {
		switch filepath.Base(f.File) {
		case "listed.xml":
			if f.Kind != odoolint.KindErr {
				t.Errorf("listed.xml = %q, want %q", f.Kind, odoolint.KindErr)
			}
		case "dead.xml":
			if f.Kind != odoolint.KindWarn {
				t.Errorf("dead.xml = %q, want %q", f.Kind, odoolint.KindWarn)
			}
		default:
			t.Errorf("unexpected finding in %s", f.File)
		}
	}
}

func TestRunLintExplicitFile(t *testing.T) {
	root := lintFixture(t)
	target := filepath.Join(root, "addons/mod_a/views/listed.xml")
	res, err := RunLint(LintOpts{Cfg: lintCfg(), Root: root, Args: []string{target}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 {
		t.Errorf("Files = %d, want 1", res.Files)
	}
	// The severity set is built from the addons roots, not just the
	// target, so an explicit path still gets the right severity.
	if res.Errors() != 1 {
		t.Errorf("Errors() = %d, want 1; findings: %+v", res.Errors(), res.Findings)
	}
}

func TestRunLintGrammarFallbackReported(t *testing.T) {
	root := lintFixture(t)
	cfg := lintCfg()
	cfg.OdooVersion = "21"
	res, err := RunLint(LintOpts{Cfg: cfg, Root: root, Args: []string{"mod_b"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.GrammarExact {
		t.Error("GrammarExact is true for an unknown major")
	}
	if res.GrammarMajor != "19" {
		t.Errorf("GrammarMajor = %q, want the newest embedded (19)", res.GrammarMajor)
	}
}

func TestHostAddonsRootsFallsBackInConfMode(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"addons", "custom"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Conf mode's paths are container paths and cannot be walked here.
	cfg := &config.Config{AddonsMode: addonsModeConf, AddonsPaths: []string{"/mnt/extra-addons"}}
	got := hostAddonsRoots(cfg, root)
	want := []string{root, filepath.Join(root, "addons"), filepath.Join(root, "custom")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hostAddonsRoots = %v, want %v", got, want)
	}
}

func TestLintModulesReportsMissing(t *testing.T) {
	root := lintFixture(t)
	res, missing, err := LintModules(lintCfg(), root, []string{"mod_a", "ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(missing, []string{"ghost"}) {
		t.Errorf("missing = %v, want [ghost]", missing)
	}
	if !reflect.DeepEqual(res.Modules, []string{"mod_a"}) {
		t.Errorf("Modules = %v, want [mod_a]", res.Modules)
	}
	if res.Errors() != 1 {
		t.Errorf("Errors() = %d, want 1", res.Errors())
	}
}

// A selection naming only modules that do not exist locally must not
// invent findings — nor an error.
func TestLintModulesAllMissingIsEmpty(t *testing.T) {
	root := lintFixture(t)
	res, missing, err := LintModules(lintCfg(), root, []string{"ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 {
		t.Errorf("missing = %v, want [ghost]", missing)
	}
	if len(res.Findings) != 0 || res.Files != 0 {
		t.Errorf("result should be empty, got %+v", res)
	}
}

func TestRelPath(t *testing.T) {
	root := "/srv/odoo"
	if got := RelPath(root, "/srv/odoo/addons/m/v.xml"); got != "addons/m/v.xml" {
		t.Errorf("RelPath inside root = %q", got)
	}
	if got := RelPath(root, "/etc/other.xml"); got != "/etc/other.xml" {
		t.Errorf("RelPath outside root = %q, want it unchanged", got)
	}
}
