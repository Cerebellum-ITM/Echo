package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

// logLine is one captured progress line from the pre-flight.
type logLine struct {
	level  string
	sub    string
	msg    string
	fields map[string]string
}

// preflightFixture builds a project with a broken-but-listed module, a
// broken-but-unlisted file, and a clean module.
func preflightFixture(t *testing.T) string {
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
	// mod_bad: the defect is in a file the manifest lists, so the loader
	// will read it and abort.
	write("addons/mod_bad/__manifest__.py", "{'name': 'Bad', 'data': ['views/listed.xml']}")
	write("addons/mod_bad/views/listed.xml", "<odoo>\n<!-- roto -- aqui -->\n</odoo>\n")
	// mod_dead: the defect is in a file nothing lists.
	write("addons/mod_dead/__manifest__.py", "{'name': 'Dead', 'data': []}")
	write("addons/mod_dead/views/dead.xml", "<odoo>\n<!-- roto -- aqui -->\n</odoo>\n")
	// mod_ok: clean.
	write("addons/mod_ok/__manifest__.py", "{'name': 'Ok', 'data': ['views/ok.xml']}")
	write("addons/mod_ok/views/ok.xml", "<odoo>\n<record id=\"a\" model=\"m\"/>\n</odoo>\n")
	return root
}

// runPreflight calls the pre-flight with a capturing logger.
func runPreflight(t *testing.T, root string, p deployArgs, modules []string) (error, []logLine) {
	t.Helper()
	var lines []logLine
	opts := DeployOpts{
		Cfg:  &config.Config{OdooVersion: "18", AddonsPaths: []string{"addons"}},
		Root: root,
		Log: func(level, sub, msg, db string, fields ...[2]string) {
			f := map[string]string{}
			for _, kv := range fields {
				f[kv[0]] = kv[1]
			}
			lines = append(lines, logLine{level, sub, msg, f})
		},
	}
	return deployLintPreflight(opts, p, []lintScope{{root: root, modules: modules}}), lines
}

// hasLine reports whether any captured line is at level and its message
// contains sub.
func hasLine(lines []logLine, level, contains string) bool {
	for _, l := range lines {
		if l.level == level && strings.Contains(l.msg, contains) {
			return true
		}
	}
	return false
}

// A defect in a manifest-listed file of a selected module stops the run.
func TestDeployLintPreflightBlocks(t *testing.T) {
	root := preflightFixture(t)
	err, lines := runPreflight(t, root, deployArgs{}, []string{"mod_bad"})
	if !errors.Is(err, ErrLintBlocked) {
		t.Fatalf("err = %v, want ErrLintBlocked", err)
	}
	if !strings.Contains(err.Error(), "--no-lint") {
		t.Errorf("err = %q, want it to name the escape hatch", err)
	}
	if !hasLine(lines, "ERROR", "blocked the deploy") {
		t.Error("no ERROR line naming the block")
	}
	if !hasLine(lines, "ERROR", `invalid sequence "--"`) {
		t.Error("the finding itself was not reported")
	}
}

// The same defect in a file no manifest lists is reported and does not
// block: the loader never reads it, so blocking would make deploy
// stricter than Odoo.
func TestDeployLintPreflightWarnsOnUnlistedFile(t *testing.T) {
	root := preflightFixture(t)
	err, lines := runPreflight(t, root, deployArgs{}, []string{"mod_dead"})
	if err != nil {
		t.Fatalf("err = %v, want nil (nothing lists the broken file)", err)
	}
	if !hasLine(lines, "WARNING", `invalid sequence "--"`) {
		t.Error("the unlisted finding was not reported as a warning")
	}
	if !hasLine(lines, "INFO", "pre-flight clean") {
		t.Error("no clean verdict despite no blocking findings")
	}
}

// Scope is the selection, not the repo: a broken module that is not being
// deployed must not stop the run.
func TestDeployLintPreflightScopedToSelection(t *testing.T) {
	root := preflightFixture(t)
	err, lines := runPreflight(t, root, deployArgs{}, []string{"mod_ok"})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	for _, l := range lines {
		if strings.Contains(l.fields["file"], "mod_bad") {
			t.Errorf("reported a file outside the selection: %v", l.fields)
		}
	}
	if !hasLine(lines, "INFO", "pre-flight clean") {
		t.Error("no clean verdict for a clean selection")
	}
}

