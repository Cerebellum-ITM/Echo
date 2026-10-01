# Unit 71: Truncate the DB name in log lines

## Goal

Every Echo/Odoo log line carries the database name in its accent-colored
`db` column. A long name (e.g. an odoo.sh dump like
`mycompany-main-prod_2026-06-18_23-42-53`) pushes the rest of the line
right and wraps the logs that actually need the width. Truncate the **displayed**
DB name to a configurable limit (default 20) with a middle ellipsis, so
long names shrink to `mycompany-…_23-42-53` while normal names
(`habitta_prod`, `my_shop`) are untouched. Display-only — the real DB name
used by commands, the clipboard payload, and the `echo run` transcript are
unchanged.

## Design

The DB name reaches the screen through two render paths, both in package
`repl`:

- `repl.formatOdooLine` (logrender.go) — streamed Odoo lines, where the db
  is parsed out of Odoo's own output.
- `repl.renderOdooLog` (logemit.go) — Echo's own `echo.<cmd>` status lines,
  where the db is passed in.

Both shorten the db right before styling it. A third path,
`cmd.renderOdooLogLine` (the projectless `echo connect <name>` flow), does
the same so that command's logs match.

The shared, pure truncation lives in `theme.MiddleTruncate(s, max)` (both
`repl` and `cmd` import `theme`): keep the head and tail around a single
`…`, total `max` runes; `s` within `max` (or `max<=1`) is returned
unchanged; rune-aware.

The limit is a **global** config value `log_db_max` (default 20), mirroring
`prompt.name_max`. `repl` reads it once into a package var at session
start (`newSession`); the projectless connect path passes `cfg.LogDBMax`
into `directConnectLogger`.

Only the styled (on-screen) renders truncate. `plainOdooLog`/
`plainOdooLogFields` (clipboard header + run-log tee) and the raw streamed
text teed to the run-log keep the full name, so copy/paste and the
`--log` transcript stay faithful and greppable.

## Implementation

### `internal/config`

- `Config.LogDBMax int`; `globalFile.LogDBMax int \`toml:"log_db_max"\``;
  map in `Load` and `SaveGlobal`. `Defaults.LogDBMax = 20`; `applyDefaults`
  sets it when `<= 0`.

### `internal/theme/theme.go`

- `func MiddleTruncate(s string, max int) string` (pure, rune-aware) + test.

### `internal/repl`

- `logemit.go`: package var `logDBMax` (default 20); in `renderOdooLog`,
  `db = theme.MiddleTruncate(db, logDBMax)` after the empty→`-` guard.
- `logrender.go`: in `formatOdooLine`, truncate the parsed `db` before
  `dbStyle.Render`.
- `repl.go`: `newSession` sets `logDBMax = cfg.LogDBMax`.

### `internal/cmd`

- `connect_log.go`: `directConnectLogger(palette, max)`; `renderOdooLogLine`
  truncates db via `theme.MiddleTruncate`.
- `connect_direct.go`: pass `cfg.LogDBMax`.

### Tests

- `theme`: `TestMiddleTruncate` (short/exact unchanged, long→middle, the
  odoo.sh example, `max<=1`, unicode).
- `config`: default applies + global round-trip of `log_db_max`.

## Verify when done

- [ ] With a long DB name, every command's log lines show the middle-
      truncated name; normal names are untouched.
- [ ] `copy-last` / `echo run --log` keep the full name.
- [ ] `log_db_max` in global.toml changes the limit; default is 20.
- [ ] `go build/vet/test` pass.
