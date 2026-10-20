package repl

import (
	"context"
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

func TestTestWithTestsPasses(t *testing.T) {
	sess := fakeOdoo(t, tallyTwelve, 0)
	out, _ := dispatchCaptured(t, sess, "test", "sale")
	if sess.exitCode != exitOK || strings.Contains(out, "no tests ran") {
		t.Fatalf("exit %d, want %d\n%s", sess.exitCode, exitOK, out)
	}
}