// --no-lint must skip the check and say so: a silent skip in a recorded
// run is how a default-on check turns into decoration.
func TestDeployLintPreflightNoLintSkipsAndLogs(t *testing.T) {
	root := preflightFixture(t)
	err, lines := runPreflight(t, root, deployArgs{noLint: true}, []string{"mod_bad"})
	if err != nil {
		t.Fatalf("err = %v, want nil with --no-lint", err)
	}
	if !hasLine(lines, "WARNING", "pre-flight skipped") {
		t.Error("--no-lint skipped silently")
	}
	if len(lines) != 1 {
		t.Errorf("got %d lines, want only the skip warning: %+v", len(lines), lines)
	}
}

// A selection naming a module that exists only on the server (a rename in
// flight) is reported, not treated as a failure.
func TestDeployLintPreflightMissingModuleIsWarning(t *testing.T) {
	root := preflightFixture(t)
	err, lines := runPreflight(t, root, deployArgs{}, []string{"ghost"})
	if err != nil {
		t.Fatalf("err = %v, want nil for a module absent locally", err)
	}
	if !hasLine(lines, "WARNING", "not found locally") {
		t.Error("a locally-absent module was not reported")
	}
}

func TestParseDeployArgsNoLint(t *testing.T) {
	p, err := parseDeployArgs([]string{"--no-lint"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.noLint {
		t.Error("--no-lint did not set noLint")
	}

	// It must compose with each of the ordinary selection flags, since the
	// pre-flight runs on whatever the selection produced.
	for _, args := range [][]string{
		{"--auto", "--no-lint"},
		{"--no-lint", "--modules", "a,b"},
		{"--no-lint", "--push"},
	} {
		p, err := parseDeployArgs(args)
		if err != nil {
			t.Fatalf("parseDeployArgs(%v): %v", args, err)
		}
		if !p.noLint {
			t.Errorf("parseDeployArgs(%v) lost --no-lint", args)
		}
	}

	// Default off.
	p, err = parseDeployArgs([]string{"--auto"})
	if err != nil {
		t.Fatal(err)
	}
	if p.noLint {
		t.Error("noLint defaults to true, want false (the check is on by default)")
	}
}

// The config-only and restore paths return before the selection resolves,
// so they never reach the pre-flight — they deploy no new code.
func TestDeployManagePathsBypassLint(t *testing.T) {
	for _, args := range [][]string{
		{"--set-push=true"},
		{"--rollback"},
		{"--restore-code"},
		{"--test-clear"},
	} {
		p, err := parseDeployArgs(args)
		if err != nil {
			t.Fatalf("parseDeployArgs(%v): %v", args, err)
		}
		manage := p.setPush != nil || p.isTestManage() || p.isCheckpointManage() ||
			p.rollback || p.restoreCodeSet
		if !manage {
			t.Errorf("parseDeployArgs(%v) is not a manage/restore path; it would reach the lint", args)
		}
	}
}

// The pre-flight inherits Unit 111 with no changes of its own: a
// listed-but-missing file blocks, an unlisted one does not.
func TestDeployLintPreflightManifestRules(t *testing.T) {
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
	write("addons/mod_gone/__manifest__.py", "{'name': 'G', 'data': ['views/ghost.xml']}")
	write("addons/mod_orphan/__manifest__.py", "{'name': 'O', 'data': []}")
	write("addons/mod_orphan/views/orphan.xml", "<odoo><record id=\"a\" model=\"m\"/></odoo>")

	err, lines := runPreflight(t, root, deployArgs{}, []string{"mod_gone"})
	if !errors.Is(err, ErrLintBlocked) {
		t.Fatalf("err = %v, want ErrLintBlocked for a listed-but-missing file", err)
	}
	if !hasLine(lines, "ERROR", "not on disk") {
		t.Error("the missing-entry finding was not reported")
	}

	err, lines = runPreflight(t, root, deployArgs{}, []string{"mod_orphan"})
	if err != nil {
		t.Fatalf("err = %v, want nil for an unlisted file", err)
	}
	if !hasLine(lines, "WARNING", "no manifest lists this file") {
		t.Error("the unlisted finding was not reported as a warning")
	}
}
