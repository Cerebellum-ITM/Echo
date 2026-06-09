package repl

import "strings"

// suppressLevel is the active per-step output suppression threshold while a
// recipe step runs under `--silent` (-1 = inactive, the default outside a
// silenced step). A line is suppressed when its level rank is
// <= suppressLevel; bare `--silent` uses silentAll to drop everything.
// It gates the stdout write and the `--log` tee in both sess.print and
// emitOdooLog. It does NOT gate lastOutputBuffer, so `report` still
// captures the silenced lines. A run is sequential, so a package-level
// switch is enough (same pattern as runLogSink).
var suppressLevel = -1

// silentAll is the suppression level for a bare `--silent`: above every
// real level rank, so every line is dropped.
const silentAll = 99

// outputSuppressed reports whether a line of the given level token should
// be dropped from screen + log under the active suppression. An empty
// level (plain output, no level) ranks 0 — suppressed by any active
// threshold, shown when inactive.
func outputSuppressed(level string) bool {
	return suppressLevel >= 0 && levelRank[level] <= suppressLevel
}

// stripSilent removes a `--silent` / `--silent=<lvl>` token from a recipe
// step's args and returns the cleaned args, the suppression level
// (-1 = none, silentAll = all, else the level's rank → suppress that level
// and below), and a display label ("all" / the level name / ""). A
// `--silent=<bad>` value is left in `bad` so the caller can warn and run
// without suppression. The token is recipe-only: it's intercepted by the
// runner and never reaches the underlying command.
func stripSilent(args []string) (clean []string, suppress int, label, bad string) {
	suppress = -1
	for _, a := range args {
		switch {
		case a == "--silent":
			suppress, label = silentAll, "all"
		case strings.HasPrefix(a, "--silent="):
			raw := strings.TrimPrefix(a, "--silent=")
			if lvl := normalizeLevel(raw); lvl != "" {
				suppress, label = levelRank[lvl], strings.ToLower(lvl)
			} else {
				bad = raw
			}
		default:
			clean = append(clean, a)
		}
	}
	return clean, suppress, label, bad
}
