# Unit 67: Live progress for `db-restore`

## Goal

`db-restore` runs silent: after the backup picker it shows nothing until
the final `→ <target>` line, even though `CREATE DATABASE` + `pg_restore`
of a real database takes seconds to minutes. The user has no signal that
anything is happening. Make the command narrate its phases as Odoo-style
log lines **and** stream the long `pg_restore`/`psql` step live, so the UI
always shows activity.

## Design

Two layers, mirroring how `connect` already reports progress
(`ConnectLogger` → `emitOdooLog`):

1. **Per-step INFO lines.** Each phase of the restore emits one
   `echo.db-restore.restore` INFO line as it begins: dropping/replacing an
   existing DB, creating the DB, extracting the archive, restoring data,
   copying the filestore, neutralizing. These are the "pasos" — visible
   milestones, colored like the rest of the log stream.

2. **Live restore output.** `pg_restore` is silent without `--verbose`;
   `psql` only prints notices/errors. The long step is exactly the silent
   gap. So `docker.Restore`/`RestoreSQL` gain an `onLine func(string)`
   callback: when set, `pg_restore` runs with `--verbose` and its stderr
   is scanned line-by-line and forwarded; `psql` forwards its stderr too.
   Each forwarded line becomes a subdued `DEBUG` line under the same
   `echo.db-restore.restore` logger (prefix `pg_restore: ` stripped), so
   the per-object progress flows by while the bold INFO milestones mark
   the phase boundaries.

The progress channel is a new `DBOpts.Log DBLogger` callback, wired in the
REPL's `runDB` to `emitOdooLog` with logger `echo.<cmd>.<step>` and the
target DB in the `db` column — identical shape to the connect logger. It
is `nil`-safe: callers that don't set it (or set it to nil) get today's
silent behavior, so nothing else changes.

`pg_restore --verbose` mixes progress and errors on stderr; the scanner
keeps only lines containing `error`/`fatal` for the failure message (so a
restore failure still surfaces a useful detail, not a wall of "creating
TABLE …"), while forwarding everything to `onLine`.

## Implementation

### `internal/docker/pgdump.go`

- Add `onLine func(string)` as the last param of `Restore` and
  `RestoreSQL`. `Restore` appends `--verbose` to the `pg_restore` argv
  only when `onLine != nil`.
- New helper `streamStderr(cmd *exec.Cmd, onLine func(string)) (detail string, err error)`:
  pipes stderr, `Start`s, scans each line (1 MB buffer cap), forwards to
  `onLine`, collects `error`/`fatal` lines into `detail`, returns
  `cmd.Wait()`'s error. Both restore funcs use it and format the failure
  as `pg_restore: <wait-err>: <detail>` / `psql restore: …`.
- `Dump` is unchanged (out of scope; `db-backup` keeps its current
  behavior).

### `internal/cmd/db.go`

- New type + field:
  ```go
  type DBLogger func(level, step, msg, db string, fields ...[2]string)
  // in DBOpts:
  Log DBLogger
  ```
- `func (o DBOpts) log(level, step, msg, db string, fields ...[2]string)`
  — nil-safe wrapper.
- `func restoreLineLogger(opts DBOpts, target string) func(string)` —
  returns the `onLine` that strips the `pg_restore:` prefix, drops blank
  lines, and emits a `DEBUG` `echo.db-restore.restore` line for `target`.
- `RunDBRestore`: emit INFO milestones —
  `dropping existing database` (the `--force` replace path),
  `creating database`, `restoring data` (field `file=<basename>`),
  `neutralizing` (before `neutralizeDB`) — and pass
  `restoreLineLogger(opts, target)` to `docker.Restore`.
- `restoreFromZip`: emit `extracting archive` and `copying filestore`
  milestones, and pass the same `onLine` to `docker.Restore`/`RestoreSQL`.

### `internal/repl/repl.go`

- In `runDB`, set `opts.Log` to a closure that builds logger
  `echo.<name>` + `.<step>`, defaults `db` to `sess.cfg.DBName`, maps
  `[2]string` fields to `logField`, and calls `emitOdooLog`.

### Tests (`internal/cmd/db_test.go`)

- `TestRestoreLineLogger`: blank/whitespace lines are dropped, the
  `pg_restore:` prefix is stripped, and each surviving line emits a
  `DEBUG`/`restore`/`<target>` event with the cleaned message.

## Verify when done

- [ ] `db-restore` shows the milestone INFO lines (creating database,
      restoring data, …) and a live stream of `pg_restore` progress
      between the picker and the final `→ <target>`.
- [ ] A failing restore still reports a useful error detail (the
      `error:` lines), not the whole verbose dump.
- [ ] The zip path also narrates extract + filestore copy.
- [ ] `--neutralize` shows the `neutralizing` milestone before the
      neutralize stream.
- [ ] `go build/vet/test` pass; the registry/help cross-checks are
      unaffected (no new command).
