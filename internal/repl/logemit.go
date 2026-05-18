package repl

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

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
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	ts = strings.Replace(ts, ".", ",", 1)
	pid := strconv.Itoa(os.Getpid())
	if db == "" {
		db = "-"
	}

	short, levelStyle := shortLevel(level, p)
	dbStyle := lipgloss.NewStyle().Foreground(p.Accent)
	loggerStyle := lipgloss.NewStyle().Foreground(loggerColor(logger))

	line := s.Dim.Render(ts) + " " +
		s.Faint.Render(pid) + " " +
		levelStyle.Render(short) + " " +
		dbStyle.Render(db) + " " +
		loggerStyle.Render(logger+":") + " " +
		s.Out.Render(msg)

	for _, f := range fields {
		keyStyle := keyColor(f.key, p)
		line += " " + keyStyle.Render(f.key) + "=" + s.Out.Render(quoteIfNeeded(f.value))
	}

	os.Stdout.WriteString(line + "\n")
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
	return ts + " " + pid + " " + shortLevelName(level) + " " +
		db + " " + logger + ": " + msg
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
