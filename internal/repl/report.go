package repl

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/config"
)

// levelRank orders the log levels by severity for the --min-level
// threshold. Levels not present (e.g. "") never satisfy a filter.
var levelRank = map[string]int{
	"DEBUG": 1, "INFO": 2, "WARNING": 3, "ERROR": 4, "CRITICAL": 5,
}

// normalizeLevel maps a user-supplied --level/--min-level value to the
// canonical token, accepting warn≡warning. Returns "" when unrecognized.
func normalizeLevel(v string) string {
	switch strings.ToLower(v) {
	case "debug":
		return "DEBUG"
	case "info":
		return "INFO"
	case "warn", "warning":
		return "WARNING"
	case "error":
		return "ERROR"
	case "critical":
		return "CRITICAL"
	}
	return ""
}

// reportArgs holds the parsed `report` flags.
type reportArgs struct {
	step     int    // 1-based; 0 = all steps
	level    string // exact-match level token; "" = no exact filter
	minLevel string // threshold level token; "" = no threshold filter
	copy     bool
}

// parseReportArgs parses the `report` flags. --level and --min-level are
// mutually exclusive; both validate against the level set.
func parseReportArgs(args []string) (reportArgs, error) {
	var r reportArgs
	for _, a := range args {
		switch {
		case a == "--copy":
			r.copy = true
		case strings.HasPrefix(a, "--step="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--step="))
			if err != nil || n < 1 {
				return reportArgs{}, fmt.Errorf("--step needs a positive number")
			}
			r.step = n
		case strings.HasPrefix(a, "--level="):
			lvl := normalizeLevel(strings.TrimPrefix(a, "--level="))
			if lvl == "" {
				return reportArgs{}, fmt.Errorf("invalid --level; valid: debug, info, warn, error, critical")
			}
			r.level = lvl
		case strings.HasPrefix(a, "--min-level="):
			lvl := normalizeLevel(strings.TrimPrefix(a, "--min-level="))
			if lvl == "" {
				return reportArgs{}, fmt.Errorf("invalid --min-level; valid: debug, info, warn, error, critical")
			}
			r.minLevel = lvl
		default:
			return reportArgs{}, fmt.Errorf("unknown argument: %s", a)
		}
	}
	if r.level != "" && r.minLevel != "" {
		return reportArgs{}, fmt.Errorf("--level and --min-level are mutually exclusive")
	}
	return r, nil
}

// matchLevel reports whether a line's level passes the level filter.
func (r reportArgs) matchLevel(level string) bool {
	switch {
	case r.level != "":
		return level == r.level
	case r.minLevel != "":
		return level != "" && levelRank[level] >= levelRank[r.minLevel]
	default:
		return true
	}
}

// filterReport returns the matching lines from the run, honoring the step
// selection and level filter, plus the number of steps actually scanned.
// An out-of-range step yields an error.
func filterReport(rep config.RunReport, r reportArgs) ([]config.ReportLine, error) {
	steps := rep.Steps
	if r.step > 0 {
		if r.step > len(rep.Steps) {
			return nil, fmt.Errorf("step %d not found — run had %d step(s)", r.step, len(rep.Steps))
		}
		steps = rep.Steps[r.step-1 : r.step]
	}
	var out []config.ReportLine
	for _, s := range steps {
		for _, l := range s.Lines {
			if r.matchLevel(l.Level) {
				out = append(out, l)
			}
		}
	}
	return out, nil
}

// runReport implements `report [--step=N] [--level=lvl | --min-level=lvl]
// [--copy]`: load the last run's record and print or copy the lines that
// match the step + level filter.
func (sess *session) runReport(args []string) {
	r, err := parseReportArgs(args)
	if err != nil {
		sess.print(Line{Kind: "warn", Text: "report: " + err.Error()})
		sess.exitCode = exitUsage
		return
	}

	rep, ok := config.LoadRunReport()
	if !ok {
		sess.print(Line{Kind: "warn", Text: "report: no run yet — run a recipe first"})
		sess.exitCode = exitUsage
		return
	}

	lines, err := filterReport(rep, r)
	if err != nil {
		sess.print(Line{Kind: "warn", Text: "report: " + err.Error()})
		sess.exitCode = exitUsage
		return
	}

	stepField := "all"
	if r.step > 0 {
		stepField = strconv.Itoa(r.step)
	}
	levelField := "all"
	switch {
	case r.level != "":
		levelField = r.level
	case r.minLevel != "":
		levelField = ">=" + r.minLevel
	}

	fields := []logField{
		{"run", rep.Recipe},
		{"step", stepField},
		{"level", levelField},
		{"lines", strconv.Itoa(len(lines))},
	}
	emit := func(level, msg string) {
		emitOdooLog(level, "echo.report", msg, fields,
			sess.styles, sess.palette, sess.cfg.DBName)
	}

	if r.copy {
		if len(lines) == 0 {
			emit("WARNING", "no lines match — nothing copied")
			sess.exitCode = exitOK
			return
		}
		var b strings.Builder
		for _, l := range lines {
			b.WriteString(l.Text)
			b.WriteByte('\n')
		}
		if err := clipboard.WriteAll(b.String()); err != nil {
			emit("ERROR", "copy failed: "+err.Error())
			sess.exitCode = exitError
			return
		}
		plural := "s"
		if len(lines) == 1 {
			plural = ""
		}
		emit("INFO", fmt.Sprintf("copied %d line%s to clipboard", len(lines), plural))
		sess.exitCode = exitOK
		return
	}

	emit("INFO", "report")
	for _, l := range lines {
		sess.print(Line{Kind: kindFromLevel(l.Level), Text: l.Text})
	}
	sess.exitCode = exitOK
}

// levelFromKind maps a classified line Kind back to a level token, used as
// a fallback when a line carries no explicit level in its text (Echo's own
// leveled lines, inherited traceback frames). Kind merges ERROR/CRITICAL,
// so it can only yield ERROR — an explicit CRITICAL token always wins.
func levelFromKind(kind string) string {
	switch kind {
	case "faint":
		return "DEBUG"
	case "info":
		return "INFO"
	case "warn":
		return "WARNING"
	case "err":
		return "ERROR"
	}
	return ""
}

// kindFromLevel maps a stored level token to the print Kind used to color
// the line, mirroring classifyOdooLog's level→kind mapping.
func kindFromLevel(level string) string {
	switch level {
	case "DEBUG":
		return "faint"
	case "INFO":
		return "info"
	case "WARNING":
		return "warn"
	case "ERROR", "CRITICAL":
		return "err"
	}
	return "out"
}
