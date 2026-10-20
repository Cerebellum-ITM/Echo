package repl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twoTracebacks = "2026-10-19 09:00:00,001 1 INFO ? odoo: Odoo version 18.0 \n" +
	"2026-10-19 09:00:06,200 1 ERROR demo odoo.http: Exception during request handling. \n" +
	"Traceback (most recent call last):\n" +
	"  File \"/mnt/extra-addons/sale_custom/models/sale_order.py\", line 51, in action_confirm\n" +
	"    raise UserError(\"credit limit\")\n" +
	"odoo.exceptions.UserError: credit limit\n" +
	"2026-10-19 09:00:07,000 1 INFO demo werkzeug: 172.18.0.1 - - \"GET /web HTTP/1.1\" 200 - 3 0.002 0.004\n" +
	"2026-10-19 09:01:00,000 1 CRITICAL demo odoo.service.server: Failed to initialize database `demo`. \n" +
	"Traceback (most recent call last):\n" +
	"  File \"/usr/lib/python3/dist-packages/odoo/service/server.py\", line 1316, in preload_registries\n" +
	"    registry = Registry.new(dbname)\n" +
	"psycopg2.OperationalError: connection refused\n"

func logsSession(t *testing.T, text string) *session {
	t.Helper()
	sess := cmdLogSession(t)
	wireCapture(t, sess)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "service.log")
	if err := os.WriteFile(logPath, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(dir, "compose")
	script := "#!/bin/sh\n[ \"$1\" = logs ] || exit 1\ncat '" + logPath + "'\n"
	if err := os.WriteFile(compose, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sess.projectDir = t.TempDir()
	sess.cfg.ComposeCmd = compose
	sess.cfg.OdooContainer = "odoo"
	return sess
}

func TestLogsErrorsJSON(t *testing.T) {
	sess := logsSession(t, twoTracebacks)
	out, _ := dispatchCaptured(t, sess, "logs", "--errors", "--json")
	if sess.exitCode != exitOK {
		t.Fatalf("exit %d, want 0 even with errors found", sess.exitCode)
	}
	var got struct {
		Scanned int `json:"scanned"`
		Errors  []struct {
			Time, DB, Logger, Message, Exception string
			Frames                               []struct {
				File string
				Line int
				Func string
			}
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not the JSON object: %v\n%q", err, out)
	}
	if got.Scanned != 12 || len(got.Errors) != 2 {
		t.Fatalf("got %+v", got)
	}
	e := got.Errors[1]
	if e.Time != "2026-10-19 09:01:00,000" || e.DB != "demo" || e.Logger != "odoo.service.server" ||
		e.Exception != "psycopg2.OperationalError: connection refused" || len(e.Frames) != 1 || e.Frames[0].Line != 1316 {
		t.Errorf("error = %+v", e)
	}
}

func TestLogsErrorsHuman(t *testing.T) {
	sess := logsSession(t, twoTracebacks)
	out, _ := dispatchCaptured(t, sess, "logs", "--errors", "-n", "1")
	if sess.exitCode != exitOK {
		t.Fatalf("exit %d", sess.exitCode)
	}
	if strings.Contains(out, "credit limit") || !strings.Contains(out, "connection refused") {
		t.Errorf("-n 1 should show only the newest error:\n%s", out)
	}
	if strings.Contains(out, "werkzeug") || strings.Contains(out, "Odoo version") {
		t.Errorf("noise was printed:\n%s", out)
	}
	if !strings.Contains(out, "errors=1") || !strings.Contains(out, "scanned=12") {
		t.Errorf("closing line missing:\n%s", out)
	}
}

func TestLogsErrorsNoneFound(t *testing.T) {
	sess := logsSession(t, "2026-10-19 09:00:00,001 1 INFO ? odoo: Odoo version 18.0 \n")
	out, _ := dispatchCaptured(t, sess, "logs", "--errors")
	if sess.exitCode != exitOK || !strings.Contains(out, "no errors in the last 1 lines") {
		t.Fatalf("exit %d\n%s", sess.exitCode, out)
	}
}

func TestLogsErrorsUsage(t *testing.T) {
	sess := logsSession(t, "")
	for _, args := range [][]string{{"--errors", "--follow"}, {"--json"}, {"-n", "2"}} {
		out, _ := dispatchCaptured(t, sess, "logs", args...)
		if sess.exitCode != exitUsage {
			t.Errorf("logs %v: exit %d, want %d\n%s", args, sess.exitCode, exitUsage, out)
		}
	}
	out, _ := dispatchCaptured(t, sess, "logs", "--errors", "--json", "-n", "0")
	if sess.exitCode != exitUsage || out != "" {
		t.Errorf("--json usage error: exit %d stdout %q", sess.exitCode, out)
	}
}
