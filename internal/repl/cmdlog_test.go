package repl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
