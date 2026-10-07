package repl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

func TestCaptureReportLines(t *testing.T) {
	cases := []struct {
		name string
		in   Line
		want string
	}{
		{"text token wins over kind", Line{Kind: "warn", Text: "Critical: meltdown"}, "CRITICAL"},
		{"error text token", Line{Kind: "out", Text: "Error: boom"}, "ERROR"},
		{"kind fallback warn", Line{Kind: "warn", Text: "no token here"}, "WARNING"},
		{"kind fallback err", Line{Kind: "err", Text: "traceback frame"}, "ERROR"},
		{"kind fallback debug", Line{Kind: "faint", Text: "verbose"}, "DEBUG"},
		{"kind fallback info", Line{Kind: "info", Text: "starting"}, "INFO"},
		{"no level at all", Line{Kind: "out", Text: "plain output"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := captureReportLines([]Line{tc.in})
			if len(got) != 1 {
				t.Fatalf("expected 1 line, got %d", len(got))
			}
			if got[0].Level != tc.want {
				t.Fatalf("level = %q, want %q (text=%q kind=%q)",
					got[0].Level, tc.want, tc.in.Text, tc.in.Kind)
			}
			if got[0].Text != tc.in.Text {
				t.Fatalf("text mangled: %q", got[0].Text)
			}
		})
	}
}

