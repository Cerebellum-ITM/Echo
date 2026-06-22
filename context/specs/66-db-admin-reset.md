# Unit 66: Reset the admin user (`db-admin`)

## Goal

Add a `db-admin [name]` command to the Database category that resets the
login **and** password of the user with `id = 2` (Odoo's admin user) to
`admin` / `admin`, so a dev can sign into the back office without knowing
the current credentials. Target defaults to `cfg.DBName`; a positional
arg overrides it; if neither resolves, the same single-select picker as
`db-drop`/`db-neutralize` is shown.

## Design

This is a pure-PostgreSQL operation (no Odoo container needed), so it
lives entirely on the `psql` machinery like the rest of `internal/docker/
postgres.go`. The reset is a single statement:

```sql
UPDATE res_users SET login = 'admin', password = 'admin' WHERE id = 2;
```

Storing the password as **plain text** is intentional and safe for Odoo:
the default `res.users` crypt context is
`CryptContext(['pbkdf2_sha512', 'plaintext'], deprecated=['plaintext'])`,
so Odoo verifies the plaintext value on the next login and transparently
re-hashes it to `pbkdf2_sha512`. This avoids hand-rolling a passlib hash
in Go and works across Odoo 16/17/18/19 (the `password` column has held
the hash since v12).

### Risk / guard

Resetting admin to a known `admin/admin` is harmless on a dev DB and a
security hole on production. Mirroring `db-neutralize`'s guard:

- if `!flags.force && stage == prod`, show a red `huh.Confirm` warning
  before doing anything; `--force` skips it.
- the active DB is *not* guarded — it's the normal target (you want into
  your running dev DB), so prompting every time would be noise.

The `UPDATE ... RETURNING id` reports whether a row matched; if uid 2 is
absent (empty DB, custom user table) the command fails with a clear
`no user with id 2 in "<db>"` instead of a silent no-op.

## Implementation

### `docker.ResetUserCredentials` (`internal/docker/postgres.go`)

```go
// ResetUserCredentials sets the login and password of the res_users row
// with the given id, returning found=false when no such user exists. The
// password is stored as plain text: Odoo's default crypt context keeps a
// deprecated `plaintext` scheme, so it verifies on the next login and is
// transparently re-hashed then. Intended for dev databases where regaining
// admin access matters more than the stored hash.
func ResetUserCredentials(ctx context.Context, composeCmd, dir, dbContainer, user, db string, uid int, login, password string) (bool, error)
```

Builds `UPDATE res_users SET login='…', password='…' WHERE id=<uid>
RETURNING id;` (login/password escaped via `escapeIdent`) and runs it
through `psqlScalar`; `found = strings.TrimSpace(out) != ""`.

### `RunDBAdmin` (`internal/cmd/db.go`)

```go
const (
    adminUserID   = 2
    adminLogin    = "admin"
    adminPassword = "admin"
)

func RunDBAdmin(ctx context.Context, opts DBOpts) error
```

Steps:

1. `requireDBContainer(opts.Cfg)`.
2. Resolve `target`: `positional[0]` → else `cfg.DBName` → else picker
   over `docker.ListDatabases(…)` (error if empty).
3. Guard: if `!flags.force && strings.EqualFold(cfg.Stage, "prod")`, call
   `confirmAdminReset(opts.Palette, target)`; return `ErrCancelled` on
   No/Esc. `confirmAdminReset` mirrors `confirmNeutralize`.
4. `docker.ResetUserCredentials(…, adminUserID, adminLogin, adminPassword)`;
   if `!found`, return `fmt.Errorf("no user with id %d in %q", …)`.
5. On success: `opts.StreamOut("→ " + target + "  admin / admin (uid 2)")`.

### REPL wiring

- `internal/repl/commands.go`:
  - `Registry`: add `"db-admin"` first in the Database cluster.
  - `commandFlags`: `"db-admin": {"--force"}`.
- `internal/repl/repl.go`:
  - `dispatch` switch list + `dispatchNames`: add `"db-admin"`.
  - `runDB` switch: `case "db-admin": err = cmd.RunDBAdmin(ctx, opts)`.
  - `helpSections()` Database: `{"db-admin [name]", "Reset admin (uid 2)
    login+password to admin/admin"}` and `{"  --force", "Skip the prod
    confirmation"}`.
- `internal/repl/registry_test.go`: update `TestMatchPrefix` `"db-"` to
  include `"db-admin"` first.

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`.
- `context/progress-tracker.md` → mark Unit 66 done.

## Verify when done

- [ ] `db-admin` on the active dev DB sets login+password to admin/admin
      with no prompt and prints `→ <db>  admin / admin (uid 2)`.
- [ ] Logging in with admin/admin works and Odoo re-hashes the password.
- [ ] `db-admin <name>` targets that DB; no arg + no `cfg.DBName` opens
      the picker.
- [ ] `stage=prod` shows the red confirm; `--force` skips it.
- [ ] A DB with no uid-2 row fails with `no user with id 2 in "<db>"`.
- [ ] Registry ↔ dispatch ↔ help cross-checks stay green; `--force`
      highlights as a known flag.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
