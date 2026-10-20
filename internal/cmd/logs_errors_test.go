package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParseLogErrorsArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    logErrorsArgs
		wantErr bool
	}{
		{"plain logs", []string{"-t", "50"}, logErrorsArgs{count: 5, tail: "50"}, false},
		{"defaults", []string{"--errors"}, logErrorsArgs{enabled: true, count: 5, tail: "2000"}, false},
		{"count and tail", []string{"--errors", "-n", "2", "-t", "500", "--json"}, logErrorsArgs{enabled: true, count: 2, tail: "500"}, false},
		{"remote value skipped", []string{"--errors", "--from", "-n"}, logErrorsArgs{enabled: true, count: 5, tail: "2000"}, false},
		{"zero count", []string{"--errors", "-n", "0"}, logErrorsArgs{}, true},
		{"bad count", []string{"--errors", "-n", "x"}, logErrorsArgs{}, true},
		{"missing count", []string{"--errors", "-n"}, logErrorsArgs{}, true},
		{"follow", []string{"--errors", "--follow"}, logErrorsArgs{}, true},
		{"copy", []string{"--errors", "-c"}, logErrorsArgs{}, true},
		{"count without errors", []string{"-n", "3"}, logErrorsArgs{}, true},
		{"json without errors", []string{"--json"}, logErrorsArgs{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLogErrorsArgs(tc.args)
			if tc.wantErr {
				if !errors.Is(err, ErrUsage) {
					t.Fatalf("err = %v, want ErrUsage", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestParseLogsArgsSkipsErrorFlags(t *testing.T) {
	_, _, _, _, services := parseLogsArgs([]string{"--errors", "-n", "3", "--json", "web"})
	if !reflect.DeepEqual(services, []string{"web"}) {
		t.Errorf("services = %v, want [web]", services)
	}
}

// serviceLog is `compose logs --no-log-prefix` of an Odoo 17/18/19 service:
// two tracebacks among request and cron noise, one header colored by
// Odoo's ColoredFormatter as stored when the container ran with a TTY.
const serviceLog = "2026-10-19 09:00:00,001 1 INFO ? odoo: Odoo version 18.0 \n" +
	"2026-10-19 09:00:05,100 1 INFO demo werkzeug: 172.18.0.1 - - [19/Oct/2026 09:00:05] \"POST /web/dataset/call_kw HTTP/1.1\" 200 - 12 0.010 0.020\n" +
	"2026-10-19 09:00:06,200 1 ERROR demo odoo.http: Exception during request handling. \n" +
	"Traceback (most recent call last):\n" +
	"  File \"/usr/lib/python3/dist-packages/odoo/http.py\", line 2375, in __call__\n" +
	"    response = request._serve_db()\n" +
	"  File \"/mnt/extra-addons/sale_custom/models/sale_order.py\", line 51, in action_confirm\n" +
	"    raise UserError(\"credit limit\")\n" +
	"odoo.exceptions.UserError: credit limit\n" +
	"2026-10-19 09:00:07,000 1 WARNING demo odoo.models: x is deprecated \n" +
	"2026-10-19 09:01:00,000 1 \x1b[1;31mERROR\x1b[0m \x1b[35mdemo\x1b[0m odoo.addons.base.models.ir_cron: Call from cron Sync for server action #12 failed in Job #3 \n" +
	"Traceback (most recent call last):\n" +
	"  File \"/mnt/extra-addons/sync/models/sync.py\", line 9, in run\n" +
	"    self._pull()\n" +
	"ConnectionError: timeout\n" +
	"2026-10-19 09:01:01,000 1 INFO demo odoo.addons.base.models.ir_cron: Job done \n"

func fakeComposeLogs(t *testing.T, text string) string {
	t.Helper()
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
	return compose
}

func TestRunLogErrorsLocal(t *testing.T) {
	cfg := &config.Config{ComposeCmd: fakeComposeLogs(t, serviceLog), OdooContainer: "odoo"}
	run := func(args ...string) LogErrors {
		res, err := RunLogErrors(context.Background(), DockerOpts{Cfg: cfg, Root: t.TempDir(), Args: args})
		if err != nil {
			t.Fatalf("RunLogErrors: %v", err)
		}
		return res
	}

	res := run("--errors")
	if res.Scanned != 16 || len(res.Errors) != 2 {
		t.Fatalf("scanned %d errors %d, want 16 and 2", res.Scanned, len(res.Errors))
	}
	first, last := res.Errors[0], res.Errors[1]
	if first.Logger != "odoo.http" || first.Exception != "odoo.exceptions.UserError: credit limit" || len(first.Frames) != 2 {
		t.Errorf("first = %+v", first)
	}
	if last.Time != "2026-10-19 09:01:00,000" || last.DB != "demo" || last.Exception != "ConnectionError: timeout" {
		t.Errorf("colored header not parsed: %+v", last)
	}
	if len(first.Lines) != 7 {
		t.Errorf("first record has %d lines, want header plus traceback (7)", len(first.Lines))
	}

	if res := run("--errors", "-n", "1"); len(res.Errors) != 1 || res.Errors[0].Exception != "ConnectionError: timeout" {
		t.Errorf("-n 1 kept %+v, want the newest error", res.Errors)
	}
}
