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

// loguruLogPrefix matches the loguru format emitted by custom Odoo modules:
//
//	YYYY-MM-DD HH:MM:SS.mmm | LEVEL | logger:func:line - msg
//
// Dot-separated ms, no pid, no db, pipes as delimiters.
var loguruLogPrefix = regexp.MustCompile(
	`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d+ \| (DEBUG|INFO|WARNING|ERROR|CRITICAL) \| `,
)

// classifyOdooLog returns the Line.Kind for an Odoo log line. Recognises
// both the standard Odoo format (comma-ms, pid, db) and the loguru format
// (dot-ms, pipes, no pid/db). Non-matching lines fall back to "out", except
// when the previous line was an err/warn level — in that case the continuation
// (Traceback header, indented frames, the final `ExceptionType: message` line,
// etc.) inherits the previous kind so the full failure stays grouped and gets
// included in copy-on-failure.
func classifyOdooLog(line, previousKind string) string {
	levelKind := func(level string) string {
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
	if m := odooLogPrefix.FindStringSubmatch(line); m != nil {
		return levelKind(m[1])
	}
	if m := loguruLogPrefix.FindStringSubmatch(line); m != nil {
		return levelKind(m[1])
	}
	if previousKind == "err" || previousKind == "warn" {
		return previousKind
	}
	return "out"
}

// lineLevel returns a line's own log-level token (DEBUG/INFO/WARNING/
// ERROR/CRITICAL) when it has one, or "" otherwise. Unlike classifyOdooLog
// it does not infer from context (no traceback inheritance) and keeps
// ERROR and CRITICAL distinct — used by `report` to filter stored lines by
// exact level. Recognizes the standard Odoo prefix, the loguru prefix, and
// loose-severity stderr.
func lineLevel(text string) string {
	if m := odooLogPrefix.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	if m := loguruLogPrefix.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	if ll, ok := parseLooseSeverity(text); ok {
		return ll.level
	}
	return ""
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
		countLevel := func(level string) {
			switch level {
			case "ERROR", "CRITICAL":
				s.errors++
			case "WARNING":
				s.warnings++
			}
		}
		if m := odooLogPrefix.FindStringSubmatch(line); m != nil {
			countLevel(m[1])
		} else if m := loguruLogPrefix.FindStringSubmatch(line); m != nil {
			countLevel(m[1])
		} else if cl, ok := parseComposeProgress(line); ok {
			countLevel(cl.level)
		} else if ll, ok := parseLooseSeverity(line); ok {
			// Loose-severity stderr (e.g. wkhtmltopdf): a Warn: counts as a
			// warning, but a loose Error: must not fail an otherwise-healthy
			// run, so only WARNING is counted here.
			if ll.level == "WARNING" {
				s.warnings++
			}
		}
		inner(line)
	}
}
