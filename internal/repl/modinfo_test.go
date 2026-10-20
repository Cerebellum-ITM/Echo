package repl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeModuleDB points the session at a compose stand-in whose psql answers
// the module-state query with sale and stock installed at 18.0.1.0, and at a
// project whose manifests put sale in sync and stock one version ahead.
func fakeModuleDB(t *testing.T) *session {
	t.Helper()
	sess := cmdLogSession(t)
	wireCapture(t, sess)
	dir := t.TempDir()
	rows := filepath.Join(dir, "rows")
	writeTestFile(t, rows, "sale|installed|18.0.1.0|f\nstock|installed|18.0.1.0|f\n")
	compose := filepath.Join(dir, "compose")
	writeTestFile(t, compose, "#!/bin/sh\ncase \"$*\" in *ir_module_module*) cat '"+rows+"' ;; *) exit 1 ;; esac\n")
	if err := os.Chmod(compose, 0o755); err != nil {
		t.Fatal(err)
	}
	sess.projectDir = t.TempDir()
	writeTestFile(t, filepath.Join(sess.projectDir, "addons/sale/__manifest__.py"), "{'version': '18.0.1.0'}")
	writeTestFile(t, filepath.Join(sess.projectDir, "addons/stock/__manifest__.py"), "{'version': '1.1'}")
	sess.cfg.ComposeCmd = compose
	sess.cfg.OdooContainer = "odoo"
	sess.cfg.DBContainer = "db"
	sess.cfg.OdooVersion = "18"
	t.Setenv("ECHO_NO_CLIPBOARD", "1")
	return sess
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestModinfoJSONSeveralModules(t *testing.T) {
	sess := fakeModuleDB(t)
	out, _ := dispatchCaptured(t, sess, "modinfo", "sale", "stock", "ghost", "--json")
	if sess.exitCode != exitOK {
		t.Fatalf("exit %d, want %d", sess.exitCode, exitOK)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, out)
	}
	want := []map[string]any{
		{"module": "sale", "state": "installed", "db_version": "18.0.1.0", "manifest_version": "18.0.1.0", "status": "in sync"},
		{"module": "stock", "state": "installed", "db_version": "18.0.1.0", "manifest_version": "1.1", "status": "update pending"},
		{"module": "ghost", "state": "", "db_version": "", "manifest_version": "", "status": "not installed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("json = %v, want %v (no deployed_version locally)", got, want)
	}
}

func TestModinfoHumanSeveralModules(t *testing.T) {
	sess := fakeModuleDB(t)
	out, _ := dispatchCaptured(t, sess, "modinfo", "sale", "stock", "ghost")
	out = stripANSISeq(out)
	if sess.exitCode != exitOK {
		t.Fatalf("exit %d, want %d\n%s", sess.exitCode, exitOK, out)
	}
	for _, want := range []string{
		`module=sale db=18.0.1.0 state=installed manifest=18.0.1.0 status="in sync"`,
		`module=stock db=18.0.1.0 state=installed manifest=1.1 status="update pending"`,
		`module=ghost db=- state=absent manifest=- status="not installed"`,
		"checked modules=3 pending=1 ahead=0 missing=1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "deployed=") {
		t.Errorf("a local run printed deployed=:\n%s", out)
	}
	if !reflect.DeepEqual(sess.lastModinfoModules, []string{"sale", "stock", "ghost"}) {
		t.Errorf("--last memory = %v", sess.lastModinfoModules)
	}
}

func TestModinfoUsageErrorsExitTwo(t *testing.T) {
	sess := fakeModuleDB(t)
	sess.lastModinfoModules = []string{"sale"}
	for _, args := range [][]string{
		{"--all", "sale"},
		{"--all", "sale", "--json"},
		{"--last", "--remote"},
		{"--last", "--from", "stg", "--json"},
	} {
		out, _ := dispatchCaptured(t, sess, "modinfo", args...)
		if sess.exitCode != exitUsage {
			t.Errorf("%v: exit %d, want %d\n%s", args, sess.exitCode, exitUsage, out)
		}
		if strings.Contains(strings.Join(args, " "), "--json") && out != "" {
			t.Errorf("%v: stdout is not empty: %q", args, out)
		}
	}
}
