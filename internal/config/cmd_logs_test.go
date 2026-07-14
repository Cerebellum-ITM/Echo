package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// saveAt writes a record stamped at a given time under root, returning its
// path. The filename's millis prefix drives both ordering and the age pass.
func saveAt(t *testing.T, root, command string, started time.Time) {
	t.Helper()
	rec := CmdLogRecord{
		Cmd:     command + " sale",
		Command: command,
		DB:      "muutrade",
		Stage:   "dev",
		Exit:    0,
		Started: started,
		Lines:   []ReportLine{{Level: "INFO", Text: "hello"}},
	}
	if err := SaveCmdLog(root, rec); err != nil {
		t.Fatalf("SaveCmdLog: %v", err)
	}
}

func TestCmdLogSaveListRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := "/some/project"

	now := time.Now()
	saveAt(t, root, "update", now.Add(-2*time.Second).Truncate(time.Millisecond))
	saveAt(t, root, "install", now.Add(-1*time.Second).Truncate(time.Millisecond))

	metas, err := ListCmdLogs(root)
	if err != nil {
		t.Fatalf("ListCmdLogs: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("expected 2 records, got %d", len(metas))
	}
	// Newest first.
	if metas[0].Command != "install" || metas[1].Command != "update" {
		t.Fatalf("expected newest-first [install, update], got [%s, %s]",
			metas[0].Command, metas[1].Command)
	}
	if metas[0].DB != "muutrade" || metas[0].Stage != "dev" || metas[0].Cmd != "install sale" {
		t.Fatalf("metadata not preserved: %+v", metas[0])
	}

	// Full-record load carries the lines.
	rec, ok := LoadCmdLog(metas[0].Path)
	if !ok {
		t.Fatalf("LoadCmdLog(%s) = false", metas[0].Path)
	}
	if len(rec.Lines) != 1 || rec.Lines[0].Text != "hello" {
		t.Fatalf("lines not preserved: %+v", rec.Lines)
	}
}

func TestCmdLogDeployedTipRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := "/some/project"

	rec := CmdLogRecord{
		Cmd:         "deploy --commits abc123",
		Command:     "watch-deploy",
		DB:          "muutrade",
		Stage:       "staging",
		Exit:        0,
		Started:     time.Now().Truncate(time.Millisecond),
		DeployedTip: "abc123def456",
		Lines:       []ReportLine{{Level: "INFO", Text: "ok"}},
	}
	if err := SaveCmdLog(root, rec); err != nil {
		t.Fatalf("SaveCmdLog: %v", err)
	}

	metas, err := ListCmdLogs(root)
	if err != nil {
		t.Fatalf("ListCmdLogs: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 record, got %d", len(metas))
	}
	if metas[0].DeployedTip != "abc123def456" || metas[0].Command != "watch-deploy" {
		t.Fatalf("DeployedTip not carried into meta: %+v", metas[0])
	}
	// A record with no tip omits it entirely (omitempty), and the meta stays "".
	full, ok := LoadCmdLog(metas[0].Path)
	if !ok || full.DeployedTip != "abc123def456" {
		t.Fatalf("DeployedTip not persisted in record: ok=%v tip=%q", ok, full.DeployedTip)
	}
}

func TestCmdLogListMissingDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	metas, err := ListCmdLogs("/never/saved")
	if err != nil {
		t.Fatalf("ListCmdLogs on missing dir: %v", err)
	}
	if len(metas) != 0 {
		t.Fatalf("expected no records, got %d", len(metas))
	}
}

func TestPruneCmdLogsAge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := "/proj/age"

	now := time.Now()
	saveAt(t, root, "old", now.Add(-10*24*time.Hour)) // 10 days old
	saveAt(t, root, "fresh", now.Add(-1*time.Hour))   // recent

	removed, err := PruneCmdLogs(root, 7, 0) // 7-day retention, no count cap
	if err != nil {
		t.Fatalf("PruneCmdLogs: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}
	metas, _ := ListCmdLogs(root)
	if len(metas) != 1 || metas[0].Command != "fresh" {
		t.Fatalf("expected only [fresh] to survive, got %+v", metas)
	}
}

func TestPruneCmdLogsCount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := "/proj/count"

	now := time.Now()
	for i := 0; i < 5; i++ {
		saveAt(t, root, "cmd", now.Add(time.Duration(i)*time.Millisecond))
	}

	removed, err := PruneCmdLogs(root, 0, 3) // no age pass, keep newest 3
	if err != nil {
		t.Fatalf("PruneCmdLogs: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 removed, got %d", removed)
	}
	if metas, _ := ListCmdLogs(root); len(metas) != 3 {
		t.Fatalf("expected 3 survivors, got %d", len(metas))
	}
}

func TestPruneCmdLogsZeroDisablesPasses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := "/proj/zero"

	now := time.Now()
	saveAt(t, root, "ancient", now.Add(-100*24*time.Hour))
	for i := 0; i < 4; i++ {
		saveAt(t, root, "c", now.Add(time.Duration(i)*time.Millisecond))
	}

	removed, err := PruneCmdLogs(root, 0, 0) // both passes disabled
	if err != nil {
		t.Fatalf("PruneCmdLogs: %v", err)
	}
	if removed != 0 {
		t.Fatalf("expected nothing removed, got %d", removed)
	}
}

func TestLoadCmdLogCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1751847123456-update.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadCmdLog(path); ok {
		t.Fatal("expected corrupt file to load as (zero, false)")
	}
	if _, ok := LoadCmdLog(filepath.Join(dir, "missing.json")); ok {
		t.Fatal("expected missing file to load as (zero, false)")
	}
}

func TestClearCmdLogs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := "/proj/clear"
	now := time.Now()
	for i := 0; i < 3; i++ {
		saveAt(t, root, "c", now.Add(time.Duration(i)*time.Millisecond))
	}
	removed, err := ClearCmdLogs(root)
	if err != nil {
		t.Fatalf("ClearCmdLogs: %v", err)
	}
	if removed != 3 {
		t.Fatalf("expected 3 removed, got %d", removed)
	}
	if metas, _ := ListCmdLogs(root); len(metas) != 0 {
		t.Fatalf("expected empty after clear, got %d", len(metas))
	}
}
