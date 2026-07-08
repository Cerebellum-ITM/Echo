package cmd

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pascualchavez/echo/internal/config"
)

// marshal a record the way SaveCmdLog does (indented) so the parser sees the
// same shape it would over SSH.
func marshalRec(t *testing.T, r config.CmdLogRecord) string {
	t.Helper()
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseRemoteCmdLogs(t *testing.T) {
	older := config.CmdLogRecord{
		Cmd: "update sale", Command: "update", DB: "muutrade", Exit: 0,
		Started: time.Date(2026, 7, 8, 9, 0, 0, 0, time.UTC),
		Lines:   []config.ReportLine{{Level: "INFO", Text: "one"}},
	}
	newer := config.CmdLogRecord{
		Cmd: "restart", Command: "restart", DB: "muutrade", Exit: 1,
		Started: time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC),
		Lines:   []config.ReportLine{{Level: "ERROR", Text: "boom"}, {Text: "trace"}},
	}
	// Filenames are millis-prefixed; the newer run sorts first (name desc).
	payload := "" +
		remoteCmdLogMarker + "1000000000000-update.json@@\n" + marshalRec(t, older) + "\n" +
		remoteCmdLogMarker + "1000000003600-restart.json@@\n" + marshalRec(t, newer) + "\n"

	metas, byPath := parseRemoteCmdLogs(payload)
	if len(metas) != 2 {
		t.Fatalf("got %d metas, want 2", len(metas))
	}
	if metas[0].Command != "restart" {
		t.Fatalf("newest first expected restart, got %q", metas[0].Command)
	}
	if metas[0].LineCount != 2 || metas[0].Exit != 1 {
		t.Fatalf("restart meta wrong: lines=%d exit=%d", metas[0].LineCount, metas[0].Exit)
	}
	if metas[1].Command != "update" {
		t.Fatalf("second meta expected update, got %q", metas[1].Command)
	}
	// The map is keyed by basename (meta.Path) for on-demand detail loading.
	rec, ok := byPath[metas[0].Path]
	if !ok || rec.Cmd != "restart" || len(rec.Lines) != 2 {
		t.Fatalf("byPath lookup failed for %q: ok=%v", metas[0].Path, ok)
	}
}

func TestParseRemoteCmdLogsEmpty(t *testing.T) {
	metas, byPath := parseRemoteCmdLogs("")
	if len(metas) != 0 || len(byPath) != 0 {
		t.Fatalf("empty payload should yield nothing, got %d/%d", len(metas), len(byPath))
	}
	// A garbage section is skipped, not fatal.
	metas, _ = parseRemoteCmdLogs(remoteCmdLogMarker + "x.json@@\nnot json\n")
	if len(metas) != 0 {
		t.Fatalf("unparseable section should be skipped, got %d", len(metas))
	}
}
