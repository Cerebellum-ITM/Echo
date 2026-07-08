package repl

import (
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
)

// cmdLogSkip lists command verbs that never produce a history record: the
// meta commands describe the REPL rather than a project action, `report`
// re-reads captured output (recording it would recurse), and `logview`
// (Unit 82) browses this very store. isMetaCommand already covers
// copy-last/help/clear; this set adds the read-only inspectors.
var cmdLogSkip = map[string]bool{
	"report":  true,
	"logview": true,
}

// captureReportLines tags each captured line with its log level: the exact
// token in the text first (keeps ERROR vs CRITICAL distinct), falling back
// to the line's classified Kind so Echo's own leveled lines and inherited
// traceback frames still carry a level. Shared by the recipe `report`
// capture and the command-log history sink.
func captureReportLines(lines []Line) []config.ReportLine {
	out := make([]config.ReportLine, 0, len(lines))
	for _, l := range lines {
		lvl := lineLevel(l.Text)
		if lvl == "" {
			lvl = levelFromKind(l.Kind)
		}
		out = append(out, config.ReportLine{Level: lvl, Text: l.Text})
	}
	return out
}

// saveCmdLog snapshots the just-finished command's captured output as a
// history record under ~/.config/echo/cmd-logs/<key>/. Best-effort: any
// failure is swallowed so it never breaks or delays the command. Skipped:
// disabled config, meta commands, the read-only inspectors, and empty
// captures (e.g. unknown-command).
func (sess *session) saveCmdLog(cmd string, args []string, dur time.Duration) {
	if sess.cfg == nil || sess.cfg.CmdLogsDisabled {
		return
	}
	if isMetaCommand(cmd) || cmdLogSkip[cmd] {
		return
	}
	if sess.lastOutput == nil || sess.lastOutput.IsEmpty() {
		return
	}

	startedAt := time.Now().Add(-dur)
	rec := config.CmdLogRecord{
		Cmd:        strings.TrimSpace(cmd + " " + strings.Join(args, " ")),
		Command:    cmd,
		DB:         sess.cfg.DBName,
		Stage:      string(sess.stage),
		From:       remoteRunLabel(args),
		Exit:       sess.exitCode,
		Started:    startedAt,
		DurationMS: dur.Milliseconds(),
		Errors:     sess.lastErrors,
		Warnings:   sess.lastWarnings,
		Truncated:  sess.lastOutput.truncated,
		Lines:      captureReportLines(sess.lastOutput.Filtered(nil)),
	}

	root := sess.projectDir
	_ = config.SaveCmdLog(root, rec)
	_, _ = config.PruneCmdLogs(root, sess.cfg.CmdLogsRetentionDays, sess.cfg.CmdLogsMaxRuns)
}

// pruneCmdLogs fires one best-effort retention pass, called once at session
// entry (REPL Start / one-shot / recipe). Disabled config is a no-op.
func (sess *session) pruneCmdLogs() {
	if sess.cfg == nil || sess.cfg.CmdLogsDisabled {
		return
	}
	_, _ = config.PruneCmdLogs(sess.projectDir, sess.cfg.CmdLogsRetentionDays, sess.cfg.CmdLogsMaxRuns)
}

// remoteRunLabel returns the record's `from` label for a command's args:
// the named `--from`/`--from=` target, or the literal "remote" for a bare
// `--remote`, or "" for a local run. This only tags the record — remote
// output already flows through the same buffer as local output.
func remoteRunLabel(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(a, "--from="):
			return strings.TrimPrefix(a, "--from=")
		case a == "--remote":
			return "remote"
		}
	}
	return ""
}
