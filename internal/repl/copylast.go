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
func (sess *session) copyFailureLog(name string, resolved []string, runErr error, errCount, warnCount int) {
	sess.exitCode = scriptExitCode(runErr, errCount)
	sess.lastErrors, sess.lastWarnings = errCount, warnCount
	sess.print(Line{Kind: "out", Text: ""})

	logger := failureLogger(name, resolved)
	header := plainOdooLog("ERROR", logger, name+" failed", sess.cfg.DBName)
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

	emitOdooLog("ERROR", logger, name+" failed",
		fields, sess.styles, sess.palette, sess.cfg.DBName)

	if !copied && errors.Is(copyErr, clipboard.ErrUnavailable) {
		sess.print(Line{Kind: "info", Text: copyErr.Error()})
	} else if !copied && copyErr != nil {
		sess.print(Line{Kind: "warn", Text: "copy failed: " + copyErr.Error()})
	}
}

// connectFailureLog auto-copies failures from `connect`. The error
// returned by cmd.RunConnect (wrapping the Python script's stderr) is
// the only useful payload — the REPL stream above it only contains the
// start INFO and the picker view, neither of which helps debug. So we
// copy `<header>\nerr: <message>` and let the user paste it as-is.
func (sess *session) connectFailureLog(runErr error) {
	sess.exitCode = scriptExitCode(runErr, 0)
	sess.print(Line{Kind: "out", Text: ""})

	logger := failureLogger("connect", nil)
	header := plainOdooLog("ERROR", logger, "connect failed", sess.cfg.DBName)
	payload := header + "\nerr: " + runErr.Error() + "\n"

	copyErr := clipboard.WriteAll(payload)
	copied := copyErr == nil

	fields := []logField{
		{"err", runErr.Error()},
		{"copied", strconv.FormatBool(copied)},
	}
	emitOdooLog("ERROR", logger, "connect failed",
		fields, sess.styles, sess.palette, sess.cfg.DBName)

	if !copied && copyErr != nil {
		if errors.Is(copyErr, clipboard.ErrUnavailable) {
			sess.print(Line{Kind: "info", Text: copyErr.Error()})
		} else {
			sess.print(Line{Kind: "warn", Text: "copy failed: " + copyErr.Error()})
		}
	}
}

