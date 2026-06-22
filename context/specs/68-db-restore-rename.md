# Unit 68: Rename the target DB during `db-restore`

## Goal

When restoring a backup from another environment (e.g. an odoo.sh dump
named `mycompany-prod-12345678`), the derived database name is long and
ugly and then bloats every log line. `db-restore` already supports
`--as <name>` to pick the target name up front, but that means knowing and
typing the name in advance. Add an **interactive name prompt**: after
picking the backup, show an input pre-filled with the derived name so the
user can shorten/rename it (or press Enter to accept) before the restore
runs.

## Design

Right after the backup picker and before `DatabaseExists`/create:

- Compute `derived := dbNameFromBackup(filepath.Base(picked))` (today's
  default name).
- If `--as <name>` was passed, use it verbatim and **skip** the prompt —
  the user already decided.
- Else, if stdin is a TTY (`stdinIsTTY()`), show a `huh.Input` titled
  "Restore as", pre-filled (`Value(&name)`) with `derived`, validated by
  `validateDBName`. The user edits or accepts; the result is the target.
- Else (non-interactive, no `--as`), fall back to `derived` — unchanged
  behavior. (In practice `db-restore` already needs a TTY for the picker,
  so this branch is only a safety net.)

`validateDBName` rejects an empty/whitespace-only name and names
containing whitespace (Odoo/Postgres DB names don't carry spaces). Esc /
Ctrl+C in the input returns `huh.ErrUserAborted`, which `runDB` already
maps to a clean "cancelled" outcome.

The existing-target guard is unchanged: if the chosen name already exists
and `--force` wasn't passed, the restore still fails with `ErrDBExists`.

## Implementation

### `internal/cmd/db.go`

- `RunDBRestore`: replace the `target := flags.asName; if target == "" {
  target = dbNameFromBackup(...) }` block with the derive → `--as` →
  prompt/fallback logic above.
- New `promptRestoreName(palette theme.Palette, suggested string)
  (string, error)`: builds the pre-filled `huh.Input` with
  `BuildHuhTheme`, returns the trimmed name.
- New `validateDBName(s string) error`: non-empty + no-whitespace; reused
  as the input's `Validate`.

### `internal/repl/repl.go`

- Update the `db-restore` help description to mention the rename
  ("Pick a backup, name the target, and restore").

### Tests (`internal/cmd/db_test.go`)

- `TestValidateDBName`: accepts `mydb`, `my_db_2`; rejects ``, `  `,
  `has space`, `tab\tname`.

## Verify when done

- [ ] `db-restore` (no `--as`) shows the name input pre-filled with the
      derived name; editing it restores under the new name; Enter keeps
      the derived one.
- [ ] `db-restore --as foo` skips the prompt and restores as `foo`.
- [ ] An empty or spaced name is rejected inline by the input.
- [ ] Esc/Ctrl+C at the prompt cancels cleanly (no DB created).
- [ ] `go build/vet/test` pass.