func TestRemoteRunLabel(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"sale"}, ""},
		{[]string{"sale", "--from", "prod"}, "prod"},
		{[]string{"sale", "--from=staging"}, "staging"},
		{[]string{"sale", "--remote"}, "remote"},
		{[]string{"--from"}, ""}, // dangling flag, no value
	}
	for _, tc := range cases {
		if got := remoteRunLabel(tc.args); got != tc.want {
			t.Fatalf("remoteRunLabel(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// cmdLogSession builds a minimal session wired for saveCmdLog against a
// temp HOME.
func cmdLogSession(t *testing.T) *session {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p := theme.PaletteByName("")
	return &session{
		palette:    p,
		styles:     theme.New(p, theme.StageDev),
		stage:      theme.StageDev,
		projectDir: "/proj/cmdlog",
		lastOutput: newLastOutputBuffer(),
		cfg: &config.Config{
			DBName:               "muutrade",
			CmdLogsRetentionDays: 7,
			CmdLogsMaxRuns:       500,
		},
	}
}

func TestSaveCmdLogWritesRecord(t *testing.T) {
	sess := cmdLogSession(t)
	sess.lastOutput.Add(Line{Kind: "info", Text: "INFO doing work"})

	started := time.Now().Add(-1200 * time.Millisecond)
	sess.saveCmdLog("update", []string{"sale"}, started)

	metas, err := config.ListCmdLogs(sess.projectDir)
	if err != nil {
		t.Fatalf("ListCmdLogs: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 record, got %d", len(metas))
	}
	if metas[0].Command != "update" || metas[0].Cmd != "update sale" {
		t.Fatalf("unexpected metadata: %+v", metas[0])
	}
	if metas[0].DurationMS < 1200 || metas[0].DurationMS > 1500 {
		t.Fatalf("duration = %d, want about 1200", metas[0].DurationMS)
	}
	if !metas[0].Started.Equal(started.Truncate(0)) {
		t.Fatalf("started = %v, want the dispatch start %v", metas[0].Started, started)
	}
}

func TestSaveCmdLogSkips(t *testing.T) {
	check := func(name, cmd string, setup func(s *session)) {
		t.Run(name, func(t *testing.T) {
			sess := cmdLogSession(t)
			if setup != nil {
				setup(sess)
			}
			sess.saveCmdLog(cmd, nil, time.Now().Add(-time.Second))
			metas, _ := config.ListCmdLogs(sess.projectDir)
			if len(metas) != 0 {
				t.Fatalf("expected no record for %q, got %d", cmd, len(metas))
			}
		})
	}

	withOutput := func(s *session) { s.lastOutput.Add(Line{Kind: "info", Text: "x"}) }

	check("meta command help", "help", withOutput)
	check("meta command copy-last", "copy-last", withOutput)
	check("report inspector", "report", withOutput)
	check("logview inspector", "logview", withOutput)
	check("empty buffer", "update", nil) // no lines added
	check("disabled config", "update", func(s *session) {
		withOutput(s)
		s.cfg.CmdLogsDisabled = true
	})
}

// liveFiles returns the session's live cmd-log files and, for each, its
// lines.
func liveFiles(t *testing.T, sess *session) map[string][]string {
	t.Helper()
	dir, err := config.CmdLogsDir(sess.projectDir)
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.running.ndjson"))
	out := make(map[string][]string, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	return out
}

func TestDispatchLeavesRecordAndNoLiveFile(t *testing.T) {
	sess := cmdLogSession(t)
	sess.dispatchParsed(context.Background(), "frobnicate", nil)

	if len(sess.liveRuns) != 0 {
		t.Fatalf("live stack not unwound: %d", len(sess.liveRuns))
	}
	if live := liveFiles(t, sess); len(live) != 0 {
		t.Fatalf("live file left behind: %v", live)
	}
	if metas, _ := config.ListCmdLogs(sess.projectDir); len(metas) != 1 || metas[0].Command != "frobnicate" {
		t.Fatalf("expected one frobnicate record, got %+v", metas)
	}
}

func TestLiveRunStreamsCapturedLines(t *testing.T) {
	sess := cmdLogSession(t)
	started := time.Now()
	if !sess.pushLiveRun("update", []string{"sale"}, started) {
		t.Fatal("pushLiveRun refused a recordable command")
	}
	sess.capture(Line{Kind: "info", Text: "INFO loading sale"})
	sess.capture(Line{Kind: "out", Text: "plain output"})

	live := liveFiles(t, sess)
	if len(live) != 1 {
		t.Fatalf("expected one live file, got %v", live)
	}
	name := liveFileFor(t, live, "update")
	lines := live[name]
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 lines, got %q", lines)
	}
	if !strings.Contains(lines[0], `"cmd":"update sale"`) || !strings.Contains(lines[0], `"db":"muutrade"`) {
		t.Fatalf("header = %s", lines[0])
	}
	if lines[1] != `{"level":"INFO","text":"INFO loading sale"}` || lines[2] != `{"level":"","text":"plain output"}` {
		t.Fatalf("captured lines = %q", lines[1:])
	}

	sess.saveCmdLog("update", []string{"sale"}, started)
	sess.popLiveRun()

	if live := liveFiles(t, sess); len(live) != 0 {
		t.Fatalf("live file left behind: %v", live)
	}
	metas, _ := config.ListCmdLogs(sess.projectDir)
	if len(metas) != 1 {
		t.Fatalf("expected one record, got %d", len(metas))
	}
	if got, want := filepath.Base(metas[0].Path), strings.TrimSuffix(name, ".running.ndjson")+".json"; got != want {
		t.Fatalf("record %s does not share the live file's stem (%s)", got, want)
	}
}

func TestLiveRunSkipsUnrecordable(t *testing.T) {
	for _, cmd := range []string{"help", "copy-last", "report", "logview"} {
		sess := cmdLogSession(t)
		if sess.pushLiveRun(cmd, nil, time.Now()) {
			t.Fatalf("pushLiveRun opened a live file for %q", cmd)
		}
		if live := liveFiles(t, sess); len(live) != 0 {
			t.Fatalf("live file for %q: %v", cmd, live)
		}
	}
	sess := cmdLogSession(t)
	sess.cfg.CmdLogsDisabled = true
	if sess.pushLiveRun("update", nil, time.Now()) {
		t.Fatal("pushLiveRun opened a live file with cmd logs disabled")
	}
}

func TestLiveRunNestedGoesToInnermost(t *testing.T) {
	sess := cmdLogSession(t)
	now := time.Now()
	sess.pushLiveRun("sequence", nil, now)
	sess.capture(Line{Kind: "out", Text: "outer 1"})
	sess.pushLiveRun("update", []string{"sale"}, now.Add(time.Millisecond))
	sess.capture(Line{Kind: "out", Text: "inner 1"})

	live := liveFiles(t, sess)
	inner := live[liveFileFor(t, live, "update")]
	if len(inner) != 2 || !strings.Contains(inner[1], "inner 1") {
		t.Fatalf("inner live file = %q", inner)
	}

	sess.popLiveRun()
	sess.capture(Line{Kind: "out", Text: "outer 2"})

	live = liveFiles(t, sess)
	if len(live) != 1 {
		t.Fatalf("expected only the outer live file, got %v", live)
	}
	outer := live[liveFileFor(t, live, "sequence")]
	if len(outer) != 3 || !strings.Contains(outer[1], "outer 1") || !strings.Contains(outer[2], "outer 2") {
		t.Fatalf("outer live file = %q", outer)
	}
	sess.popLiveRun()
}

func TestLiveRunDroppedSlotIsSkipped(t *testing.T) {
	sess := cmdLogSession(t)
	sess.liveRuns = []*config.CmdLogLive{nil}
	sess.capture(Line{Kind: "out", Text: "still buffered"})
	sess.popLiveRun()
	if len(sess.liveRuns) != 0 || sess.lastOutput.Len() != 1 {
		t.Fatalf("stack = %d, buffered = %d", len(sess.liveRuns), sess.lastOutput.Len())
	}
}

// filepathStem returns the one live file name in live for command.
func liveFileFor(t *testing.T, live map[string][]string, command string) string {
	t.Helper()
	for name := range live {
		if strings.HasSuffix(name, "-"+command+".running.ndjson") {
			return name
		}
	}
	t.Fatalf("no live file for %q in %v", command, live)
	return ""
}

// wireCapture points the package hooks newSession sets at sess, restoring
// them when the test ends.
func wireCapture(t *testing.T, sess *session) {
	t.Helper()
	prevCapture, prevRemote := captureLine, cmd.OnRemoteResolved
	captureLine = sess.capture
	cmd.OnRemoteResolved = sess.noteRemoteResolved
	t.Cleanup(func() { captureLine, cmd.OnRemoteResolved = prevCapture, prevRemote })
}

func TestEmitOdooLogIsCaptured(t *testing.T) {
	sess := cmdLogSession(t)
	wireCapture(t, sess)
	started := time.Now()
	if !sess.pushLiveRun("modules", nil, started) {
		t.Fatal("pushLiveRun refused a recordable command")
	}
	defer sess.popLiveRun()

	emitOdooLogTo(io.Discard, "WARNING", "echo.modules", "nothing to list",
		[]logField{{"hint", "run init"}}, sess.styles, sess.palette, "muutrade")
	suppressLevel = silentAll
	emitOdooLogTo(io.Discard, "INFO", "echo.modules", "silenced", nil, sess.styles, sess.palette, "muutrade")
	suppressLevel = -1

	lines := sess.lastOutput.Filtered(nil)
	if len(lines) != 2 {
		t.Fatalf("captured %d lines, want 2: %v", len(lines), lines)
	}
	if lines[0].Kind != "warn" || !strings.Contains(lines[0].Text, " WARN muutrade echo.modules: nothing to list hint=") {
		t.Fatalf("first line = %+v", lines[0])
	}
	if lines[1].Kind != "info" || !strings.HasSuffix(lines[1].Text, "echo.modules: silenced") {
		t.Fatalf("suppressed line not captured: %+v", lines[1])
	}
	if got := captureReportLines(lines); got[0].Level != "WARNING" || got[1].Level != "INFO" {
		t.Fatalf("levels = %q, %q", got[0].Level, got[1].Level)
	}

	live := liveFiles(t, sess)[liveFileFor(t, liveFiles(t, sess), "modules")]
	if len(live) != 3 || !strings.HasPrefix(live[1], `{"level":"WARNING","text":`) || !strings.Contains(live[2], "silenced") {
		t.Fatalf("live file = %q", live)
	}
}

func TestNoteRemoteResolvedFirstWins(t *testing.T) {
	sess := cmdLogSession(t)
	sess.noteRemoteResolved(cmd.RemoteResolution{Target: "a", Host: "a.host", DB: "a_db", Stage: "dev"})
	sess.noteRemoteResolved(cmd.RemoteResolution{Target: "b", Host: "b.host", DB: "b_db", Stage: "prod"})
	sess.lastOutput.Add(Line{Kind: "out", Text: "compared"})
	sess.saveCmdLog("compare", []string{"--targets", "a,b"}, time.Now())

	metas, _ := config.ListCmdLogs(sess.projectDir)
	if len(metas) != 1 {
		t.Fatalf("expected one record, got %d", len(metas))
	}
	m := metas[0]
	if m.Target != "a" || m.Host != "a.host" || m.RemoteDB != "a_db" || m.RemoteStage != "dev" {
		t.Fatalf("record = %+v, want target a", m)
	}
	if m.DB != "muutrade" || m.Stage != "dev" {
		t.Fatalf("db/stage must stay the local profile's: %+v", m)
	}
}

// fakeSSH puts an ssh first on PATH that runs every "remote" command locally
// except compose calls, which swallow stdin and print the script's result.
func fakeSSH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
while [ "$1" = "-o" ]; do shift 2; done
shift
case "$*" in
  *compose*) cat >/dev/null; printf '190\n' ;;
  *) exec sh -c "$*" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// remoteShellRunSession is a session with one connect target, stg, served by
// fakeSSH from a temp directory whose Echo profile is a staging one.
func remoteShellRunSession(t *testing.T) *session {
	t.Helper()
	sess := cmdLogSession(t)
	fakeSSH(t)
	srv, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(os.Getenv("HOME"), ".config/echo/projects", config.ProjectKey(srv)+".toml")
	if err := os.MkdirAll(filepath.Dir(profile), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "stage = \"staging\"\ndb_name = \"stg_db\"\nodoo_version = \"18\"\nodoo_container = \"odoo\"\n"
	if err := os.WriteFile(profile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sess.cfg.ConnectTargets = []config.ConnectTarget{{Name: "stg", SSHHost: "fakehost", RemotePath: srv}}
	wireCapture(t, sess)
	return sess
}

func TestShellRunRecordCarriesRemoteAndScript(t *testing.T) {
	sess := remoteShellRunSession(t)
	scriptPath := filepath.Join(t.TempDir(), "count.py")
	script := "print(env['res.partner'].search_count([]))\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	sess.dispatchParsed(context.Background(), "shell-run", []string{scriptPath, "--from", "stg", "--no-copy"})
	if sess.exitCode != exitOK {
		t.Fatalf("exit = %d, lines = %v", sess.exitCode, sess.lastOutput.Filtered(nil))
	}

	metas, _ := config.ListCmdLogs(sess.projectDir)
	if len(metas) != 1 {
		t.Fatalf("expected one record, got %d", len(metas))
	}
	m := metas[0]
	if m.Target != "stg" || m.Host != "fakehost" || m.RemoteDB != "stg_db" || m.RemoteStage != "staging" {
		t.Fatalf("remote fields = %+v", m)
	}
	rec, _ := config.LoadCmdLog(m.Path)
	sum := sha256.Sum256([]byte(script))
	if rec.ScriptPath != scriptPath || rec.ScriptBody != script || rec.ScriptSHA256 != hex.EncodeToString(sum[:]) || rec.ScriptBodyTruncated {
		t.Fatalf("script fields = path %q body %q sha %q truncated %v", rec.ScriptPath, rec.ScriptBody, rec.ScriptSHA256, rec.ScriptBodyTruncated)
	}
	if len(rec.ScriptOutputLines) != 1 || rec.ScriptOutputLines[0] != "190" {
		t.Fatalf("script output lines = %q", rec.ScriptOutputLines)
	}
	var echoLevels []string
	for _, l := range rec.Lines {
		if strings.Contains(l.Text, " echo.") {
			echoLevels = append(echoLevels, l.Level)
		}
	}
	if len(echoLevels) == 0 || slices.Contains(echoLevels, "") {
		t.Fatalf("Echo lines missing or without a full level: %v in %+v", echoLevels, rec.Lines)
	}
}

func TestNestedDispatchExtrasDoNotLeak(t *testing.T) {
	sess := remoteShellRunSession(t)
	scriptPath := filepath.Join(t.TempDir(), "noop.py")
	if err := os.WriteFile(scriptPath, []byte("pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outer := cmd.RemoteResolution{Target: "outer", Host: "outer.host", DB: "outer_db", Stage: "dev"}
	sess.noteRemoteResolved(outer)

	sess.dispatchParsed(context.Background(), "shell-run", []string{scriptPath, "--from", "stg", "--no-copy"})

	if sess.extras.remote == nil || *sess.extras.remote != outer {
		t.Fatalf("outer resolution after the nested dispatch = %+v, want %+v", sess.extras.remote, outer)
	}
	if sess.extras.script != nil {
		t.Fatal("the nested step's script leaked into the outer extras")
	}
	metas, _ := config.ListCmdLogs(sess.projectDir)
	if len(metas) != 1 || metas[0].Target != "stg" {
		t.Fatalf("nested record = %+v, want target stg", metas)
	}

	sess.dispatchParsed(context.Background(), "frobnicate", nil)
	metas, _ = config.ListCmdLogs(sess.projectDir)
	if len(metas) != 2 || metas[0].Command != "frobnicate" || metas[0].Target != "" {
		t.Fatalf("a dispatch with no resolution got one: %+v", metas[0])
	}
}
