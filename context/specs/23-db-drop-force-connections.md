# Unit 23: Force-terminate connections on drop/replace

## Goal

Let `--force` clear active connections itself instead of telling the dev
to `down odoo` by hand. Today `db-drop` (and `db-restore --force`'s
replace step) abort when any backend is connected to the target DB —
which leaves orphaned databases (e.g. one left behind by a failed
restore) impossible to delete without manually stopping Odoo. With this
unit, `--force` means "skip the confirmation **and** terminate the
connections, then drop".

## Design

Safety is preserved by keeping the guard for the non-`--force` path:

- `db-drop <name>` (no `--force`): if there are active connections, abort
  with a message that now points at `--force` (not just `down odoo`).
  Otherwise show the red confirm and drop. This still protects the live
  DB from an accidental one-keystroke nuke.
- `db-drop <name> --force`: terminate every other backend on the DB
  (`pg_terminate_backend`), then drop — no confirmation, no abort.
- `db-restore --force` replacing an existing DB: same — terminate the
  connections before dropping the old DB, instead of asserting none exist.

`--force` thus has one coherent meaning across the destructive db
commands: *"I'm sure — bypass the prompt and whatever's holding the DB."*

## Implementation

### `docker.TerminateConnections` — new helper (`internal/docker/postgres.go`)

```go
// TerminateConnections force-closes every other backend connected to db
// so it can be dropped or replaced even while a stale Odoo worker holds
// it open. Run before DropDatabase under --force.
func TerminateConnections(ctx context.Context, composeCmd, dir, dbContainer, user, db string) error {
    if user == "" { user = "postgres" }
    query := `SELECT pg_terminate_backend(pid) FROM pg_stat_activity ` +
        `WHERE datname = '` + escapeIdent(db) + `' AND pid <> pg_backend_pid();`
    return psqlExec(ctx, composeCmd, dir, dbContainer, user, "postgres", query)
}
```

Runs against the `postgres` maintenance DB (same as `DropDatabase` /
`ActiveConnections`), reusing `escapeIdent` and `psqlExec`.

### `RunDBDrop` (`internal/cmd/db.go`)

Restructure so the connection guard only blocks the non-force path:

```go
if flags.force {
    if err := docker.TerminateConnections(ctx, …, target); err != nil {
        return err
    }
} else {
    if err := assertNoActiveConns(ctx, opts, target); err != nil {
        return err
    }
    if err := confirmDrop(opts.Palette, target); err != nil {
        return err
    }
}
if err := docker.DropDatabase(ctx, …, target); err != nil {
    return err
}
```

### `RunDBRestore` replace branch (`internal/cmd/db.go`)

In the `exists && force` branch, swap the `assertNoActiveConns` guard for
`docker.TerminateConnections` before `DropDatabase`, so a `--force`
restore over a busy DB terminates its connections first.

### `ErrActiveConns` message (`internal/cmd/db.go`)

Point at `--force` as the resolution:

```
active connections to the database — pass --force to terminate them and
drop, or stop Odoo first (`down odoo`)
```

### Help text

Update the `db-drop --force` / `db-restore --force` help lines (in
`internal/repl/repl.go` `helpSections`) to mention that `--force` also
terminates active connections.

## Dependencies

None new. Reuses `pg_terminate_backend` (Postgres built-in), `escapeIdent`,
`psqlExec`.

## Verify when done

- [ ] `db-drop <orphan> --force` drops a DB that has an open connection
      (e.g. a stale Odoo worker) without any manual `down odoo`.
- [ ] `db-drop <name>` without `--force` and with active connections still
      aborts, now with the `--force` hint in the message.
- [ ] `db-drop <name>` without `--force` and no connections still shows the
      red confirm and drops.
- [ ] `db-restore --force` over an existing DB that has connections
      terminates them, drops, recreates, and restores — no abort.
- [ ] Dropping a DB with zero connections via `--force` still works (the
      terminate query is a harmless no-op).
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
