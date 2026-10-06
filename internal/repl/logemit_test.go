package repl

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// renderOdooLog must produce a self-contained styled line carrying the
// logger and message — this is what the shell transform uses to reformat
// loose-severity stderr (wkhtmltopdf `Warn:` …) into Echo's Odoo style.
func TestRenderOdooLogLooseSeverity(t *testing.T) {
	p := theme.PaletteByName("")
	s := theme.New(p, theme.StageFromString("dev"))

	ll, ok := parseLooseSeverity("Warn: Can't find .pfb for face 'Courier'")
	if !ok || ll.level != "WARNING" {
		t.Fatalf("parseLooseSeverity = %+v ok=%v", ll, ok)
	}
	out := renderOdooLog(ll.level, looseSeverityLogger, ll.message, nil, s, p, "habitta_prod")
	for _, want := range []string{looseSeverityLogger, "Can't find .pfb", "habitta_prod"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered loose line missing %q: %q", want, out)
		}
	}
}

func enableJSONLogs(t *testing.T) {
	t.Helper()
	jsonLogs = true
	t.Cleanup(func() { jsonLogs = false })
}

func TestEmitOdooLogJSON(t *testing.T) {
	enableJSONLogs(t)
	p := theme.PaletteByName("")
	s := theme.New(p, theme.StageDev)
	fields := []logField{{"modules", "sale"}, {"from", "develop"}, {"note", "two words"}}

	var buf bytes.Buffer
	emitOdooLogTo(&buf, "INFO", "echo.update.start", "update started", fields, s, p, "muutrade")
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("want one newline-terminated line, got %q", out)
	}
	var got jsonLogLine
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	want := [][2]string{{"modules", "sale"}, {"from", "develop"}, {"note", "two words"}}
	if !reflect.DeepEqual(got.Fields, want) {
		t.Errorf("fields = %v, want %v", got.Fields, want)
	}
	if got.PID != os.Getpid() || got.Level != "INFO" || got.DB != "muutrade" ||
		got.Logger != "echo.update.start" || got.Msg != "update started" {
		t.Errorf("line = %+v", got)
	}
	ts, err := time.Parse("2006-01-02T15:04:05.000Z07:00", got.Time)
	if err != nil {
		t.Fatalf("time %q: %v", got.Time, err)
	}
	if plain := plainOdooLogFields(ts, "INFO", "echo.update.start", "update started", fields, "muutrade"); got.Text != plain {
		t.Errorf("text = %q, want %q", got.Text, plain)
	}
}

func TestEmitOdooLogJSONWithoutFields(t *testing.T) {
	enableJSONLogs(t)
	p := theme.PaletteByName("")
	s := theme.New(p, theme.StageDev)

	for _, level := range []string{"DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"} {
		var buf bytes.Buffer
		emitOdooLogTo(&buf, level, "echo.modules", "listed", nil, s, p, "")
		if strings.Count(buf.String(), "\n") != 1 {
			t.Fatalf("%s: want one line, got %q", level, buf.String())
		}
		var m map[string]any
		if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
			t.Fatalf("%s: %q is not JSON: %v", level, buf.String(), err)
		}
		if m["level"] != level {
			t.Errorf("level = %v, want %s", m["level"], level)
		}
		if m["db"] != "-" {
			t.Errorf("%s: db = %v, want -", level, m["db"])
		}
		if _, ok := m["fields"]; ok {
			t.Errorf("%s: fields present with none: %q", level, buf.String())
		}
	}
}

func TestPrintJSON(t *testing.T) {
	enableJSONLogs(t)
	sess := cmdLogSession(t)
	odooLine := "2026-10-06 18:04:05,123 4242 WARNING muutrade odoo.modules.loading: module sale: not installable"

	out := captureStdout(t, func() {
		sess.print(Line{Kind: "out", Text: odooLine})
		sess.print(Line{Kind: "ok", Text: "done"})
		sess.printStyled("\x1b[32m+ added.py\x1b[0m", "+ added.py", "err")
	})
	if strings.Contains(out, "\x1b[") {
		t.Errorf("JSON output carries ANSI: %q", out)
	}
	var got []config.ReportLine
	for _, raw := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		var l config.ReportLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("line %q is not JSON: %v", raw, err)
		}
		got = append(got, l)
	}
	want := []config.ReportLine{
		{Level: "WARNING", Text: odooLine},
		{Level: "", Text: "done"},
		{Level: "ERROR", Text: "+ added.py"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %+v, want %+v", got, want)
	}
}

func TestPrintJSONSuppressed(t *testing.T) {
	enableJSONLogs(t)
	sess := cmdLogSession(t)
	suppressLevel = silentAll
	t.Cleanup(func() { suppressLevel = -1 })

	out := captureStdout(t, func() {
		sess.print(Line{Kind: "err", Text: "boom"})
		sess.printStyled("styled", "plain", "info")
	})
	if out != "" {
		t.Errorf("suppressed lines printed %q", out)
	}
}

func TestLogFormatEnv(t *testing.T) {
	prevCapture, prevRemote, prevDBMax := captureLine, cmd.OnRemoteResolved, logDBMax
	t.Cleanup(func() {
		captureLine, cmd.OnRemoteResolved, logDBMax, jsonLogs = prevCapture, prevRemote, prevDBMax, false
	})
	p := theme.PaletteByName("")
	s := theme.New(p, theme.StageDev)

	for _, tc := range []struct {
		value string
		json  bool
	}{{"json", true}, {"yaml", false}, {"", false}} {
		t.Setenv("ECHO_LOG_FORMAT", tc.value)
		newSession(s, p, "proj", "id", theme.StageDev, "", "", "", t.TempDir(), &config.Config{LogDBMax: 20})
		if jsonLogs != tc.json {
			t.Errorf("ECHO_LOG_FORMAT=%q: jsonLogs = %v, want %v", tc.value, jsonLogs, tc.json)
		}
	}
}
