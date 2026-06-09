package repl

import (
	"regexp"
	"strings"
)

// looseSeverity matches a line whose first token is a bare severity
// keyword followed by ':' — the shape tools like wkhtmltopdf/Qt write to
// stderr (e.g. "Warn: Can't find .pfb for face 'Courier'"). The match is
// case-insensitive. Real Odoo and loguru lines start with a timestamp, so
// they never match and keep their rich rendering.
var looseSeverity = regexp.MustCompile(
	`^(?i)(warning|warn|critical|crit|fatal|error|err|info|debug):\s*(.*)$`,
)

// looseSeverityLogger is the synthetic logger the reformatted line is
// attributed to. In practice these loose lines come from the PDF report
// engine's stderr, so the name is honest about the dominant source and is
// trivially changeable if other sources appear.
const looseSeverityLogger = "report.wkhtmltopdf"

// looseLine is the parsed form of a loose-severity stderr line.
type looseLine struct {
	level   string // mapped Odoo level: DEBUG/INFO/WARNING/ERROR/CRITICAL
	message string
}

// parseLooseSeverity returns the parsed line and true when `line` begins
// with a bare severity keyword + ':', false otherwise. The caller
// reformats a match into Echo's Odoo log style via emitOdooLog.
func parseLooseSeverity(line string) (looseLine, bool) {
	m := looseSeverity.FindStringSubmatch(line)
	if m == nil {
		return looseLine{}, false
	}
	return looseLine{level: looseLevel(m[1]), message: m[2]}, true
}

// looseLevel maps a bare severity keyword to an Odoo log level.
func looseLevel(kw string) string {
	switch strings.ToLower(kw) {
	case "warn", "warning":
		return "WARNING"
	case "err", "error":
		return "ERROR"
	case "crit", "critical", "fatal":
		return "CRITICAL"
	case "info":
		return "INFO"
	case "debug":
		return "DEBUG"
	}
	return "INFO"
}
