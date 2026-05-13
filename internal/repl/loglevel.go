package repl

import "regexp"

var odooLevel = regexp.MustCompile(`\b(DEBUG|INFO|WARNING|ERROR|CRITICAL)\b`)

// classifyOdooLog returns the Line.Kind for an Odoo log line. Non-matching
// lines fall back to "out". Pass the previous kind so indented continuation
// lines (Python tracebacks) inherit the level of the line that triggered them.
func classifyOdooLog(line, previousKind string) string {
	if m := odooLevel.FindString(line); m != "" {
		switch m {
		case "DEBUG":
			return "faint"
		case "INFO":
			return "dim"
		case "WARNING":
			return "warn"
		case "ERROR", "CRITICAL":
			return "err"
		}
	}
	if (previousKind == "err" || previousKind == "warn") && len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
		return previousKind
	}
	return "out"
}

// logColorer remembers the previous classification so traceback indentation
// inherits the level of the line that opened it. Use a fresh instance per
// command run.
type logColorer struct{ last string }

func (l *logColorer) classify(line string) string {
	k := classifyOdooLog(line, l.last)
	l.last = k
	return k
}
