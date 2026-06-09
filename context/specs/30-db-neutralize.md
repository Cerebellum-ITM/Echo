# Unit 30: Neutralize a database (`db-neutralize` + `db-restore --neutralize`)

## Goal

Expose Odoo's native database neutralization through Echo so a dev can
turn a production copy into a safe, non-production database in one
command. Add a `db-neutralize [name]` command that runs `odoo neutralize
-d <db>` inside the Odoo container, and a `--neutralize` flag on
`db-restore` that neutralizes the target right after a successful
restore (the prod→test flow). Neutralization deactivates the parameters
Odoo flags as production-only: outgoing mail / fetchmail servers, cron
jobs, payment providers, the environment ribbon, etc. — driven entirely
by the `data/neutralize.sql` files Odoo ships per installed module, so
Echo never hand-rolls the SQL.

## Design

Neutralization is the **inverse risk** of `db-drop`: it's safe on a
throwaway copy and destructive on production (it wipes the real mail and
payment configuration). So the guard is the opposite of `drop`'s — we do
**not** block on active connections (neutralize runs fine while Odoo is
up), and we confirm only when the target looks like something you'd
regret neutralizing:

- `db-neutralize <name>`: if `name == cfg.DBName` (the active DB) **or**
  `stage == prod`, show a red `huh.Confirm` warning that real mail
  servers / payment providers will be deactivated. Otherwise run
  directly. `--force` skips the confirm.
- `db-neutralize` with no name: resolve to `cfg.DBName`; if that's also
  empty, open the single-select fuzzy picker over the database list
  (same UX as `db-drop`).
- `db-restore … --neutralize`: after the restore completes, neutralize
  the freshly restored `target`. Because the target of a restore is by
  construction a copy (and is rarely the active DB), no extra confirm is
  added on this path beyond restore's own `--force` semantics — the flag
  is an explicit opt-in.

Streamed output is colored by the existing Odoo log classifier (same as
`install`/`update`/`test`), so neutralization INFO/WARNING lines render
in theme. The success footer line is `→ <target> (neutralized)`, matching
the `→ <name>` convention of the other db commands.

## Implementation

### `odoo.Neutralize` — new builder (`internal/odoo/cmd.go`)

`neutralize` is a CLI **subcommand**, so it goes immediately after the
`odoo` token, before the connection flags:

```go
// Neutralize builds the argv for `odoo neutralize`, which applies the
// per-module data/neutralize.sql to the target DB (disabling crons,
// mail/fetchmail servers, payment providers, the env ribbon, …). The
// subcommand exits on its own — no --stop-after-init needed.
func Neutralize(c Conn) Cmd {
    return append(Cmd{"odoo", "neutralize"}, c.flags()...)
}
```

`Conn.Flags()` already emits `-d <DB>` plus `--db_host/--db_port/
--db_user/--db_password`, which is what `compose exec` needs since it
bypasses the image entrypoint (same rationale as every other builder in
this file). Flag set verified identical on Odoo 17 / 18 / 19.

### `dbFlags.neutralize` — parse flag (`internal/cmd/db.go`)

Extend `dbFlags` with `neutralize bool` and add the case to
`parseDBArgs`:

```go
case a == "--neutralize":
    f.neutralize = true
```

### `RunDBNeutralize` — new command (`internal/cmd/db.go`)

```go
// RunDBNeutralize runs `odoo neutralize` against the target DB. Target
// defaults to cfg.DBName; a positional arg overrides it; if neither is
// set, a picker is shown. Confirms (red) when the target is the active
// DB or stage=prod, unless --force is passed.
func RunDBNeutralize(ctx context.Context, opts DBOpts) error
```

Steps:

1. `requireDBContainer(opts.Cfg)` **and** `requireOdooConfig(opts.Cfg)`
   (it runs through the Odoo container).
2. Resolve `target`: `positional[0]` → else `cfg.DBName` → else
   `runSingleFuzzyPicker("Pick a database to neutralize", names, …)`
   over `docker.ListDatabases(…)` (reuse the `db-drop` pattern; error
   if the list is empty).
3. Guard: if `!flags.force && (target == cfg.DBName ||
   strings.EqualFold(cfg.Stage, "prod"))`, call a new
   `confirmNeutralize(opts.Palette, target)` red confirm; return
   `ErrCancelled` on No/Esc.
