package repl

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeOdoo points the session at a compose stand-in that, for `exec -T odoo
// odoo …`, prints logText and exits with rc; any other exec fails, so the
// container-side module listing finds nothing.
func fakeOdoo(t *testing.T, logText string, rc int) *session {
	t.Helper()
	sess := cmdLogSession(t)
	wireCapture(t, sess)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "odoo.log")
	if err := os.WriteFile(logPath, []byte(logText), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n[ \"$4\" = odoo ] || exit 1\ncat '" + logPath + "'\nexit " + strconv.Itoa(rc) + "\n"
	compose := filepath.Join(dir, "compose")
	if err := os.WriteFile(compose, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sess.projectDir = t.TempDir()
	sess.cfg.ComposeCmd = compose
	sess.cfg.OdooContainer = "odoo"
	sess.cfg.DBContainer = "db"

	t.Setenv("ECHO_NO_CLIPBOARD", "1")
	return sess
}

// dispatchCaptured runs one command and returns what it wrote to stdout and
// stderr.
func dispatchCaptured(t *testing.T, sess *session, name string, args ...string) (stdout, stderr string) {
	t.Helper()
	read := func(r *os.File, into *string, done chan struct{}) {
		b, _ := io.ReadAll(r)
		*into = string(b)
		close(done)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outDone, errDone := make(chan struct{}), make(chan struct{})
	go read(outR, &stdout, outDone)
	go read(errR, &stderr, errDone)
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	sess.dispatchParsed(context.Background(), name, args)
	os.Stdout, os.Stderr = origOut, origErr
	outW.Close()
	errW.Close()
	<-outDone
	<-errDone
	return stdout, stderr
}

const tallyTwelve = "2026-10-19 10:00:01,000 7 INFO muutrade odoo.tests.result: 0 failed, 0 error(s) of 12 tests when loading database 'muutrade' \n"

func TestModuleUsageErrorExitsTwoWithoutCopy(t *testing.T) {
	sess := fakeOdoo(t, tallyTwelve, 0)
	for _, args := range [][]string{
		{"install", "sale", "--bogus"},
		{"update", "sale", "--level", "loud"},
		{"uninstall", "sale", "--nope"},
		{"test", "sale", "--tags"},
	} {
		out, _ := dispatchCaptured(t, sess, args[0], args[1:]...)
		if sess.exitCode != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, sess.exitCode, exitUsage)
		}
		if !strings.Contains(out, "usage error") {
			t.Errorf("%v: output does not name the usage error: %q", args, out)
		}
		if strings.Contains(out, "copied=") {
			t.Errorf("%v: a usage error went through the failure copy: %q", args, out)
		}
	}
}

func TestTestWithoutReportFailsNoTestsRan(t *testing.T) {
	log := "2026-10-19 10:00:00,000 7 INFO muutrade odoo.modules.loading: 80 modules loaded in 0.50s, 0 queries \n"
	sess := fakeOdoo(t, log, 0)
	out, _ := dispatchCaptured(t, sess, "test", "sale")
	if sess.exitCode != exitError {
		t.Fatalf("exit %d, want %d\n%s", sess.exitCode, exitError, out)
	}
	if !strings.Contains(out, "no tests ran") || !strings.Contains(out, "tags=/sale") {
		t.Errorf("missing the no-tests line:\n%s", out)
	}
}

func TestTestWithZeroTallyFails(t *testing.T) {
	log := "2026-10-19 10:00:01,000 7 WARNING muutrade odoo.tests.result: 0 failed, 0 error(s) of 0 tests when loading database 'muutrade' \n"
	sess := fakeOdoo(t, log, 0)
	out, _ := dispatchCaptured(t, sess, "test", "sale", "--tags", ":TestTypo")
	if sess.exitCode != exitError || !strings.Contains(out, "tags=:TestTypo") {
		t.Fatalf("exit %d, want %d with the tags named\n%s", sess.exitCode, exitError, out)
	}
}

// failingTestLog follows odoo/tests/result.py logError and the server's
// tally on 17, 18 and 19.
const failingTestLog = `2026-10-19 10:00:00,100 7 WARNING muutrade odoo.fields: Field sale.order.x: unknown parameter 'tracking'
2026-10-19 10:00:01,050 7 ERROR muutrade odoo.addons.sale_custom.tests.test_order: FAIL: TestOrder.test_confirm
Traceback (most recent call last):
  File "/mnt/extra-addons/sale_custom/tests/test_order.py", line 88, in test_confirm
    self.assertEqual(order.state, 'sale')
AssertionError: 'draft' != 'sale'
2026-10-19 10:00:01,400 7 ERROR muutrade odoo.modules.loading: At least one test failed when loading the modules.
2026-10-19 10:00:02,000 7 ERROR muutrade odoo.tests.result: 1 failed, 0 error(s) of 4 tests when loading database 'muutrade'
`

func TestTestJSONReportsFailures(t *testing.T) {
	sess := fakeOdoo(t, failingTestLog, 1)
	out, stderr := dispatchCaptured(t, sess, "test", "sale_custom", "--json")
	if sess.exitCode != exitError {
		t.Fatalf("exit %d, want %d", sess.exitCode, exitError)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("stdout is not one JSON line: %q", out)
	}
	var got moduleRunJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout: %v\n%s", err, out)
	}
	if got.Command != "test" || got.Target != nil || got.DB != "muutrade" || got.Exit != exitError ||
		len(got.Modules) != 1 || got.Modules[0] != "sale_custom" {
		t.Errorf("header = %+v", got)
	}
	if got.Tests == nil || got.Tests.Run != 4 || got.Tests.Failed != 1 || len(got.Tests.Failures) != 1 {
		t.Fatalf("tests = %+v", got.Tests)
	}
	f := got.Tests.Failures[0]
	if f.Kind != "FAIL" || f.Test != "TestOrder.test_confirm" || f.Module != "sale_custom" ||
		f.Line != 88 || f.Message != "AssertionError: 'draft' != 'sale'" {
		t.Errorf("failure = %+v", f)
	}
	if len(got.Errors) != 1 || len(got.Warnings) != 1 || got.Migrations == nil {
		t.Errorf("errors = %+v warnings = %+v migrations = %v", got.Errors, got.Warnings, got.Migrations)
	}
	if !strings.Contains(stderr, "FAIL: TestOrder.test_confirm") || !strings.Contains(stderr, "echo.test") {
		t.Errorf("the run's lines did not reach stderr:\n%s", stderr)
	}
}

