package repl

import "regexp"

var odooLevel = regexp.MustCompile(`\b(DEBUG|INFO|WARNING|ERROR|CRITICAL)\b`)

// classifyOdooLog returns the Line.Kind for an Odoo log line. Non-matching
// lines fall back to "out", except when the previous line was an err/warn
// level — in that case the continuation (indented traceback frames, the
// non-indented `Traceback (most recent call last):` header, and the final
// `ExceptionType: message` line) inherits the previous kind so the full
// failure stays grouped and gets included in copy-on-failure.
func classifyOdooLog(line, previousKind string) string {
	if m := odooLevel.FindString(line); m != "" {
		switch m {
		case "DEBUG":
			return "faint"
		case "INFO":
			return "info"
		case "WARNING":
			return "warn"
		case "ERROR", "CRITICAL":
			return "err"
		}
	}
	if previousKind == "err" || previousKind == "warn" {
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

// runStats observes streamed lines and counts those classified as
// ERROR/CRITICAL severity. Used to detect silent failures where the
// subprocess exits 0 but logged errors. Counts only level-prefixed
// lines, so traceback continuations don't inflate the total.
type runStats struct{ errors int }

func (s *runStats) wrap(inner func(string)) func(string) {
	return func(line string) {
		if m := odooLevel.FindString(line); m == "ERROR" || m == "CRITICAL" {
			s.errors++
		}
		inner(line)
	}
}
