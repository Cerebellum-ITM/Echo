package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestCmdLogRemoteAndScriptFieldsRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := "/some/project"

	rec := CmdLogRecord{
		Cmd:                 "shell-run fix.py --from stg",
		Command:             "shell-run",
		DB:                  "local_db",
		Stage:               "dev",
		From:                "stg",
		Started:             time.Now().Truncate(time.Millisecond),
		Lines:               []ReportLine{{Text: "190"}},
		Target:              "stg",
		Host:                "stg.example",
		RemoteDB:            "stg_db",
		RemoteStage:         "staging",
		ScriptPath:          "/proj/scripts/fix.py",
		ScriptSHA256:        "abc",
		ScriptBody:          "print(190)\n",
		ScriptBodyTruncated: true,
		ScriptOutputLines:   []string{"190"},
	}
	if err := SaveCmdLog(root, rec); err != nil {
		t.Fatalf("SaveCmdLog: %v", err)
	}
	metas, err := ListCmdLogs(root)
	if err != nil || len(metas) != 1 {
		t.Fatalf("ListCmdLogs = %d records, %v", len(metas), err)
	}
	m := metas[0]
	if m.Target != "stg" || m.Host != "stg.example" || m.RemoteDB != "stg_db" || m.RemoteStage != "staging" {
		t.Fatalf("remote fields not carried into meta: %+v", m)
	}
	if m.DB != "local_db" || m.Stage != "dev" {
		t.Fatalf("db/stage must stay the local profile's: %+v", m)
	}
	full, ok := LoadCmdLog(m.Path)
	if !ok {
		t.Fatal("LoadCmdLog failed")
	}
	if full.ScriptPath != rec.ScriptPath || full.ScriptSHA256 != "abc" || full.ScriptBody != rec.ScriptBody ||
		!full.ScriptBodyTruncated || len(full.ScriptOutputLines) != 1 || full.ScriptOutputLines[0] != "190" {
		t.Fatalf("script fields not persisted: %+v", full)
	}
}

func TestCmdLogOldRecordWithoutNewFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := "/some/project"
	dir, err := CmdLogsDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"cmd":"update sale","command":"update","db":"muutrade","stage":"dev","from":"",` +
		`"exit":0,"started":"2026-09-01T10:00:00Z","duration_ms":5,"errors":0,"warnings":0,` +
		`"truncated":false,"lines":[{"level":"INFO","text":"ok"}]}`
	if err := os.WriteFile(filepath.Join(dir, "1756720800000-update.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	metas, err := ListCmdLogs(root)
	if err != nil || len(metas) != 1 {
		t.Fatalf("ListCmdLogs = %d records, %v", len(metas), err)
	}
	if m := metas[0]; m.Command != "update" || m.Target != "" || m.Host != "" || m.RemoteDB != "" || m.RemoteStage != "" {
		t.Fatalf("old record loaded wrong: %+v", m)
	}

	saveAt(t, root, "install", time.Now())
	metas, _ = ListCmdLogs(root)
	data, err := os.ReadFile(metas[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"target", "host", "remote_db", "remote_stage", "script_path", "script_sha256",
		"script_body", "script_body_truncated", "script_output_lines"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("empty %s was written; it must be omitted", key)
		}
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

func liveHeader(started time.Time) CmdLogLiveHeader {
	return CmdLogLiveHeader{
		Cmd:     "update sale --from develop",
		Command: "update",
		DB:      "muutrade",
		Stage:   "dev",
		From:    "develop",
		Started: started,
		PID:     4242,
	}
}

func readLiveFile(t *testing.T, l *CmdLogLive) []string {
	t.Helper()
	data, err := os.ReadFile(l.path)
	if err != nil {
		t.Fatalf("read live file: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("live file does not end in a newline: %q", data)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestCmdLogLiveRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := "/proj/live"
	started := time.Date(2026, 10, 6, 18, 4, 5, 123_000_000, time.FixedZone("", -6*3600))

	l, err := OpenCmdLogLive(root, liveHeader(started))
	if err != nil {
		t.Fatalf("OpenCmdLogLive: %v", err)
	}
	if want := cmdLogStem(started, "update") + cmdLogLiveSuffix; filepath.Base(l.path) != want {
		t.Fatalf("live file name = %s, want %s", filepath.Base(l.path), want)
	}
	if err := l.Append(ReportLine{Level: "INFO", Text: "loading\nsale"}, nil); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := l.Append(ReportLine{Text: "plain"}, nil); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got := readLiveFile(t, l)
	want := []string{
		`{"schema":1,"cmd":"update sale --from develop","command":"update","db":"muutrade","stage":"dev","from":"develop","started":"2026-10-06T18:04:05.123-06:00","pid":4242}`,
		`{"level":"INFO","text":"loading\nsale"}`,
		`{"level":"","text":"plain"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("live file:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	l.Remove()
	if _, err := os.Stat(l.path); !os.IsNotExist(err) {
		t.Fatalf("live file still present after Remove: %v", err)
	}
	(*CmdLogLive)(nil).Remove()
}

func TestCmdLogLiveCompaction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := cmdLogLiveMaxBytes
	cmdLogLiveMaxBytes = 2000
	t.Cleanup(func() { cmdLogLiveMaxBytes = prev })

	l, err := OpenCmdLogLive("/proj/compact", liveHeader(time.Now()))
	if err != nil {
		t.Fatalf("OpenCmdLogLive: %v", err)
	}
	defer l.Remove()

	// keep returns the whole buffer, which outgrows the cap, as the repl's
	// lastOutput does on a long run.
	var buffered []ReportLine
	keep := func() []ReportLine { return buffered }
	appendLine := func(i int) (compacted bool) {
		t.Helper()
		line := ReportLine{Level: "INFO", Text: fmt.Sprintf("%03d %s", i, strings.Repeat("x", 80))}
		before := l.written
		if err := l.Append(line, keep); err != nil {
			t.Fatalf("Append: %v", err)
		}
		buffered = append(buffered, line)
		return l.written < before
	}

	i := 0
	for ; !appendLine(i); i++ {
		if i > 100 {
			t.Fatal("file never compacted")
		}
	}

	got := readLiveFile(t, l)
	if !strings.Contains(got[0], `"truncated":true`) || !strings.Contains(got[0], `"schema":1`) {
		t.Fatalf("compacted header = %s", got[0])
	}
	newLine := len(got[len(got)-1]) + 1
	if l.written > cmdLogLiveMaxBytes/2+newLine {
		t.Fatalf("written = %d after compaction, want at most %d", l.written, cmdLogLiveMaxBytes/2+newLine)
	}
	body := got[1:]
	if len(body) < 2 || len(body) >= len(buffered) {
		t.Fatalf("expected some but not all lines kept, got %d of %d", len(body), len(buffered))
	}
	newest := buffered[len(buffered)-len(body):]
	for j, raw := range body {
		var rl ReportLine
		if err := json.Unmarshal([]byte(raw), &rl); err != nil {
			t.Fatalf("line %d: %v", j+1, err)
		}
		if rl != newest[j] {
			t.Fatalf("line %d = %+v, want %+v (newest lines, in order)", j+1, rl, newest[j])
		}
	}

	if appendLine(i + 1) {
		t.Fatal("the append right after a compaction compacted again")
	}
}