4. Build the `odoo.Conn` from `.env` (same block as `RunOdooShell`:
   DB=target, Host=DBContainer, Port/User/Password from `env.Load`,
   default port `5432`).
5. Run `docker.Exec(ctx, cfg.ComposeCmd, opts.Root, cfg.OdooContainer,
   odoo.Neutralize(conn), opts.StreamOut)`.
6. On success, `opts.StreamOut("→ " + target + " (neutralized)")`.

`confirmNeutralize` mirrors `confirmDrop`/`confirmProd`: red bold db
name, Title `⚠  About to neutralize <db>`, Description "Disables mail
servers, crons, and payment providers. Don't run this on production."
Affirmative `Neutralize` / Negative `Cancel`.

Note: `RunDBNeutralize` needs the `odoo` and `env` imports (db.go already
imports `env`; add `odoo`).

### `RunDBRestore` — wire `--neutralize` (`internal/cmd/db.go`)

After a restore succeeds, neutralize when the flag is set. Factor the
neutralization core into a helper so both entry points share it:

```go
func neutralizeDB(ctx context.Context, opts DBOpts, target string) error {
    // build Conn + docker.Exec(odoo.Neutralize(conn)); used by
    // RunDBNeutralize (after its guard) and RunDBRestore (--neutralize).
}
```

`restoreFromZip` returns before the plain-dump path's footer, so call the
neutralize step in **both** the zip branch and the plain `.dump` branch
of `RunDBRestore` once the restore is done. Append `(neutralized)` to the
footer when it ran. Keep it last so a neutralize failure surfaces after
the data is already in place.

### REPL wiring

- `internal/repl/commands.go`:
  - `Registry`: add `"db-neutralize"` (in the Database group, after
    `"db-drop"`).
  - `commandFlags`: add `"db-neutralize": {"--force"}` and append
    `"--neutralize"` to `"db-restore"`'s slice.
- `internal/repl/repl.go`:
  - `dispatch` switch: add `"db-neutralize"` to the `runDB` case list.
  - `runDB` switch: `case "db-neutralize": err = cmd.RunDBNeutralize(ctx, opts)`.
  - `helpSections()` Database section: add
    `{"db-neutralize [name]", "Neutralize a DB (disable mail/cron/payments)"}`,
    `{"  --force", "Skip the active-DB / prod confirmation"}`, and under
    `db-restore` add `{"  --neutralize", "Neutralize the DB after restoring"}`.

### Tests (`internal/cmd/db_test.go`, `internal/repl/*_test.go`)

- Table test for `parseDBArgs` covering `--neutralize` (alone and mixed
  with `--as`/`--force`).
- Table test for `odoo.Neutralize` asserting argv `["odoo","neutralize",
  "-d","db", …]`.
- The existing `registry_test.go` / `commandhl_test.go` cross-checks
  (Registry ↔ dispatchNames ↔ helpCommandNames, and the `commandFlags`
  init-guard) must stay green with the new command and flag.

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: `db-neutralize` command and
  `db-restore --neutralize` flag.
- `context/progress-tracker.md` → mark Unit 30 done with a session note.

## Dependencies

None new. Uses Odoo's built-in `neutralize` CLI subcommand (v16+),
`docker.Exec`, `odoo.Conn`, `env.Load`, and the existing `huh`/picker
helpers.

## Verify when done

- [ ] `db-neutralize <copy>` on a non-active, non-prod DB runs
      `odoo neutralize` with no prompt and prints `→ <copy> (neutralized)`.
- [ ] `db-neutralize` with no arg targets `cfg.DBName`; with no configured
      DB it opens the picker.
- [ ] Neutralizing the active DB (`target == cfg.DBName`) or with
      `stage=prod` shows the red confirm; `--force` skips it.
- [ ] After neutralization, Odoo shows the "neutralized database" state
      (mail servers / crons / payment providers disabled) — confirmed in a
      real container.
- [ ] `db-restore … --neutralize` restores then neutralizes the target,
      and the footer reads `→ <target> (neutralized)` (with filestore note
      preserved on the zip path).
- [ ] `db-restore` without `--neutralize` behaves exactly as before.
- [ ] Flag highlighting/Tab-complete: `--force` on `db-neutralize` and
      `--neutralize` on `db-restore` render as known flags.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
