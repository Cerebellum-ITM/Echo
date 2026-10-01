# Unit 08: Log Level Coloring

## Goal

Color the streamed output of Odoo and docker commands according to log
severity so the user can spot errors at a glance instead of scanning a
wall of grey text.

## Design

Odoo log format (consistent across v17/v18/v19):

```
2026-05-12 19:50:17,356 18 INFO ? odoo: Odoo version 17.0+e-20251104
2026-05-12 19:50:17,357 18 ERROR ? odoo.sql_db: Connection to the database failed
```

Generic pattern: `<date> <time>,<ms> <pid> <LEVEL> <db?> <logger>: <msg>`.
A regex matching `\b(DEBUG|INFO|WARNING|ERROR|CRITICAL)\b` is enough.

Mapping from level to existing `Line.Kind` (which the print method
already styles via the active theme):

| Level     | Line kind | Theme style    |
|-----------|-----------|----------------|
| DEBUG     | `faint`   | `s.Faint`      |
| INFO      | `dim`     | `s.Dim`        |
| WARNING   | `warn`    | `s.Warn`       |
| ERROR     | `err`     | `s.Err`        |
| CRITICAL  | `err`     | `s.Err` (bold) |
| (unmatched) | `out`   | `s.Out`        |

CRITICAL reuses `err` (red) — no separate kind needed for v1. If we want
bold red later, add a `crit` kind.

### Tracebacks

Python tracebacks span multiple lines and only the trigger line carries
the level. To keep the whole traceback in red:

- Track "last classified kind" inside the streaming wrapper.
- If the current line does not match a level **and** starts with
  whitespace (typical of traceback indentation), inherit the previous
  kind. This catches `  File "..."`, `    return ...`, etc.
- Reset to `out` on the next level-prefixed line.

This is a heuristic, but it keeps traceback context visually grouped
without parsing Python syntax.

### docker output

`docker compose` does not follow the Odoo format, but it does print
prefixes like:

```
db Pulling
odoo Pulling
db Pulled
odoo Started
```

For v1, only Odoo log lines get colored. Compose output stays as `out`.
If we want to detect `Error` / `Warning` from compose later, we add a
second classifier — but keep them separate to avoid false positives
(e.g. Odoo log lines mentioning "Error" as text).

## Implementation

### `internal/repl/loglevel.go` (new)

```go
package repl

import (
    "regexp"
    "strings"
)

var odooLevel = regexp.MustCompile(`\b(DEBUG|INFO|WARNING|ERROR|CRITICAL)\b`)

// classifyOdooLog returns the Line.Kind for an Odoo log line. Empty
// "out" for non-matching lines. Pass the previous kind so traceback
// indentation inherits the level.
func classifyOdooLog(line, previousKind string) string {
    m := odooLevel.FindString(line)
    if m != "" {
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
    // Indented continuation of a previous level line (Python traceback).
    if (previousKind == "err" || previousKind == "warn") && len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
        return previousKind
    }
    return "out"
}
```

### Streaming wrapper

A small stateful wrapper that remembers the last kind per command run:

```go
type logColorer struct{ last string }

func (l *logColorer) classify(line string) string {
    k := classifyOdooLog(line, l.last)
    l.last = k
    return k
}
```

### `internal/repl/repl.go` integration

Both `runDocker` and `runModules` instantiate a fresh `logColorer` per
command and call its `classify` in the stream callback:

```go
lc := &logColorer{}
StreamOut: func(line string) {
    sess.print(Line{Kind: lc.classify(line), Text: line})
},
```

This integrates cleanly with the `runStats.wrap` middleware from Unit 07
— wrap the classifier-emitting callback with `stats.wrap`.

## Dependencies

None new — stdlib `regexp`.

## Verify when done

- [ ] `go build ./...` passes.
- [ ] `update sale` shows INFO lines dim, WARNING lines orange, ERROR
      lines red, DEBUG lines faint.
- [ ] A Python traceback after an ERROR line stays red across all its
      lines (until the next non-traceback line).
- [ ] CRITICAL lines render red.
- [ ] Lines without a recognised level render in default `out` style.
- [ ] `up` / `down` / `restart` / `ps` output is unaffected (still `out`).
