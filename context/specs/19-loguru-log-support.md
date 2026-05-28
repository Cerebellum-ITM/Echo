# Unit 19: Loguru Log Support

## Goal

Extend Echo's log classifier and renderer to understand the **loguru**
format used by custom Odoo modules alongside the standard `logging`
format already handled since Unit 08. Both formats must receive the
same treatment: level-based coloring, per-segment rendering, error
counting for auto-copy, and failure detection.

## Design

### Loguru format

```
2026-05-28 19:03:26.241 | INFO | odoo.addons.module:func:309 - message
```

Compared to the standard Odoo format:

| Field     | Standard Odoo                       | Loguru                           |
|-----------|-------------------------------------|----------------------------------|
| timestamp | `YYYY-MM-DD HH:MM:SS,mmm` (comma)   | `YYYY-MM-DD HH:MM:SS.mmm` (dot)  |
| separator | space                               | ` \| ` (space-pipe-space)        |
| pid       | present (integer)                   | absent                           |
| level     | `INFO` etc. after pid               | `INFO` etc. between pipes        |
| db        | present (string)                    | absent                           |
| logger    | `module.path` (dot-separated)       | `module.path:func:line`          |
| separator | `: ` before message                 | ` - ` before message             |

### Rendering plan

Segment palette for a loguru line — mirrors Unit 08 spirit:

- **timestamp** → `s.Dim` (same as Odoo)
- **LEVEL chip** → bold + level color (same `shortLevel` helper)
- **module path** → `loggerColor(module)` pastel rotation (stable by name)
- **`:func:line`** → `s.Faint` (low-contrast suffix — rarely the focus)
- **message** → `s.Out` (default fg — highest contrast)

No db segment (loguru doesn't carry it). No pid segment.

### Error counting and auto-copy

`runStats.wrap` and `classifyOdooLog` must count loguru WARNING and
ERROR lines on equal footing with standard Odoo lines. Without this,
a loguru `| ERROR |` line from a failing test:
- does not increment `stats.errors`, so `finalize` shows `0 errors`
- does not set `previousKind = "err"`, so the following traceback
  lines are not inherited into the copy buffer
- does not trigger `copyFailureLog`, so the clipboard is empty on
  test failure

### No new public API

All changes are internal to `internal/repl/loglevel.go` and
`internal/repl/logrender.go`. No new files required.

## Implementation

### `internal/repl/loglevel.go`

Add a second regex after `odooLogPrefix`:

```go
// loguruLogPrefix matches the loguru format emitted by custom Odoo modules:
//   YYYY-MM-DD HH:MM:SS.mmm | LEVEL | logger:func:line - msg
// Dot-separated ms, no pid, no db, pipes as delimiters.
var loguruLogPrefix = regexp.MustCompile(
    `^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d+ \| (DEBUG|INFO|WARNING|ERROR|CRITICAL) \| `,
)
```

Update `classifyOdooLog` to check both:

```go
func classifyOdooLog(line, previousKind string) string {
    check := func(m []string) string {
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
        return "out"
    }
    if m := odooLogPrefix.FindStringSubmatch(line); m != nil {
        return check(m)
    }
    if m := loguruLogPrefix.FindStringSubmatch(line); m != nil {
        return check(m)
    }
    if previousKind == "err" || previousKind == "warn" {
        return previousKind
    }
    return "out"
}
```

Update `runStats.wrap` the same way — add loguru check after the
existing odooLogPrefix block:

```go
if m := loguruLogPrefix.FindStringSubmatch(line); m != nil {
    switch m[1] {
    case "ERROR", "CRITICAL":
        s.errors++
    case "WARNING":
        s.warnings++
    }
}
```

### `internal/repl/logrender.go`

Add a second full-line regex and a formatter:

```go
// loguruLogLine matches and captures the parts of a loguru log line.
//   YYYY-MM-DD HH:MM:SS.mmm | LEVEL | module:func:line - msg
var loguruLogLine = regexp.MustCompile(
    `^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d+) \| (DEBUG|INFO|WARNING|ERROR|CRITICAL) \| ([^:]+):([^:]+):(\d+) - (.*)$`,
)

// formatLoguruLine renders a loguru log line with per-segment styling.
// Segment palette:
//   timestamp  → dim
//   LEVEL chip → bold + level color (same shortLevel helper)
//   module     → loggerColor pastel rotation (stable by name)
//   :func:line → faint (low-contrast location suffix)
//   message    → default fg
func formatLoguruLine(line string, s theme.Styles, p theme.Palette) (string, bool) {
    m := loguruLogLine.FindStringSubmatch(line)
    if m == nil {
        return "", false
    }
    ts, level, module, fn, lineno, msg := m[1], m[2], m[3], m[4], m[5], m[6]

    short, levelStyle := shortLevel(level, p)
    moduleStyle := lipgloss.NewStyle().Foreground(loggerColor(module))

    return s.Dim.Render(ts) + " " +
        levelStyle.Render(short) + " " +
        moduleStyle.Render(module) +
        s.Faint.Render(":"+fn+":"+lineno+":") + " " +
        s.Out.Render(msg), true
}
```

Update `formatOdooLine` — rename it or add a dispatch wrapper in
`sess.print` (wherever `formatOdooLine` is called in `repl.go`):

```go
// renderLogLine tries the standard Odoo format first, then loguru.
// Falls back to (empty, false) if neither matches.
func renderLogLine(line string, s theme.Styles, p theme.Palette) (string, bool) {
    if out, ok := formatOdooLine(line, s, p); ok {
        return out, true
    }
    return formatLoguruLine(line, s, p)
}
```

In `repl.go` (wherever `formatOdooLine` is currently called), replace
the call with `renderLogLine`.

### `internal/repl/repl.go` — call site

Search for `formatOdooLine` and replace with `renderLogLine`. The
function signature is identical (`line string, s theme.Styles, p
theme.Palette`) → drop-in replacement.

## Dependencies

None new. All stdlib + existing internal packages.

## Verify when done

- [ ] Loguru `| INFO |` lines are rendered with the LEVEL chip in
      theme-info color (same shade as standard Odoo INFO lines).
- [ ] Loguru `| WARNING |` lines are rendered in warning color AND
      increment `stats.warnings` — visible in the `finalize` line.
- [ ] Loguru `| ERROR |` lines are rendered in error color AND
      increment `stats.errors`, triggering `copyFailureLog` on a
      test failure exactly like a standard Odoo ERROR line.
- [ ] Tracebacks following a loguru `| ERROR |` line inherit the `err`
      kind (no line between the ERROR and its traceback reverts to `out`).
- [ ] Standard Odoo lines (comma-ms, pid, db) are unaffected — all
      existing behavior preserved.
- [ ] Loguru lines with a logger that has no `:func:line` suffix (edge
      case: bare module path with ` - ` separator) fall back gracefully
      to kind-based styling via `classifyOdooLog`, not to a panic.
- [ ] `go build ./...`, `go vet ./...`, and `go test ./internal/repl/...`
      all pass.
