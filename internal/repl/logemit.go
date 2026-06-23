package repl

import (
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

// runLogSink, when non-nil, receives a plain-text (ANSI-free) copy of
// every line emitted by sess.print and emitOdooLog. It is set only for
// the duration of an `echo run … --log` run (see recipe.go) so the whole
// transcript can be captured to a file; nil in the REPL and in a plain
// `echo run`, where the tee is skipped. A run is sequential, so no
// locking is needed beyond what sess.print already assumes.
var runLogSink io.Writer

// logDBMax is the max display width of the database name in styled log
// lines before it's middle-truncated (so a long name doesn't wrap the rest
// of the line). Defaults to the config default; newSession overrides it
// from cfg.LogDBMax at session start.
var logDBMax = 20

// teeRunLog writes a plain line (plus newline) to the run-log sink when
// one is active. No-op otherwise.
func teeRunLog(plain string) {
	if runLogSink != nil {
		io.WriteString(runLogSink, plain+"\n")
	}
}

// logField is one structured key/value pair for emitOdooLog. Order is
// preserved when rendering.
type logField struct {
	key, value string
}

// emitOdooLog prints a single status line that mimics Odoo's log
// format, end-to-end, so the post-command summary lives next to the
// container's own log stream without standing out:
//
//	YYYY-MM-DD HH:MM:SS,SSS <pid> LEVEL <db> <logger>: <msg> key=val ...
//
// `logger` is a hierarchical `echo.<cmd>.…` name produced by
// echoCommandLogger. Each segment is colored consistently with the
// rest of the REPL: timestamp dim, PID faint, level chip per level,
// db in palette.Accent, logger via the same FNV pastel rotation used
// for Odoo loggers, message default fg, and per-key colors on the
// structured fields.
func emitOdooLog(level, logger, msg string, fields []logField, s theme.Styles, p theme.Palette, db string) {
	emitOdooLogTo(os.Stdout, level, logger, msg, fields, s, p, db)
}

// emitOdooLogTo is emitOdooLog with an explicit destination writer. It
// backs the default stdout path and the `modstate --json` error path,
// which writes its diagnostic to stderr so stdout carries only the JSON
// array. The run-log tee still fires regardless of destination.
func emitOdooLogTo(w io.Writer, level, logger, msg string, fields []logField, s theme.Styles, p theme.Palette, db string) {
	// Silenced recipe step (--silent): drop screen + log entirely. The
	// runner's own step/recap lines are emitted with suppression inactive,
	// so they stay visible.
	if outputSuppressed(level) {
		return
	}
	io.WriteString(w, renderOdooLog(level, logger, msg, fields, s, p, db)+"\n")
	teeRunLog(plainOdooLogFields(level, logger, msg, fields, db))
}

// renderOdooLog builds the styled Odoo-format line emitOdooLog prints, but
// returns it as a string (generating a fresh timestamp + pid). Used both by
// emitOdooLog and by the `shell` line transform, which reformats loose
// stderr (wkhtmltopdf `Warn:`/`Error:`) into this style on the fly.
func renderOdooLog(level, logger, msg string, fields []logField, s theme.Styles, p theme.Palette, db string) string {
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	ts = strings.Replace(ts, ".", ",", 1)
	pid := strconv.Itoa(os.Getpid())
	if db == "" {
		db = "-"
	}
	db = theme.MiddleTruncate(db, logDBMax)

	short, levelStyle := shortLevel(level, p)
	dbStyle := lipgloss.NewStyle().Foreground(p.Accent)
	loggerStyle := lipgloss.NewStyle().Foreground(loggerColor(logger))

	line := s.Dim.Render(ts) + " " +
		s.Faint.Render(pid) + " " +
		levelStyle.Render(short) + " " +
		dbStyle.Render(db) + " " +
		loggerStyle.Render(logger+":")
	if msg != "" {
		line += " " + s.Out.Render(msg)
	}

	for _, f := range fields {
		keyStyle := keyColor(f.key, p)
		valText := quoteIfNeeded(f.value)
		if vs, ok := valueStyleFor(f.key, f.value, p); ok {
			line += " " + keyStyle.Render(f.key) + "=" + vs.Render(valText)
		} else {
			line += " " + keyStyle.Render(f.key) + "=" + s.Out.Render(valText)
		}
	}
	return line
}

// keyColor returns the lipgloss style for a known structured-field
// key, mirroring the per-key palette used on the ERRO/OK lines.
// Unknown keys fall back to the dim color.
func keyColor(key string, p theme.Palette) lipgloss.Style {
	bold := lipgloss.NewStyle().Bold(true)
	switch key {
	case "module", "modules":
		return bold.Foreground(p.Accent)
	case "err", "errors":
		return bold.Foreground(p.Error)
	case "warnings":
		return bold.Foreground(p.Warning)
	case "copied":
		return bold.Foreground(p.Info)
	}
	return lipgloss.NewStyle().Foreground(p.Dim)
}

// valueStyleFor returns the style for a structured field's VALUE, keyed on
// the field name (and value) — mirroring how keyColor styles the key. It
// makes the run recap readable at a glance: the status word is colored by
// outcome (ok green, failed red, cancelled/skipped amber) and the cmd is
// tinted by its action (the first token), reusing the logger pastel
// rotation so each action keeps a stable color. Returns ok=false to fall
// back to the default foreground.
func valueStyleFor(key, value string, p theme.Palette) (lipgloss.Style, bool) {
	switch key {
	case "status":
		switch value {
		case "ok":
			return lipgloss.NewStyle().Foreground(p.Success).Bold(true), true
		case "failed":
			return lipgloss.NewStyle().Foreground(p.Error).Bold(true), true
		case "cancelled", "skipped":
			return lipgloss.NewStyle().Foreground(p.Warning), true
		}
	case "cmd":
		action := value
		if i := strings.IndexByte(action, ' '); i >= 0 {
			action = action[:i]
		}
		if action != "" {
			return lipgloss.NewStyle().Foreground(loggerColor(action)), true
		}
	}
	return lipgloss.Style{}, false
}

// quoteIfNeeded wraps values that contain whitespace or quotes so the
// emitted line stays parseable.
func quoteIfNeeded(v string) string {
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, " \t\"") {
		return strconv.Quote(v)
	}
	return v
}