// shellFailureLog auto-copies failures from interactive shell-style
// commands (bash, psql, shell). They bypass sess.print and write
// directly to the TTY, so we can't read from lastOutputBuffer; the
// `captured` string is the stderr we tee'd inside `ExecInteractive`.
func (sess *session) shellFailureLog(name, captured string, runErr error) {
	sess.exitCode = scriptExitCode(runErr, 0)
	sess.print(Line{Kind: "out", Text: ""})

	logger := failureLogger(name, nil)
	header := plainOdooLog("ERROR", logger, name+" failed", sess.cfg.DBName)
	payload := header + "\n" + captured

	copyErr := clipboard.WriteAll(payload)
	copied := copyErr == nil

	var fields []logField
	if runErr != nil {
		fields = append(fields, logField{"err", runErr.Error()})
	}
	fields = append(fields, logField{"copied", strconv.FormatBool(copied)})

	emitOdooLog("ERROR", logger, name+" failed",
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
	sess.exitCode = scriptExitCode(runErr, errCount)
	sess.lastErrors, sess.lastWarnings = errCount, warnCount
	sess.print(Line{Kind: "out", Text: ""})

	logger := failureLogger(name, nil)
	header := plainOdooLog("ERROR", logger, name+" failed", sess.cfg.DBName)
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

	emitOdooLog("ERROR", logger, name+" failed",
		fields, sess.styles, sess.palette, sess.cfg.DBName)

	if !copied && copyErr != nil {
		if errors.Is(copyErr, clipboard.ErrUnavailable) {
			sess.print(Line{Kind: "info", Text: copyErr.Error()})
		} else {
			sess.print(Line{Kind: "warn", Text: "copy failed: " + copyErr.Error()})
		}
	}
}

// shellExitLog emits the Odoo-style INFO line printed when an
// interactive shell command returns cleanly. Counterpart to
// startLog / shellFailureLog so a shell session reads as a normal
// start → exit pair in the log stream.
func (sess *session) shellExitLog(name string) {
	sess.exitCode = exitOK
	sess.print(Line{Kind: "out", Text: ""})
	emitOdooLog("INFO", echoCommandLogger(name, nil), name+" exited",
		nil, sess.styles, sess.palette, sess.cfg.DBName)
}

// readonlyFinalize emits the Odoo-style end-log line for read-only
// commands (`ps`, `logs`, `modules`, `db-list`) whose output is a
// listing the user reads directly. Mirrors the success/failure pair
// emitted around shell sessions, but never auto-copies — these
// commands do not change state, so a failure does not produce a
// payload worth pasting.
func (sess *session) readonlyFinalize(name string, runErr error) {
	sess.exitCode = scriptExitCode(runErr, 0)
	sess.print(Line{Kind: "out", Text: ""})
	if runErr != nil {
		emitOdooLog("ERROR", failureLogger(name, nil), name+" failed",
			[]logField{{"err", runErr.Error()}},
			sess.styles, sess.palette, sess.cfg.DBName)
		return
	}
	emitOdooLog("INFO", echoCommandLogger(name, nil), name+" completed",
		nil, sess.styles, sess.palette, sess.cfg.DBName)
}

// shellCancelledLog emits a WARN-level Odoo-style line when the user
// hit Ctrl+C during an interactive shell. Distinct from
// shellFailureLog because the subprocess error (Odoo catches SIGINT
// and exits with code 1 plus a KeyboardInterrupt traceback) is the
// expected outcome of cancellation, not a real failure — we don't
// want to auto-copy a traceback the user already saw and intended to
// trigger.
func (sess *session) shellCancelledLog(name string) {
	sess.exitCode = exitCancelled
	sess.print(Line{Kind: "out", Text: ""})
	logger := echoCommandLogger(name, nil) + ".cancelled"
	emitOdooLog("WARNING", logger, name+" interrupted by user",
		nil, sess.styles, sess.palette, sess.cfg.DBName)
}

// startLog emits the Odoo-style INFO line printed when a command
// begins executing. Replaces the legacy `$ <name>` prompt-echo with
// a structured event whose logger sits under `echo.<cmd>.start`. For
// module commands the path embeds the resolved targets; for other
// commands the positional args ride as a structured field.
func (sess *session) startLog(name string, args []string) {
	logger := startLogger(name)
	var fields []logField
	if !isModuleCommand(name) {
		var positional []string
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			positional = append(positional, a)
		}
		if len(positional) > 0 {
			fields = append(fields, logField{"args", strings.Join(positional, " ")})
		}
	}
	emitOdooLog("INFO", logger, name, fields,
		sess.styles, sess.palette, sess.cfg.DBName)
}

// startResolved emits the start line for a module command once its final
// module set is known (after picker / --last resolution), so the line
// names the actual modules — closing the gap where `update --last` or a
// picker selection produced a generic `echo.update.start`. The logger
// encodes the target (echo.<cmd>.module.<mod> / .modules / .all) and the
// modules= field always spells out the full set.
func (sess *session) startResolved(name string, resolved []string) {
	logger := echoCommandLogger(name, resolved) + ".start"
	emitOdooLog("INFO", logger, name,
		[]logField{{"modules", moduleField(resolved)}},
		sess.styles, sess.palette, sess.cfg.DBName)
}

// successLog emits the post-command ✓ entry for module commands as
// the Odoo-style INFO line, with the hierarchical logger naming the
// module(s) targeted (echo.update.module.<mod> / .modules / .all).
func (sess *session) successLog(name string, resolved []string, warnCount int) {
	sess.exitCode = exitOK
	sess.lastWarnings = warnCount
	sess.print(Line{Kind: "out", Text: ""})
	var fields []logField
	if warnCount > 0 {
		fields = append(fields, logField{"warnings", strconv.Itoa(warnCount)})
	}
	emitOdooLog("INFO", echoCommandLogger(name, resolved), name+" completed",
		fields, sess.styles, sess.palette, sess.cfg.DBName)
}

// emitMigrations prints one Odoo-style summary line per module migration
// detected in the command's output, after the success/failure recap so it
// closes the run. The logger encodes the command and `.migration`, and the
// fields name the module, the version it migrated to, and which phases ran
// (pre/post/end) — mirroring Odoo's own `odoo.modules.migration` semantics.
func (sess *session) emitMigrations(name string, resolved []string, migs []migration) {
	if len(migs) == 0 {
		return
	}
	logger := echoCommandLogger(name, resolved) + ".migration"
	for _, mg := range migs {
		fields := []logField{
			{"module", mg.module},
			{"version", mg.version},
		}
		if len(mg.phases) > 0 {
			fields = append(fields, logField{"phases", strings.Join(mg.phases, ",")})
		}
		emitOdooLog("INFO", logger, "migration detected", fields,
			sess.styles, sess.palette, sess.cfg.DBName)
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
