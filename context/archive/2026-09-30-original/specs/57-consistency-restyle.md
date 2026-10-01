# Unit 57: consistency restyle — db-list, modules, logs

## Goal

Close the styling pass started by the `ps` restyle (Unit 56): bring the last
RAW/MINIMAL command outputs in line with the app's aesthetic (themed tables
and Odoo-style log lines). After an audit, the only inconsistent commands
were `db-list` (printf, no color), `modules` (one bare name per line), and
`logs --follow` (raw docker passthrough). Everything else already emits
`emitOdooLog` lines or themed `Line{Kind:"table"}` rows.

## Design

### `db-list` — themed table

Mirror `modstate`/`ps`: a `name · size · created` table with an accent
header, the active database (`cfg.DBName`) marked with a green `●` and its
name in the ok style; size/created dim; closes with
`echo.db-list: databases listed count=N`. Empty →
`echo.db-list: no databases`. The data fetch moves to `cmd.DBList` (returns
`[]docker.DatabaseInfo`); the REPL renders.

### `modules` — wrapped, count-closed list

Replace the bare one-per-line names + plain `(N modules)` footer with the
picker's wrapped match-list layout (`renderMatchList`, terminal-width
columns) closed by `echo.modules: modules listed count=N`. Empty →
`echo.modules: no modules` (with the `--config` hint). The `--config`
addons-path picker keeps its existing streamed output. Data via
`cmd.ModulesList` (wraps `resolveModules`).

### `logs` — colorized stream

`logs --follow` no longer inherits the child's stdout raw; it pipes and scans
line-by-line, routing each line through the REPL's `emitStreamLine` (the same
Odoo-log colorizer `up`/`down`/`update` already use). `--no-follow` and
`--copy` already streamed through `emitStreamLine`, so only follow changes.
Ctrl+C cancels the follow cleanly (SIGINT cancels the context → kills the
child → ends the scan → reported as a clean exit). Per-line parse cost is
negligible (microseconds; dominated by terminal rendering) even on a live
high-volume stream. Trade-off: Echo's colors replace docker's native ANSI.

## Implementation

- `internal/cmd/db.go`: `RunDBList` → `DBList(opts) ([]docker.DatabaseInfo,
  error)` (no printing).
- `internal/repl/dblist.go`: `runDBListTable`, `emitDBListTable`.
- `internal/cmd/modules.go`: add `ModulesList(opts) ([]string, error)`;
  `RunModules` kept for `--config`.
- `internal/repl/modules_list.go`: `runModulesList`, `emitModulesList`.
- `internal/docker/compose.go`: `LogsFollow` gains an `onLine` param and
  pipes/scans instead of inheriting stdio; SIGINT → context cancel.
- `internal/cmd/docker.go`: follow branch passes `opts.StreamOut`.
- `internal/repl/repl.go`: `db-list` dispatch → `runDBListTable`; `modules`
  dispatch → `runModulesList`.

## Dependencies

- none (reuses `pad`, `renderMatchList`, `emitStreamLine`, `emitOdooLog`).

## Verify when done

- [ ] `db-list` shows an aligned `name · size · created` table; the active DB
      has a green `●` + green name; closes with `databases listed count=N`.
- [ ] `modules` prints a wrapped, width-fitted list and a `modules listed
      count=N` line; `modules --config` still opens the addons-path picker.
- [ ] `logs` (follow) lines are Odoo-colorized like `up`/`update`; Ctrl+C
      stops it cleanly with no error frame; `--no-follow`/`--copy` unchanged.
- [ ] `go build/vet/test ./...` pass; registry cross-checks stay green;
      `CHANGELOG.md` `[Unreleased]` gets a `Changed` entry.