func TestModuleJSONUsageErrorKeepsStdoutEmpty(t *testing.T) {
	sess := fakeOdoo(t, "", 0)
	out, stderr := dispatchCaptured(t, sess, "update", "sale", "--json", "--bogus")
	if sess.exitCode != exitUsage || out != "" {
		t.Fatalf("exit %d stdout %q, want %d and nothing", sess.exitCode, out, exitUsage)
	}
	if !strings.Contains(stderr, "unknown flag") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestUpdateJSONCarriesTheTraceback(t *testing.T) {
	log := `2026-10-19 11:00:01,000 9 ERROR muutrade odoo.modules.registry: Failed to load registry
Traceback (most recent call last):
  File "/mnt/extra-addons/sale_custom/models/sale_order.py", line 30, in _compute_total
    raise ValueError("bad total")
ValueError: bad total
`
	sess := fakeOdoo(t, log, 1)
	out, _ := dispatchCaptured(t, sess, "update", "sale", "--json")
	var got moduleRunJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout: %v\n%s", err, out)
	}
	if got.Exit != exitError || got.Tests != nil || len(got.Errors) != 1 {
		t.Fatalf("got %+v", got)
	}
	e := got.Errors[0]
	if e.Exception != "ValueError: bad total" || len(e.Frames) != 1 || e.Frames[0].Line != 30 || e.Frames[0].Func != "_compute_total" {
		t.Errorf("error = %+v", e)
	}
}

func TestTestHumanListsFailuresAfterClosing(t *testing.T) {
	sess := fakeOdoo(t, failingTestLog, 1)
	out, _ := dispatchCaptured(t, sess, "test", "sale_custom")
	closing := strings.Index(out, "test failed")
	failure := strings.Index(out, "echo.test.failure")
	if closing < 0 || failure < closing {
		t.Fatalf("failure line not after the closing line:\n%s", out)
	}
	for _, want := range []string{"run=4", "failed=1", "errored=0", "test=TestOrder.test_confirm", "line=88"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %s:\n%s", want, out)
		}
	}
}

func TestTestWithTestsPasses(t *testing.T) {
	sess := fakeOdoo(t, tallyTwelve, 0)
	out, _ := dispatchCaptured(t, sess, "test", "sale")
	if sess.exitCode != exitOK || strings.Contains(out, "no tests ran") {
		t.Fatalf("exit %d, want %d\n%s", sess.exitCode, exitOK, out)
	}
}

func TestModuleJSONCarriesTheRunError(t *testing.T) {
	sess := fakeOdoo(t, "Port 8189 is in use by another program.\n", 1)
	out, _ := dispatchCaptured(t, sess, "test", "sale_custom", "--json")
	var got moduleRunJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout: %v\n%s", err, out)
	}
	if got.Exit != exitError || got.Error == "" {
		t.Fatalf("exit %d error %q, want %d and the run error", got.Exit, got.Error, exitError)
	}
	if got.Target != nil {
		t.Errorf("a local run has no target, got %q", *got.Target)
	}
}