func TestOpenCmdLogLiveLeavesNoTempFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	l, err := OpenCmdLogLive("/proj/tmp", liveHeader(time.Now()))
	if err != nil {
		t.Fatalf("OpenCmdLogLive: %v", err)
	}
	defer l.Remove()
	if _, err := os.Stat(l.path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary file left behind: %v", err)
	}
	if isOrphanLiveFile(l.path) {
		t.Fatal("renamed live file lost its lock")
	}
}

// liveFixture leaves, under root's cmd-logs dir, one record, one orphaned
// live file written by hand and one live file held open by this process.
func liveFixture(t *testing.T, root string) (orphan string, held *CmdLogLive) {
	t.Helper()
	saveAt(t, root, "update", time.Now())
	dir, err := CmdLogsDir(root)
	if err != nil {
		t.Fatal(err)
	}
	orphan = filepath.Join(dir, "1751847123456-install"+cmdLogLiveSuffix)
	if err := os.WriteFile(orphan, []byte(`{"schema":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	held, err = OpenCmdLogLive(root, liveHeader(time.Now().Add(time.Second)))
	if err != nil {
		t.Fatalf("OpenCmdLogLive: %v", err)
	}
	t.Cleanup(held.Remove)
	return orphan, held
}

func TestPruneCmdLogsRemovesOrphanLiveFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := "/proj/prune-live"
	orphan, held := liveFixture(t, root)

	removed, err := PruneCmdLogs(root, 7, 1)
	if err != nil {
		t.Fatalf("PruneCmdLogs: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected only the orphan removed, got %d", removed)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan still present: %v", err)
	}
	if _, err := os.Stat(held.path); err != nil {
		t.Fatalf("held live file was removed: %v", err)
	}
	if metas, _ := ListCmdLogs(root); len(metas) != 1 {
		t.Fatalf("expected the record to survive, got %d", len(metas))
	}
}

func TestClearCmdLogsRemovesOrphanLiveFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := "/proj/clear-live"
	orphan, held := liveFixture(t, root)

	removed, err := ClearCmdLogs(root)
	if err != nil {
		t.Fatalf("ClearCmdLogs: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected the record and the orphan removed, got %d", removed)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan still present: %v", err)
	}
	if _, err := os.Stat(held.path); err != nil {
		t.Fatalf("held live file was removed: %v", err)
	}
}

func TestListCmdLogsIgnoresLiveFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := "/proj/list-live"
	liveFixture(t, root)

	metas, err := ListCmdLogs(root)
	if err != nil {
		t.Fatalf("ListCmdLogs: %v", err)
	}
	if len(metas) != 1 || metas[0].Command != "update" {
		t.Fatalf("expected only the record, got %+v", metas)
	}
}
