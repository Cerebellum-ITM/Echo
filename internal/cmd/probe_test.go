package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
)

func TestParseProbeArgs(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		want   odoo.ProbeRequest
		from   string
		remote bool
		json   bool
	}{
		{"unnamed -c", []string{"-c", "env.user.name"},
			odoo.ProbeRequest{Kind: "eval", Exprs: []odoo.ProbeExpr{{Expr: "env.user.name"}}}, "", false, false},
		{"named and unnamed -c", []string{"-c", "n=env['res.partner'].search_count([])", "-c", "1+1", "--json"},
			odoo.ProbeRequest{Kind: "eval", Exprs: []odoo.ProbeExpr{{Name: "n", Expr: "env['res.partner'].search_count([])"}, {Expr: "1+1"}}}, "", false, true},
		{"comparison is not a name", []string{"-c", "a == b", "-c", "a==b"},
			odoo.ProbeRequest{Kind: "eval", Exprs: []odoo.ProbeExpr{{Expr: "a == b"}, {Expr: "a==b"}}}, "", false, false},
		{"stdin script", []string{"-", "--remote"},
			odoo.ProbeRequest{Kind: "script"}, "", true, false},
		{"ref with --from consumed", []string{"ref", "--from", "staging", "base.main_company"},
			odoo.ProbeRequest{Kind: "ref", XMLID: "base.main_company"}, "staging", false, false},
		{"read with every flag", []string{"read", "sale.order", "--domain", `[["state","=","sale"]]`, "--fields", "name, amount_total", "--limit=5", "--order", "id desc"},
			odoo.ProbeRequest{Kind: "read", Model: "sale.order", Domain: json.RawMessage(`[["state","=","sale"]]`), Fields: []string{"name", "amount_total"}, Limit: 5, Order: "id desc"}, "", false, false},
		{"read count gets the default limit", []string{"read", "res.partner", "--count"},
			odoo.ProbeRequest{Kind: "read", Model: "res.partner", Count: true, Limit: 20}, "", false, false},
		{"fields with names", []string{"fields", "res.partner", "name", "vat"},
			odoo.ProbeRequest{Kind: "fields", Model: "res.partner", Names: []string{"name", "vat"}}, "", false, false},
		{"param prefix revealed", []string{"param", "database.", "--reveal", "--from=prod"},
			odoo.ProbeRequest{Kind: "param", Key: "database.", Reveal: true}, "prod", false, false},
		{"param lists all", []string{"param"},
			odoo.ProbeRequest{Kind: "param"}, "", false, false},
		{"arch with type", []string{"arch", "res.partner", "--type", "list"},
			odoo.ProbeRequest{Kind: "arch", XMLID: "res.partner", ViewType: "list"}, "", false, false},
		{"reverb env", []string{"-c", "1", "-E", "acme/main"},
			odoo.ProbeRequest{Kind: "eval", Exprs: []odoo.ProbeExpr{{Expr: "1"}}}, ReverbRef("acme/main"), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, from, remote, jsonOut, err := parseProbeArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(req, c.want) {
				t.Errorf("request\n got: %+v\nwant: %+v", req, c.want)
			}
			if from != c.from || remote != c.remote || jsonOut != c.json {
				t.Errorf("from=%q remote=%v json=%v, want %q %v %v", from, remote, jsonOut, c.from, c.remote, c.json)
			}
		})
	}
}

func TestParseProbeArgsUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"no form":              {},
		"unknown form":         {"write", "res.partner"},
		"unknown flag":         {"-c", "1", "--force"},
		"bad domain":           {"read", "res.partner", "--domain", "[('a','=',1)]"},
		"domain not an array":  {"read", "res.partner", "--domain", `{"a":1}`},
		"bad limit":            {"read", "res.partner", "--limit", "0"},
		"limit not a number":   {"read", "res.partner", "--limit", "ten"},
		"missing value":        {"-c"},
		"ref without xmlid":    {"ref"},
		"read flag on eval":    {"-c", "1", "--domain", "[]"},
		"type on read":         {"read", "res.partner", "--type", "form"},
		"-c with a positional": {"-c", "1", "ref"},
		"stdin with extras":    {"-", "extra"},
	}
	for name, args := range cases {
		if _, _, _, _, err := parseProbeArgs(args); !errors.Is(err, ErrUsage) {
			t.Errorf("%s: %v: want ErrUsage, got %v", name, args, err)
		}
	}
}

// probeFrameLines encodes payload as the template does, in chunks of size.
func probeFrameLines(payload string, size int) []string {
	data := base64.StdEncoding.EncodeToString([]byte(payload))
	var lines []string
	total := (len(data) + size - 1) / size
	for i := 0; i < total; i++ {
		end := min((i+1)*size, len(data))
		lines = append(lines, fmt.Sprintf("%s %d %d %s", odoo.ProbeFramePrefix, i+1, total, data[i*size:end]))
	}
	return lines
}

