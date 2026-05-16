package repl

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/pascualchavez/echo/internal/clipboard"
)

var errKinds = map[string]bool{"err": true, "warn": true}

// runCopyLast handles the `copy-last` command: copy the buffered
// output of the previous command. `--errors` filters to err/warn.
func (sess *session) runCopyLast(args []string) {
	onlyErrors := false
	for _, a := range args {
		switch a {
		case "--errors":
			onlyErrors = true
		default:
			sess.print(Line{Kind: "warn", Text: "unknown flag: " + a})
			return
		}
	}

	if sess.lastOutput.IsEmpty() {
		sess.print(Line{Kind: "warn", Text: "no output to copy — run a command first"})
		return
	}

	var payload string
	var label string
	var count int
	if onlyErrors {
		filtered := sess.lastOutput.Filtered(errKinds)
		if len(filtered) == 0 {
			sess.print(Line{Kind: "warn", Text: "no error lines in the last command"})
			return
		}
		payload = linesToPlain(filtered, sess.lastOutput.truncated)
		count = len(filtered)
		label = "error line"
	} else {
		payload = sess.lastOutput.Plain()
		count = sess.lastOutput.Len()
		label = "line"
	}

	if err := clipboard.WriteAll(payload); err != nil {
		sess.print(Line{Kind: "err", Text: "copy failed: " + err.Error()})
		return
	}

	plural := ""
	if count != 1 {
		plural = "s"
	}
	sess.print(Line{Kind: "ok", Text: fmt.Sprintf("copied %d %s%s to clipboard", count, label, plural)})
}

// copyFailureLog handles the auto-copy-on-failure path for module
// commands. It is called instead of finalize() when err != nil or
// errCount > 0 (cancellation is handled separately in runModules).
//
// The clipboard payload covers everything from the first err/warn line
// onwards — preceding warnings, the traceback, and shutdown/cleanup
// INFO lines emitted as Odoo unwinds. The final REPL line is a single
// Odoo-style log line (emitOdooLog) so success and failure share the
// exact same visual frame and slot in next to Odoo's own log stream.
func (sess *session) copyFailureLog(name string, resolved []string, summary string, runErr error, errCount, warnCount int) {
	sess.print(Line{Kind: "out", Text: ""})

	header := "✗ " + summary + " failed"
	failLines := sess.lastOutput.FromFirstError()
	var buf strings.Builder
	buf.WriteString(header)
	buf.WriteByte('\n')
	for _, l := range failLines {
		buf.WriteString(l.Text)
		buf.WriteByte('\n')
	}

	copyErr := clipboard.WriteAll(buf.String())
	copied := copyErr == nil

	var fields []logField
	if runErr != nil {
		fields = append(fields, logField{"err", runErr.Error()})
	}
	if errCount > 0 {
		fields = append(fields, logField{"errors", strconv.Itoa(errCount)})
	}
	if warnCount > 0 {
		fields = append(fields, logField{"warnings", strconv.Itoa(warnCount)})
	}
	fields = append(fields, logField{"copied", strconv.FormatBool(copied)})

	emitOdooLog("ERROR", echoCommandLogger(name, resolved), name+" failed",
		fields, sess.styles, sess.palette, sess.cfg.DBName)

	if !copied && errors.Is(copyErr, clipboard.ErrUnavailable) {
		sess.print(Line{Kind: "info", Text: copyErr.Error()})
	} else if !copied && copyErr != nil {
		sess.print(Line{Kind: "warn", Text: "copy failed: " + copyErr.Error()})
	}
}

// shellFailureLog auto-copies failures from interactive shell-style
// commands (bash, psql, shell). They bypass sess.print and write
// directly to the TTY, so we can't read from lastOutputBuffer; the
// `captured` string is the stderr we tee'd inside `ExecInteractive`.
func (sess *session) shellFailureLog(name, captured string, runErr error) {
	sess.print(Line{Kind: "out", Text: ""})

	header := "✗ " + name + " failed"
	payload := header + "\n" + captured

	copyErr := clipboard.WriteAll(payload)
	copied := copyErr == nil

	var fields []logField
	if runErr != nil {
		fields = append(fields, logField{"err", runErr.Error()})
	}
	fields = append(fields, logField{"copied", strconv.FormatBool(copied)})

	emitOdooLog("ERROR", echoCommandLogger(name, nil), name+" failed",
		fields, sess.styles, sess.palette, sess.cfg.DBName)

	if !copied && copyErr != nil {
		if errors.Is(copyErr, clipboard.ErrUnavailable) {
			sess.print(Line{Kind: "info", Text: copyErr.Error()})
		} else {
			sess.print(Line{Kind: "warn", Text: "copy failed: " + copyErr.Error()})
		}
	}
}

// commandFailureLog auto-copies failures from non-module commands that
// stream their output through sess.print (docker up/down/restart, i18n,
// db-*). The clipboard payload is everything from the first err/warn
// onwards, or the full buffer when no err/warn was logged.
func (sess *session) commandFailureLog(name string, runErr error, errCount, warnCount int) {
	sess.print(Line{Kind: "out", Text: ""})

	header := "✗ " + name + " failed"
	failLines := sess.lastOutput.FromFirstError()
	if len(failLines) == 0 {
		failLines = sess.lastOutput.Filtered(nil)
	}
	var buf strings.Builder
	buf.WriteString(header)
	buf.WriteByte('\n')
	for _, l := range failLines {
		buf.WriteString(l.Text)
		buf.WriteByte('\n')
	}

	copyErr := clipboard.WriteAll(buf.String())
	copied := copyErr == nil

	var fields []logField
	if runErr != nil {
		fields = append(fields, logField{"err", runErr.Error()})
	}
	if errCount > 0 {
		fields = append(fields, logField{"errors", strconv.Itoa(errCount)})
	}
	if warnCount > 0 {
		fields = append(fields, logField{"warnings", strconv.Itoa(warnCount)})
	}
	fields = append(fields, logField{"copied", strconv.FormatBool(copied)})

	emitOdooLog("ERROR", echoCommandLogger(name, nil), name+" failed",
		fields, sess.styles, sess.palette, sess.cfg.DBName)

	if !copied && copyErr != nil {
		if errors.Is(copyErr, clipboard.ErrUnavailable) {
			sess.print(Line{Kind: "info", Text: copyErr.Error()})
		} else {
			sess.print(Line{Kind: "warn", Text: "copy failed: " + copyErr.Error()})
		}
	}
}

// successLog emits the post-command ✓ entry for module commands as
// the Odoo-style INFO line, with the hierarchical logger naming the
// module(s) targeted (echo.update.module.<mod> / .modules / .all).
func (sess *session) successLog(name string, resolved []string, warnCount int) {
	sess.print(Line{Kind: "out", Text: ""})
	var fields []logField
	if warnCount > 0 {
		fields = append(fields, logField{"warnings", strconv.Itoa(warnCount)})
	}
	emitOdooLog("INFO", echoCommandLogger(name, resolved), name+" completed",
		fields, sess.styles, sess.palette, sess.cfg.DBName)
}

// moduleField returns the comma-joined module list to expose as a log
// field. Accepts the resolved-modules slice from RunInstall/Update/
// Uninstall, so it covers picker selections and the `--all` sentinel.
func moduleField(resolved []string) string {
	if len(resolved) == 0 {
		return "(none)"
	}
	if len(resolved) == 1 && resolved[0] == "--all" {
		return "all"
	}
	return strings.Join(resolved, ",")
}