// plainOdooLog renders the same Odoo-style line as emitOdooLog but
// without any ANSI styling. Used as the first line of the clipboard
// payload on auto-copy so the copied text starts with a self-contained
// header that identifies the failure (timestamp, pid, level, db,
// logger, message) without leaking terminal escapes.
func plainOdooLog(level, logger, msg, db string) string {
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	ts = strings.Replace(ts, ".", ",", 1)
	pid := strconv.Itoa(os.Getpid())
	if db == "" {
		db = "-"
	}
	base := ts + " " + pid + " " + shortLevelName(level) + " " + db + " " + logger + ":"
	if msg != "" {
		base += " " + msg
	}
	return base
}

// plainOdooLogFields is plainOdooLog plus the ` key=val` tail, used to
// tee a structured emitOdooLog line into the run-log sink without ANSI.
func plainOdooLogFields(level, logger, msg string, fields []logField, db string) string {
	line := plainOdooLog(level, logger, msg, db)
	for _, f := range fields {
		line += " " + f.key + "=" + quoteIfNeeded(f.value)
	}
	return line
}

// shortLevelName returns the 4-char display label for an Odoo level
// token. Mirrors shortLevel but discards the style — useful for plain
// text contexts (clipboard payload, plain renders).
func shortLevelName(level string) string {
	switch level {
	case "DEBUG":
		return "DEBU"
	case "INFO":
		return "INFO"
	case "WARNING":
		return "WARN"
	case "ERROR":
		return "ERRO"
	case "CRITICAL":
		return "CRIT"
	}
	return level
}

// failureLogger appends `.error` to the command logger so the logger
// path itself encodes the severity. Used by every auto-copy path so
// the line shows up as e.g. `echo.update.module.sale.error` —
// hierarchical, greppable, distinguishable from the success line that
// drops the suffix.
func failureLogger(cmd string, resolved []string) string {
	return echoCommandLogger(cmd, resolved) + ".error"
}

// startLogger builds the logger path for the start line of a non-module
// command: `echo.<cmd>.start`. Their positional args (if any) ride along
// as a structured field on the start line. Module commands (install /
// update / uninstall / test) no longer use this — their start line is
// emitted by startResolved once the module set is resolved, so it can
// name the actual modules (picker / --last) in both the logger and the
// modules= field.
func startLogger(name string) string {
	return "echo." + name + ".start"
}

func isModuleCommand(name string) bool {
	switch name {
	case "install", "update", "uninstall":
		return true
	}
	return false
}

// echoCommandLogger builds a hierarchical logger name for a module
// command, so the post-command status line looks at home next to
// Odoo's own `odoo.modules.loading`, `odoo.service.server` paths:
//
//	1 module → echo.<cmd>.module.<mod>     (e.g. echo.update.module.sale)
//	--all    → echo.<cmd>.all              (e.g. echo.update.all)
//	N>1      → echo.<cmd>.modules
//	none     → echo.<cmd>
func echoCommandLogger(cmd string, resolved []string) string {
	base := "echo." + cmd
	switch {
	case len(resolved) == 1 && resolved[0] == "--all":
		return base + ".all"
	case len(resolved) == 1:
		return base + ".module." + resolved[0]
	case len(resolved) > 1:
		return base + ".modules"
	}
	return base
}
