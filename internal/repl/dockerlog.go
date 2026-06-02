package repl

import (
	"regexp"
	"strings"
)

// composeProgress matches a docker compose lifecycle progress line:
//
//	" Container <name>  <State>"
//	" Network <name>  Created"
//	" Volume <name>  Removed"
//
// Leading whitespace is optional; the gap before the state is 1+ spaces.
var composeProgress = regexp.MustCompile(
	`^\s*(Container|Network|Volume|Image)\s+(\S+)\s+([A-Za-z]+)\s*$`,
)

// composeLine is the parsed form of a docker compose progress line.
type composeLine struct {
	resource string // "container", "network", "volume", "image"
	name     string // resource name, e.g. dvz_ny_odoo_19-db-1
	state    string // lowercased verb, e.g. "restarting"
	level    string // mapped Odoo level: DEBUG/INFO/WARNING/ERROR
}

// parseComposeProgress returns the parsed line and true if `line` is a
// recognized compose lifecycle progress line, false otherwise. Real Odoo log
// lines, `ps` table rows and blank lines don't match and fall through to the
// caller's existing handling.
func parseComposeProgress(line string) (composeLine, bool) {
	m := composeProgress.FindStringSubmatch(line)
	if m == nil {
		return composeLine{}, false
	}
	return composeLine{
		resource: strings.ToLower(m[1]),
		name:     m[2],
		state:    strings.ToLower(m[3]),
		level:    composeStateLevel(m[3]),
	}, true
}

// composeStateLevel maps a compose state verb to an Odoo log level so the
// reformatted line is colored by the same level-chip path as everything else.
// Transitional states render faint (DEBUG) so the eye lands on the terminal
// state; unrecognized states default to INFO.
func composeStateLevel(state string) string {
	switch state {
	case "Warning":
		return "WARNING"
	case "Error":
		return "ERROR"
	case "Creating", "Starting", "Restarting", "Stopping",
		"Removing", "Recreate", "Pulling", "Building", "Waiting":
		return "DEBUG"
	default:
		return "INFO"
	}
}
