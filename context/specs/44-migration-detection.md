# Unit 44: migration-detection — surface Odoo data migrations

## Goal

When a module command (`install` / `update` / `uninstall`) triggers Odoo's
data-migration machinery, Echo should notice it and tell the user. Odoo
already logs each migration phase as:

```
2026-06-10 00:09:01,699 12 INFO habitta_prod odoo.modules.migration: module real_state_bits_finance_quote: Running migration [18.0.0.6>] post-migration
```

but that line scrolls past in a wall of `loading`/`init` output and is easy
to miss. This unit watches the streamed log, collects those lines, and — at
the very end of the command, after the success/failure recap — prints a
compact summary: one `echo.<cmd>.migration` line per migrated module naming
the module, the version it migrated to, and which phases ran. `report`
mirrors the same summary by scanning the whole last `echo run`.

## Design

Migration detection is a **passive observer over the existing log stream** —
it adds no new Odoo invocation. It plugs into the same `StreamOut` callback
that already feeds `logColorer` (coloring) and `runStats` (error/warning
counts) in `runModules`, so it sees every line exactly once.

The Odoo migration manager emits one log line per **phase** (`pre`, `post`,
`end`) of the same module's migration. The summary should not repeat the
module three times, so detection **collapses by module + version** and
accumulates the phases that ran. The version inside the brackets can carry a
trailing range marker (`18.0.0.6>`); it is trimmed to `18.0.0.6`.

The summary lines reuse `emitOdooLog` so they sit visually next to Odoo's own
`odoo.modules.migration` lines — consistent with Echo's Odoo-cohesion
principle (no generic CLI decorations). The logger is hierarchical,
`echo.<cmd>.migration` (e.g. `echo.update.module.<mod>.migration`), matching
the `.start` / `.error` logger naming already in use. They are placed **after**
the success/failure recap so they close the run ("al final"), and they fire
on both success and failure paths (a migration can run before a later error).

For `report`, migrations are INFO lines, so a `--min-level=warn` filter would
hide them; the summary therefore scans **every step of the whole run**,
independent of the `--step` / `--level` filter, and appends
`echo.report.migration` lines after the filtered output.

## Implementation

### `internal/repl/migration.go` (new)

- `migrationLine` regexp matching the Odoo migration-manager line and
  capturing `(module, version, phase)`, with `phase ∈ {pre, post, end}` so
  stray text never registers:
  ```
  odoo\.modules\.migration: module (\S+): Running migration \[([^\]]+)\] (pre|post|end)-migration
  ```
- `migration{module, version string; phases []string}` — one module's
  collapsed record (phases in first-seen order).
- `migrationTracker{order []string; byKey map[string]*migration}` with:
  - `observe(line string)` — on a match, trims a trailing `>` from the
    version, dedupes by `module\x00version`, appends the phase if new.
  - `migrations() []migration` — records in first-seen order.
- `collectMigrations(texts []string) []migration` — runs a fresh tracker over
  a slice of stored line texts (used by `report`).
- `containsString` helper.

### `internal/repl/repl.go` — `runModules`

- Construct a `migs := &migrationTracker{}` alongside `stats`/`lc`.
- In the `StreamOut` closure, call `migs.observe(line)` before
  `sess.emitStreamLine(lc, line)`.
- After the success/failure `switch`, call
  `sess.emitMigrations(name, resolved, migs.migrations())`.

### `internal/repl/copylast.go` — `emitMigrations`

```go
func (sess *session) emitMigrations(name string, resolved []string, migs []migration)
```

No-op when empty. Otherwise emits one INFO line per migration with logger
`echoCommandLogger(name, resolved)+".migration"`, message `migration detected`,
and fields `module`, `version`, and `phases=pre,post` (joined, when present).

### `internal/repl/report.go` — `runReport`

- After printing the filtered lines (non-copy path), iterate
  `collectMigrations(reportTexts(rep))` and emit one
  `echo.report.migration` INFO line per migration (same fields as above).
- `reportTexts(rep config.RunReport) []string` flattens every step's line
  texts across the whole run.

## Dependencies

None new. Reuses `emitOdooLog`, `echoCommandLogger`, `config.RunReport`.

## Verify when done

- [ ] A streamed `update` log containing `pre`/`post`/`end` migration lines
      for one module produces a single `… echo.update.module.<mod>.migration:
      migration detected module=<mod> version=<ver> phases=pre,post,end` line,
      after the `update completed` recap.
- [ ] Two different modules migrating produce two summary lines, in
      first-seen order; the trailing `>` is trimmed from the version.
- [ ] A run with no migration lines prints no migration summary (no-op).
- [ ] `report` (after an `echo run` whose update migrated a module) appends
      the matching `echo.report.migration` line(s), even with
      `--min-level=warn` (which hides the INFO migration log lines themselves).
- [ ] Migration summary fires on a failed `update` too (migration ran before
      a later error).
- [ ] No TS/Go build errors; `go vet` clean; `go test ./...` passes; new
      `migration_test.go` covers dedup, order, trim, and no-match.
