# Unit 09: DB Commands

## Goal

Add four database commands operating on the project's PostgreSQL
container: `db-backup` (dump current DB to `./backups/`), `db-restore`
(restore a previously backed-up file picked via fuzzy picker), `db-drop`
(drop a DB with confirmation), and `db-list` (list non-system DBs with
size, creation date, and an active-marker on `cfg.DBName`).

## Design

All four commands share the same connection pattern as the module
commands: route through `docker compose exec -T <dbContainer>`, using
the PostgreSQL role/password from the project's `.env` (already loaded
by `internal/env`). No direct host connection — everything stays inside
the compose network.

### Connection guard

Before any destructive op (`db-backup`, `db-restore`, `db-drop`), check
how many active connections the target DB has via:

```sql
SELECT count(*) FROM pg_stat_activity
WHERE datname = $1 AND pid <> pg_backend_pid();
```

If the count is `>0`, **abort with a clear message** asking the user to
stop the Odoo service first:

```
✗ db-backup aborted: 3 active connections to "mydb"
  Stop Odoo first: down odoo
```

This is safer than auto-stopping odoo: the user keeps explicit control.
`db-list` does not require this check.

### Backup format

Two modes, controlled by a flag:

- **Default (SQL only)** — `pg_dump -Fc` (custom format, compressed).
  Filename: `<db>_<YYYYMMDD-HHMMSS>.dump`. Smaller and faster.
- **`--with-filestore`** — produces a `.zip` mirroring the Odoo
  database-manager format: `dump.backup` (pg_dump custom) +
  `filestore/<db>/...` (copied from the host's
  `~/.local/share/Odoo/filestore/<db>` if it exists; warn and skip if
  not). Filename: `<db>_<YYYYMMDD-HHMMSS>.zip`.

Both land in `./backups/` (created if missing) relative to the project
root. Add `backups/` to `.gitignore` automatically on first run if a
`.gitignore` exists at the project root and doesn't already list it.

### Restore flow

`db-restore [--as <name>] [--force]`:

1. List candidate files in `./backups/` (`*.dump` and `*.zip`),
   sorted by mtime descending.
2. If none → return `ErrNoBackups` ("no backups found in ./backups/").
3. Open the fuzzy picker (reusing `runFuzzyPicker` from Unit 06) in
   single-select mode. Need a `runSingleFuzzyPicker` variant or a flag
   on the existing picker — see Implementation.
4. Target DB name resolution:
   - `--as <name>` → use it.
   - Otherwise → derive from the filename prefix before the timestamp
     (e.g. `mydb_20260513-130700.dump` → `mydb`).
5. Existence check on target DB:
   - Exists, no `--force` → abort: `✗ db-restore: "mydb" already exists — use --force to replace`.
   - Exists, `--force` → drop first (subject to the active-connections check above), then `createdb`, then restore.
   - Doesn't exist → `createdb`, then restore.
6. Restore execution:
   - `.dump` → `pg_restore -d <db> --no-owner --role=<user>` piped from the file.
   - `.zip` → extract to a temp dir; `pg_restore` the SQL; copy the filestore subtree to the host's filestore path. Warn if the target filestore path doesn't exist.

### Drop flow

`db-drop <name> [--force]`:

1. If `name` is missing → fuzzy-pick from `db-list` results
   (single-select).
2. Connection guard.
3. If no `--force`, present a `huh.Confirm` prompt with the DB name
   rendered in `s.Err` (red):

   ```
   ⚠  About to drop database "mydb" — this cannot be undone.
   Continue? (y/N)
   ```

   Default is `No`. `Esc` / user-abort → `ErrCancelled`.
4. Run `DROP DATABASE "<name>"` via `psql -c`.
5. Final result line surfaces via the existing `finalize` helper from
   Unit 07 — `✓ db-drop completed (mydb)` or `✗ db-drop failed: …`.

### List output

`db-list` prints a small table:

```
  ● mydb          82 MB    2026-05-10
    demo          14 MB    2026-05-12
    test_v18      45 MB    2026-05-09
```

- `●` (filled bullet) in `s.Ok` (green) marks the row matching
  `cfg.DBName`. Other rows get two leading spaces for alignment.
