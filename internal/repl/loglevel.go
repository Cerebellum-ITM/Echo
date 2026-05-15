package repl

import "regexp"

// odooLogPrefix matches the canonical Odoo log line prefix and captures
// the level token. Using the full prefix (not just the bare keyword)
// avoids classifying stray text like `# Restart with "--log-handler X:DEBUG"`
// inside a traceback frame as a new log line, which would break
// err-kind inheritance and truncate the captured failure context.
var odooLogPrefix = regexp.MustCompile(
	`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d{3} \d+ (DEBUG|INFO|WARNING|ERROR|CRITICAL) `,
)

// classifyOdooLog returns the Line.Kind for an Odoo log line. Non-matching
// lines fall back to "out", except when the previous line was an err/warn
// level — in that case the continuation (Traceback header, indented
// frames, the final `ExceptionType: message` line, etc.) inherits the
// previous kind so the full failure stays grouped and gets included in
// copy-on-failure.
func classifyOdooLog(line, previousKind string) string {
	if m := odooLogPrefix.FindStringSubmatch(line); m != nil {
		switch m[1] {
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
// ERROR/CRITICAL and WARNING severity. Counts only level-prefixed lines,
// so traceback continuations don't inflate the totals.
type runStats struct{ errors, warnings int }

func (s *runStats) wrap(inner func(string)) func(string) {
	return func(line string) {
		if m := odooLogPrefix.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "ERROR", "CRITICAL":
				s.errors++
			case "WARNING":
				s.warnings++
			}
		}
		inner(line)
	}
}
