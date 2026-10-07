package repl

import (
	"os"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/cmd"
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
		out = append(out, reportLine(l))
	}
	return out
}

func reportLine(l Line) config.ReportLine {
	lvl := lineLevel(l.Text)
	if lvl == "" {
		lvl = levelFromKind(l.Kind)
	}
	return config.ReportLine{Level: lvl, Text: l.Text}
}

// recordExtras is what a dispatch adds to its record beyond the captured
// lines: the remote target it resolved and the script it ran. Nil means the
// run had none.
type recordExtras struct {
	remote *cmd.RemoteResolution
	script *scriptFields
}

// noteRemoteResolved is cmd.OnRemoteResolved for this session. The first
// resolution of a dispatch wins, so `compare --targets a,b` records a.
func (sess *session) noteRemoteResolved(r cmd.RemoteResolution) {
	if sess.extras.remote == nil {
		sess.extras.remote = &r
	}
}

// isRecordable reports whether a dispatch of cmd gets a history record and
// a live file: not when disabled, not for meta commands or the read-only
// inspectors.
func (sess *session) isRecordable(cmd string) bool {
	return sess.cfg != nil && !sess.cfg.CmdLogsDisabled && !isMetaCommand(cmd) && !cmdLogSkip[cmd]
}

// saveCmdLog snapshots the just-finished command's captured output as a
// history record under ~/.config/echo/cmd-logs/<key>/. Best-effort: any
// failure is swallowed so it never breaks or delays the command. Skipped:
// commands that are not recordable and empty captures (e.g.
// unknown-command).
func (sess *session) saveCmdLog(cmd string, args []string, started time.Time) {
	if !sess.isRecordable(cmd) {
		return
	}
	if sess.lastOutput == nil || sess.lastOutput.IsEmpty() {
		return
	}

	dur := time.Since(started)
	rec := config.CmdLogRecord{
		Cmd:        strings.TrimSpace(cmd + " " + strings.Join(args, " ")),
		Command:    cmd,
		DB:         sess.cfg.DBName,
		Stage:      string(sess.stage),
		From:       remoteRunLabel(args),
		Exit:       sess.exitCode,
		Started:    started,
		DurationMS: dur.Milliseconds(),
		Errors:     sess.lastErrors,
		Warnings:   sess.lastWarnings,
		Truncated:  sess.lastOutput.truncated,
		Lines:      captureReportLines(sess.lastOutput.Filtered(nil)),
	}
	if r := sess.extras.remote; r != nil {
		rec.Target, rec.Host, rec.RemoteDB, rec.RemoteStage = r.Target, r.Host, r.DB, r.Stage
	}
	if sc := sess.extras.script; sc != nil {
		rec.ScriptPath = sc.path
		rec.ScriptSHA256 = sc.sha256
		rec.ScriptBody = sc.body
		rec.ScriptBodyTruncated = sc.bodyTruncated
		rec.ScriptOutputLines = sc.outputLines
	}

	root := sess.projectDir
	_ = config.SaveCmdLog(root, rec)
	_, _ = config.PruneCmdLogs(root, sess.cfg.CmdLogsRetentionDays, sess.cfg.CmdLogsMaxRuns)
}

// pushLiveRun opens the live file a recordable dispatch streams its lines
// to and pushes it on the session's stack. It reports whether it pushed;
// an open failure only means this run has no live file.
func (sess *session) pushLiveRun(cmd string, args []string, started time.Time) bool {
	if !sess.isRecordable(cmd) {
		return false
	}
	live, err := config.OpenCmdLogLive(sess.projectDir, config.CmdLogLiveHeader{
		Cmd:     strings.TrimSpace(cmd + " " + strings.Join(args, " ")),
		Command: cmd,
		DB:      sess.cfg.DBName,
		Stage:   string(sess.stage),
		From:    remoteRunLabel(args),
		Started: started,
		PID:     os.Getpid(),
	})
	if err != nil {
		return false
	}
	sess.liveRuns = append(sess.liveRuns, live)
	return true
}

// popLiveRun closes and deletes the innermost live file. Nested dispatches
// return in LIFO order, so the top is the caller's own.
func (sess *session) popLiveRun() {
	n := len(sess.liveRuns)
	sess.liveRuns[n-1].Remove()
	sess.liveRuns = sess.liveRuns[:n-1]
}

// capture records a printed line: in the live file of the innermost run
// and in lastOutput. The live append goes first so a compaction rebuilds
// from the buffer as it was before this line, then adds the line once.
// A failed append drops that run's live file; its slot stays as nil so the
// stack still pops in order.
func (sess *session) capture(l Line) {
	if n := len(sess.liveRuns); n > 0 && sess.liveRuns[n-1] != nil {
		live := sess.liveRuns[n-1]
		keep := func() []config.ReportLine { return captureReportLines(sess.lastOutput.Filtered(nil)) }
		if err := live.Append(reportLine(l), keep); err != nil {
			live.Remove()
			sess.liveRuns[n-1] = nil
		}
	}
	if sess.lastOutput != nil {
		sess.lastOutput.Add(l)
	}
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