- Name column padded to the widest name (with a 12-char minimum).
- Size from `pg_database_size(datname)`, rendered with `pg_size_pretty`.
- Creation date from `(pg_stat_file('base/'||oid||'/PG_VERSION')).modification`
  formatted as `YYYY-MM-DD`. Fall back to `—` if the query fails (no
  permission to `pg_stat_file` in some hosted Postgres setups).

Single `psql` call with one query joining `pg_database` + the helpers,
parsed line-by-line.

## Implementation

### `internal/docker/postgres.go` — new helpers

Add:

```go
// DatabaseInfo returned by ListDatabasesDetailed.
type DatabaseInfo struct {
    Name      string
    SizeBytes int64
    SizeHuman string // pg_size_pretty
    CreatedAt string // YYYY-MM-DD or "—"
}

// ListDatabasesDetailed runs a single psql query and returns one row
// per non-system DB.
func ListDatabasesDetailed(ctx context.Context, composeCmd, dir, dbContainer, user string) ([]DatabaseInfo, error)

// ActiveConnections returns count(*) of pg_stat_activity rows other
// than the caller for `db`.
func ActiveConnections(ctx context.Context, composeCmd, dir, dbContainer, user, db string) (int, error)

// DropDatabase executes DROP DATABASE "<name>" via psql -c.
func DropDatabase(ctx context.Context, composeCmd, dir, dbContainer, user, db string) error

// CreateDatabase executes CREATE DATABASE "<name>" OWNER <user>.
func CreateDatabase(ctx context.Context, composeCmd, dir, dbContainer, user, db string) error

// DatabaseExists returns true if a DB with the given name is in the cluster.
func DatabaseExists(ctx context.Context, composeCmd, dir, dbContainer, user, db string) (bool, error)
```

All helpers route through `compose exec -T <dbContainer>` and use the
provided `user` (defaults to `postgres` when empty, as in
`ListDatabases`).

### `internal/docker/pgdump.go` — new file

```go
// Dump runs `pg_dump -Fc -U <user> <db>` inside dbContainer and writes
// the binary output to outPath. Returns ErrAborted if Wait fails.
func Dump(ctx context.Context, composeCmd, dir, dbContainer, user, db, outPath string) error

// Restore runs `pg_restore -U <user> -d <db> --no-owner --role=<user>`
// inside dbContainer, piping the contents of inPath to stdin via
// `docker cp` + exec, or directly with `cat <local> | compose exec -T`.
func Restore(ctx context.Context, composeCmd, dir, dbContainer, user, db, inPath string) error
```

For `Dump`, the output is piped from the container's stdout to the
local file (`exec -T` doesn't allocate a TTY, so binary stdout is
safe). For `Restore`, redirect a local file into the container's stdin
the same way.

### `internal/cmd/db.go` — new file

Public entry points mirroring the existing `RunInstall`, `RunDocker`
shape:

```go
type DBOpts struct {
    Cfg       *config.Config
    Root      string
    Args      []string
    Palette   theme.Palette
    StreamOut func(string)
}

func RunDBBackup(ctx context.Context, opts DBOpts) error
func RunDBRestore(ctx context.Context, opts DBOpts) error
func RunDBDrop(ctx context.Context, opts DBOpts) error
func RunDBList(ctx context.Context, opts DBOpts) error
```

Internal helpers:

- `parseDBArgs(args []string) (flags dbFlags, positional []string)` — strips known flags (`--with-filestore`, `--force`, `--as <name>`).
- `pickBackupFile(opts DBOpts) (string, error)` — lists `./backups/*.{dump,zip}` and opens a single-select fuzzy picker. Returns `ErrCancelled` on abort.
- `pickDatabase(opts DBOpts, title string) (string, error)` — fuzzy-pick from `ListDatabasesDetailed`. Used by `db-drop` when no positional name is given.
- `confirmDrop(palette theme.Palette, name string) error` — `huh.Confirm` rendered red. Returns `huh.ErrUserAborted` on No / Esc.
- `defaultDBFromArgs(opts DBOpts) string` — for `db-backup`, falls back to `opts.Cfg.DBName` if no positional name.

