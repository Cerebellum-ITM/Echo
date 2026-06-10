package repl

import (
	"regexp"
	"strings"
)

// migrationLine matches Odoo's migration-manager log line, emitted once per
// phase (pre/post/end) when a module's data migration runs:
//
//	… odoo.modules.migration: module <mod>: Running migration [<version>] <phase>-migration
//
// The version inside the brackets can carry a trailing range marker (e.g.
// `18.0.0.6>`); observe() trims it. Only the canonical phases are accepted so
// stray text never registers as a migration.
var migrationLine = regexp.MustCompile(
	`odoo\.modules\.migration: module (\S+): Running migration \[([^\]]+)\] (pre|post|end)-migration`,
)

// migration is one module's detected data migration, collapsing the per-phase
// log lines (pre/post/end) into a single record keyed by module + version.
type migration struct {
	module  string
	version string
	phases  []string // in first-seen order: pre, post, end
}

// migrationTracker collects distinct module migrations seen across a stream
// of log lines, deduplicating by module+version and accumulating the phases
// that ran. Zero value is ready to use.
type migrationTracker struct {
	order []string // module\x00version keys, first-seen order
	byKey map[string]*migration
}

// observe inspects one log line and records a migration when the line matches
// the Odoo migration-manager format. Non-matching lines are ignored.
func (mt *migrationTracker) observe(line string) {
	m := migrationLine.FindStringSubmatch(line)
	if m == nil {
		return
	}
	mod := m[1]
	ver := strings.TrimRight(m[2], ">")
	phase := m[3]
	key := mod + "\x00" + ver
	if mt.byKey == nil {
		mt.byKey = map[string]*migration{}
	}
	mg, ok := mt.byKey[key]
	if !ok {
		mg = &migration{module: mod, version: ver}
		mt.byKey[key] = mg
		mt.order = append(mt.order, key)
	}
	if !containsString(mg.phases, phase) {
		mg.phases = append(mg.phases, phase)
	}
}

// migrations returns the collected migrations in first-seen order.
func (mt *migrationTracker) migrations() []migration {
	out := make([]migration, 0, len(mt.order))
	for _, k := range mt.order {
		out = append(out, *mt.byKey[k])
	}
	return out
}

// collectMigrations scans a slice of stored log-line texts and returns the
// distinct module migrations found, deduplicated by module+version. Used by
// `report` to summarize migrations captured during an `echo run`.
func collectMigrations(texts []string) []migration {
	var mt migrationTracker
	for _, t := range texts {
		mt.observe(t)
	}
	return mt.migrations()
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
