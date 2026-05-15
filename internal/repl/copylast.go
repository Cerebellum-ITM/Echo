package repl

import (
	"errors"
	"fmt"
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
// onwards — that includes preceding warnings that contextualise the
// crash, the traceback, and shutdown/cleanup INFO lines emitted while
// Odoo tears down. The final REPL line is a single charmbracelet/log
// Error styled with the active palette.
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

	fields := []any{"module", moduleField(resolved)}
	if runErr != nil {
		fields = append(fields, "err", runErr.Error())
	}
	if errCount > 0 {
		fields = append(fields, "errors", errCount)
	}
	if warnCount > 0 {
		fields = append(fields, "warnings", warnCount)
	}
	fields = append(fields, "copied", copied)

	sess.log.Error(name+" failed", fields...)

	if !copied && errors.Is(copyErr, clipboard.ErrUnavailable) {
		sess.print(Line{Kind: "info", Text: copyErr.Error()})
	} else if !copied && copyErr != nil {
		sess.print(Line{Kind: "warn", Text: "copy failed: " + copyErr.Error()})
	}
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
