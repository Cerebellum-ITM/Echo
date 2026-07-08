package repl

import (
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

	sess.saveCmdLog("update", []string{"sale"}, 1200*time.Millisecond)

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
	if metas[0].DurationMS != 1200 {
		t.Fatalf("duration = %d, want 1200", metas[0].DurationMS)
	}
}

func TestSaveCmdLogSkips(t *testing.T) {
	check := func(name, cmd string, setup func(s *session)) {
		t.Run(name, func(t *testing.T) {
			sess := cmdLogSession(t)
			if setup != nil {
				setup(sess)
			}
			sess.saveCmdLog(cmd, nil, time.Second)
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