Errors:

```go
var (
    ErrNoBackups    = errors.New("no backups found in ./backups/")
    ErrActiveConns  = errors.New("active connections to the database — stop Odoo first")
    ErrDBExists     = errors.New("database already exists — use --force to replace")
    ErrNoFilestore  = errors.New("no filestore directory for this database — skipping")
)
```

### `internal/cmd/picker.go` — single-select variant

Add a small extension to the existing fuzzy picker:

```go
// runSingleFuzzyPicker is the same model as runFuzzyPicker but Enter
// returns the highlighted item directly (no selection toggling). Tab is
// disabled. Returns "" + ErrCancelled on Esc.
func runSingleFuzzyPicker(title string, items []string, palette theme.Palette) (string, error)
```

Implementation: reuse the model struct with a `single bool` field; the
`update` method skips Tab in single mode, and Enter commits the cursor
row instead of `picked`.

### `internal/repl/repl.go` — dispatch + finalize

Extend the dispatch switch:

```go
case "db-backup", "db-restore", "db-drop", "db-list":
    sess.runDB(ctx, cmd, args)
```

`runDB` mirrors `runModules` — it instantiates a `logColorer` and
`runStats`, calls the corresponding `cmd.RunDB*`, and routes through
`finalize` for `backup`/`restore`/`drop`. `db-list` skips `finalize`
because it's instantaneous and the table is the result.

Final result strings:

- `✓ db-backup completed (mydb → backups/mydb_20260513-130700.dump)`
- `✓ db-restore completed (mydb)`
- `✓ db-drop completed (mydb)`

For `db-backup`, the relative output path is appended for confirmation.

### `.gitignore` handling

On first successful `db-backup`, after writing the file:

1. Open `<root>/.gitignore` if it exists.
2. If no line equals exactly `backups/` (whitespace-trimmed), append
   `backups/\n`. Otherwise no-op.

Do not create a `.gitignore` if absent — that's a project-wide decision
for the user, not Echo's call.

### Help integration

Add a "Database" section to `runHelp` between "Modules" and "Docker":

```
Database
  db-backup [name]              Dump DB (default: configured DB) to ./backups/
    --with-filestore            Include filestore (.zip instead of .dump)
  db-restore [--as name] [--force]
                                Pick a backup and restore (creates DB; --force replaces)
  db-drop [name] [--force]      Drop a database (confirmation by default)
  db-list                       List non-system DBs with size, creation date, ● marks active
```

## Dependencies

None new. Reuses:

- `internal/docker` for compose exec + psql + pg_dump/restore wrappers (new helpers within the package).
- `internal/env` for POSTGRES_USER.
- `internal/cmd/picker.go` for the fuzzy picker (single-select variant added).
- `huh` for the drop-confirmation prompt.
- `archive/zip` (stdlib) for `--with-filestore` packing/unpacking.
- `internal/repl/loglevel.go` for `runStats` + `finalize` (Unit 07).

## Verify when done

- [ ] `db-list` prints all non-system DBs with size and creation date, with `●` marking `cfg.DBName`.
- [ ] `db-backup` writes `./backups/<db>_<ts>.dump` and the post-command line includes the relative path.
- [ ] `db-backup --with-filestore` writes a `.zip` containing `dump.backup` plus a `filestore/<db>/` subtree when the host directory exists; warns when it doesn't.
- [ ] `db-restore` opens a single-select fuzzy picker scoped to `./backups/`, derives the target name from the filename, and creates the DB.
- [ ] `db-restore` aborts with `ErrDBExists` if the target DB exists and `--force` isn't passed.
- [ ] `db-restore --force` drops the existing DB and recreates it.
- [ ] `db-drop mydb` shows a red confirmation prompt and aborts on `No`/`Esc`; `--force` skips the prompt.
- [ ] Any destructive op aborts with a clear message when active connections to the target DB exist.
- [ ] `backups/` is appended to `.gitignore` on first backup if not already listed.
- [ ] `go build ./...` and `go vet ./...` pass.
- [ ] Help shows the new Database section.