func fakeProbeExec(t *testing.T, lines []string, gotScript *string) {
	t.Helper()
	orig := probeLocalExec
	t.Cleanup(func() { probeLocalExec = orig })
	probeLocalExec = func(_ context.Context, _, _, container string, argv []string, r io.Reader, onLine func(string)) error {
		if container != "odoo" || !strings.Contains(strings.Join(argv, " "), "shell -d mydb") ||
			argv[len(argv)-1] != "--log-level=warn" {
			t.Errorf("unexpected exec: %s %v", container, argv)
		}
		body, _ := io.ReadAll(r)
		*gotScript = string(body)
		for _, l := range lines {
			onLine(l)
		}
		return nil
	}
}

func probeTestCfg() *config.Config {
	return &config.Config{OdooContainer: "odoo", DBContainer: "db", DBName: "mydb", Stage: "dev", OdooVersion: "18.0"}
}

func TestRunProbeLocalSeparatesFramesFromLogs(t *testing.T) {
	payload := `{"ok": true, "kind": "eval", "result": {"n": 42, "names": ["a", "b"]}, "stdout": "hi\n"}`
	frames := probeFrameLines(payload, 16)
	logs := []string{"2026-10-19 10:00:00,000 1 WARNING mydb odoo.modules: boot noise", "", "trailing log"}
	lines := []string{logs[0], frames[0], logs[1]}
	lines = append(lines, frames[1:]...)
	lines = append(lines, logs[2])
	var script string
	fakeProbeExec(t, lines, &script)

	var streamed []string
	res, err := RunProbe(context.Background(), ProbeOpts{
		Cfg:       probeTestCfg(),
		Root:      t.TempDir(),
		Args:      []string{"-c", "n=42"},
		StreamOut: func(l string) { streamed = append(streamed, l) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(streamed, logs) {
		t.Errorf("streamed %q, want only the log lines %q", streamed, logs)
	}
	if !res.OK || res.Kind != "eval" || res.Stdout != "hi\n" || res.DB != "mydb" || res.Stage != "dev" {
		t.Errorf("unexpected result %+v", res)
	}
	if string(res.Result) != `{"n": 42, "names": ["a", "b"]}` {
		t.Errorf("result %s", res.Result)
	}
	if res.Payload["target"] != nil || res.Payload["db"] != "mydb" {
		t.Errorf("payload %v", res.Payload)
	}
	if _, ok := res.Payload["duration_ms"]; !ok {
		t.Error("payload has no duration_ms")
	}
	if !strings.Contains(script, "ARGS = json.loads(base64.b64decode(\"") || strings.Contains(script, "n=42") {
		t.Error("script does not carry the request as base64 only")
	}
}

func TestRunProbePythonErrorIsProbeFailed(t *testing.T) {
	payload := `{"ok": false, "kind": "ref", "error": {"type": "ValueError", "message": "External ID not found: x.y",
		"traceback": "Traceback...", "file": "/odoo/models.py", "line": 7}, "stdout": ""}`
	var script string
	fakeProbeExec(t, probeFrameLines(payload, 64), &script)
	res, err := RunProbe(context.Background(), ProbeOpts{Cfg: probeTestCfg(), Root: t.TempDir(), Args: []string{"ref", "x.y"}})
	if !errors.Is(err, ErrProbeFailed) || !strings.Contains(err.Error(), "ValueError: External ID not found") {
		t.Fatalf("want ErrProbeFailed, got %v", err)
	}
	if res.OK || res.Error.File != "/odoo/models.py" || res.Error.Line != 7 || res.Payload == nil {
		t.Errorf("unexpected result %+v", res)
	}
}

func TestRunProbeWithoutFrameFails(t *testing.T) {
	var script string
	fakeProbeExec(t, []string{"Traceback (most recent call last):", "psycopg2.OperationalError: boom"}, &script)
	res, err := RunProbe(context.Background(), ProbeOpts{Cfg: probeTestCfg(), Root: t.TempDir(), Args: []string{"param"}})
	if err == nil || !strings.Contains(err.Error(), "probe produced no result") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want no-result error with context, got %v", err)
	}
	if res.Payload != nil {
		t.Error("no payload expected without a frame")
	}
}

func TestRunProbeStdinScript(t *testing.T) {
	origTTY := stdinIsTTY
	t.Cleanup(func() { stdinIsTTY = origTTY })

	stdinIsTTY = func() bool { return true }
	if _, err := RunProbe(context.Background(), ProbeOpts{Cfg: probeTestCfg(), Args: []string{"-"}}); !errors.Is(err, ErrUsage) {
		t.Fatalf("TTY stdin: want ErrUsage, got %v", err)
	}

	stdinIsTTY = func() bool { return false }
	var script string
	fakeProbeExec(t, probeFrameLines(`{"ok": true, "kind": "script", "result": null, "stdout": ""}`, 64), &script)
	_, err := RunProbe(context.Background(), ProbeOpts{
		Cfg:   probeTestCfg(),
		Root:  t.TempDir(),
		Args:  []string{"-"},
		Stdin: strings.NewReader("result = env.user.login"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "env.user.login") {
		t.Error("the piped script must travel as base64, not as source")
	}
}
