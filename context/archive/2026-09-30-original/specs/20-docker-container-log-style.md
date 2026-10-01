# Unit 20: Docker Container Log Style

## Goal

Reformat the per-resource progress lines that `docker compose` prints during
`up` / `down` / `restart` / `stop` (e.g. `Container dvz_ny_odoo_19-db-1
Restarting`) into Echo's Odoo-style log line so they stop standing out as the
only raw, unaligned output in the stream. Each matched line is re-emitted
through `emitOdooLog` with a `docker.<resource>` logger, the state as the
message verb, and the resource name as a structured `name=` field. Lines that
don't match (real Odoo logs, `ps` table output, anything else) keep their
current handling untouched.

This closes the gap explicitly deferred in Unit 08
(`08-log-level-coloring.md`: "For v1, only Odoo log lines get colored. Compose
output stays as `out`.") and follows the Odoo-cohesion principle already used
by the start/finalize lines.

## Design

### Before / after

Today (raw pass-through, classified as `out`):

```
 Container dvz_ny_odoo_19-db-1  Restarting
 Container dvz_ny_odoo_19-odoo-1  Restarting
 Container dvz_ny_odoo_19-db-1  Started
 Container dvz_ny_odoo_19-odoo-1  Started
```

After (Odoo-style, same shape as `echo.restart.start`):

```
2026-06-02 18:34:47,606 3675802 INFO develop docker.container: restarting name=dvz_ny_odoo_19-db-1
2026-06-02 18:34:47,606 3675802 INFO develop docker.container: restarting name=dvz_ny_odoo_19-odoo-1
2026-06-02 18:34:47,820 3675802 INFO develop docker.container: started name=dvz_ny_odoo_19-db-1
2026-06-02 18:34:47,820 3675802 INFO develop docker.container: started name=dvz_ny_odoo_19-odoo-1
```

### Logger naming (chosen: per-resource, Option A)

The logger mirrors Odoo's own dotted lowercase loggers
(`odoo.modules.loading`, `odoo.service.server`). The verb lives in the
message, not the logger, because a single command emits several distinct
verbs (`restart` → `restarting`/`started`; `up` → `creating`/`created`/
`started`; `down` → `stopping`/`stopped`/`removing`/`removed`):

| Compose resource | Logger             |
|------------------|--------------------|
| `Container`      | `docker.container` |
| `Network`        | `docker.network`   |
| `Volume`         | `docker.volume`    |
| `Image`          | `docker.image`     |

The logger gets the same FNV-pastel color rotation as every other logger via
`loggerColor` — no special-casing required.

### Message and fields

- **Message** = the compose state lowercased (`Restarting` → `restarting`).
- **Field** = `name=<resource-name>` (e.g. `name=dvz_ny_odoo_19-db-1`).
  Reuses the structured-field rendering in `emitOdooLog`. `name` is not a
  known key in `keyColor`, so it falls back to dim — acceptable, and may be
  promoted to `p.Accent` there if we want it to pop.

### Level mapping

The compose state maps to an Odoo level so the line is colored by the same
`shortLevel` / level-chip path as everything else:

| Compose state(s)                                                       | Level    | Rationale                  |
|------------------------------------------------------------------------|----------|----------------------------|
| `Created`, `Started`, `Running`, `Restarted`, `Stopped`, `Removed`, `Healthy`, `Recreated`, `Pulled`, `Built`, `Synchronized` | `INFO`    | Terminal / success states  |
| `Creating`, `Starting`, `Restarting`, `Stopping`, `Removing`, `Recreate`, `Pulling`, `Building`, `Waiting` | `DEBUG`   | Transitional / in-progress |
| `Warning`                                                              | `WARNING` | Compose-reported warning   |
| `Error`                                                                | `ERROR`   | Compose-reported error     |
| (unrecognized state)                                                   | `INFO`    | Safe default               |

Transitional states render faint (DEBUG) so the eye lands on the terminal
state, matching how Odoo's own DEBUG noise recedes.

### Scope / non-goals

- Only the streaming lifecycle commands (`up`, `down`, `restart`, `stop`)
  route through the new parser. `ps` and `logs` keep their current
  pass-through (`readonlyFinalize`) — `ps` is a table, `logs` is already real
  Odoo output handled by the existing classifier.
- The start (`echo.restart.start`) and finalize (`echo.restart`) lines are
  unchanged — this unit only touches the lines *between* them.
- Like `startLog`/`finalize`, the reformatted lines are emitted via
  `emitOdooLog` straight to stdout and are **not** added to the
  copy-on-failure ring (consistent with existing behavior; out of scope here).

## Implementation

### `internal/repl/dockerlog.go` (new)

A pure parser plus an emit helper, kept separate from the Odoo classifier so
the two concerns don't entangle.

```go
package repl

import (
    "regexp"
    "strings"
)

// composeProgress matches a docker compose lifecycle progress line:
//   " Container <name>  <State>"
//   " Network <name>  Created"
//   " Volume <name>  Removed"
// Leading whitespace is optional; the gap before the state is 1+ spaces.
var composeProgress = regexp.MustCompile(
    `^\s*(Container|Network|Volume|Image)\s+(\S+)\s+([A-Za-z]+)\s*$`,
)

// composeLine is the parsed form of a compose progress line.
type composeLine struct {
    resource string // "container", "network", "volume", "image"
    name     string // resource name, e.g. dvz_ny_odoo_19-db-1
    state    string // lowercased verb, e.g. "restarting"
    level    string // mapped Odoo level: DEBUG/INFO/WARNING/ERROR
}

// parseComposeProgress returns the parsed line and true if `line` is a
// recognized compose lifecycle progress line, false otherwise.
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

// composeStateLevel maps a compose state verb to an Odoo log level.
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
```

### `internal/repl/repl.go` — `runDocker` stream callback

The streaming callback tries the compose parser first; on a match it emits the
Odoo-style line, otherwise it falls back to the existing classify-and-print
path. `runStats` still observes the raw line for warning/error counting.

```go
StreamOut: stats.wrap(func(line string) {
    if cl, ok := parseComposeProgress(line); ok {
        emitOdooLog(cl.level, "docker."+cl.resource, cl.state,
            []logField{{"name", cl.name}},
            sess.styles, sess.palette, sess.cfg.DBName)
        return
    }
    sess.print(Line{Kind: lc.classify(line), Text: line})
}),
```

Only `runDocker` changes. `runModules`, `runDB`, etc. keep their callbacks.

### `internal/repl/loglevel.go` — `runStats.wrap` (optional)

`runStats` currently counts only Odoo/loguru level-prefixed lines. Extend it
to also count compose `Error`/`Warning` states so a failed container surfaces
in the `finalize` summary (`errors=`/`warnings=`) and triggers the failure
path. Keep it additive — match compose only when the Odoo/loguru regexes miss:

```go
if m := odooLogPrefix.FindStringSubmatch(line); m != nil {
    countLevel(m[1])
} else if m := loguruLogPrefix.FindStringSubmatch(line); m != nil {
    countLevel(m[1])
} else if cl, ok := parseComposeProgress(line); ok {
    switch cl.level {
    case "ERROR":
        s.errors++
    case "WARNING":
        s.warnings++
    }
}
```

### `internal/repl/dockerlog_test.go` (new)

Table-driven test for `parseComposeProgress`:

- `" Container dvz_ny_odoo_19-db-1  Restarting"` →
  `{container, dvz_ny_odoo_19-db-1, restarting, DEBUG}`, ok.
- `" Container dvz_ny_odoo_19-db-1  Started"` → `…, started, INFO`, ok.
- `" Network dvz_ny_odoo_19_default  Created"` → `…network…, created, INFO`.
- `" Volume foo  Removed"` → `…volume…, removed, INFO`.
- A real Odoo log line → `ok == false` (must NOT be captured).
- A `ps` table row / blank line → `ok == false`.
- Verify `composeStateLevel` mapping for one state per level bucket.

## Dependencies

None new — stdlib `regexp` / `strings` only.

## Verify when done

- [ ] `go build ./...` passes.
- [ ] `go test ./internal/repl/...` passes, including the new
      `dockerlog_test.go`.
- [ ] `restart` shows each container line as
      `… INFO <db> docker.container: started name=<container>` (transitional
      `restarting` faint, terminal `started` INFO).
- [ ] `up` / `down` / `stop` container, network and volume lines are all
      reformatted to the `docker.<resource>` style.
- [ ] A real Odoo log line in the same stream is unaffected (still classified
      by the existing Odoo/loguru classifier).
- [ ] `ps` output (table) and `logs` output are unchanged.
- [ ] The `echo.<cmd>.start` and `echo.<cmd>` finalize lines are unchanged.
- [ ] A compose `Error` line bumps the failure count so `finalize` reports it.
- [ ] `CHANGELOG.md` `[Unreleased]` has an `Added`/`Changed` entry for the
      docker container log alignment.
