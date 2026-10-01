# Unit 10: Shell Commands

## Goal

Add three interactive-shell commands that hand the user a live TTY
inside one of the project's containers: `bash` (shell inside the Odoo
container), `psql` (PostgreSQL client connected to the configured DB),
and `shell` (Odoo's Python shell loaded against the configured DB).

## Design

All three commands are TTY pass-throughs: stdin, stdout and stderr are
attached directly to the subprocess and Echo gets out of the way until
the user exits the shell. None of them stream through the `runStats` /
`finalize` pipeline — those are designed for batch commands with a
finite output. The REPL prints a `$ <name>` info line before launching
and resumes the prompt cleanly when the shell exits.

### Target DB

The DB is always `cfg.DBName`. Multi-DB support is out of scope for v1
— if the user needs another DB they switch with `init`. This is
documented in the help text so the expectation is explicit.

### Prod confirmation

For `bash`, `psql`, and `shell`, when `cfg.Stage == "prod"`, Echo
shows a red `huh.Confirm` prompt before opening the shell:

```
⚠  Opening <shell-name> against prod database "mydb".
Continue? (y/N)
```

Default is `No`. The prompt can be bypassed with a `--force` flag, for
scripted workflows. This mirrors the `db-drop` confirmation pattern
from Unit 09.

`dev` and `staging` skip the confirmation entirely.

### Underlying commands

| Echo command | Subprocess                                                     |
|--------------|----------------------------------------------------------------|
| `bash`       | `<compose> exec <odooContainer> bash`                          |
| `psql`       | `<compose> exec <dbContainer> psql -U <POSTGRES_USER> <db>`    |
| `shell`      | `<compose> exec <odooContainer> odoo shell -d <db> --db_host=<dbContainer> --db_port=<port> --db_user=<user> --db_password=<pass> --no-http` |

`shell` reuses the same explicit connection flags as the module commands
(`buildConn` pattern) so it bypasses the Odoo image entrypoint and
connects directly. `--no-http` keeps it from binding the HTTP port.

If the Odoo container doesn't have `bash`, the user will see the
container's own error (`exec: "bash": not found`) — Echo doesn't try to
fall back to `sh`. Keeping the failure visible is preferable to a silent
substitution that hides which image was used.

### Exit handling

The TTY pass-through inherits SIGINT through the process group (same
pattern as `docker.LogsFollow`), so `Ctrl+C` inside the shell goes to
the shell itself. When the shell exits, the subprocess returns
normally. A non-zero exit prints `✗ <name> failed: …`, zero exits
return without a result line (the user already saw the shell session,
no need to clutter).

## Implementation

### `internal/docker/shell.go` — new file

A single helper that wraps `compose exec` with full TTY pass-through:

```go
// ExecInteractive runs `<compose> exec <container> <argv...>` with
// stdin/stdout/stderr attached to the current TTY. SIGINT is consumed
// in the parent so the subprocess (in the same process group) handles
// the interrupt and exits cleanly — same pattern as LogsFollow.
func ExecInteractive(ctx context.Context, composeCmd, dir, container string, argv []string) error
```

Note: drop the `-T` flag used by `Exec` because we *want* a TTY.

### `internal/cmd/shell.go` — new file

```go
type ShellOpts struct {
    Cfg     *config.Config
    Root    string
    Args    []string
    Palette theme.Palette
}

func RunBash(ctx context.Context, opts ShellOpts) error
func RunPsql(ctx context.Context, opts ShellOpts) error
func RunOdooShell(ctx context.Context, opts ShellOpts) error
```

Each one:

1. Checks `requireOdooConfig` (or `requireDBContainer` for `psql`).
2. If `cfg.Stage == "prod"` and `--force` isn't in `opts.Args`, opens
   a `huh.Confirm` (same `confirmDrop`-style helper, generalised to
   take a label + a red-rendered target). Cancellation returns
   `ErrCancelled`.
3. Builds the argv:
   - `bash`: `[]string{"bash"}`, container = `cfg.OdooContainer`.
   - `psql`: `[]string{"psql", "-U", envVars["POSTGRES_USER"], cfg.DBName}`, container = `cfg.DBContainer`.
   - `shell`: `[]string{"odoo", "shell", "-d", cfg.DBName, "--db_host="+cfg.DBContainer, "--db_port="+envVars["POSTGRES_PORT"], "--db_user="+envVars["POSTGRES_USER"], "--db_password="+envVars["POSTGRES_PASSWORD"], "--no-http"}`, container = `cfg.OdooContainer`.
4. Calls `docker.ExecInteractive` and returns its error.

A small helper `confirmProd(palette, action, db string) error` reuses
the same form skeleton as `confirmDrop`, with the message:

```
⚠  Opening <action> against prod database "<db>".
   This will run against production data.
```

### `internal/repl/repl.go` — dispatch

Extend the dispatch switch:

```go
case "shell", "bash", "psql":
    sess.runShell(ctx, cmd, args)
```

`runShell` prints the `$ <name>` info line and then calls the right
`cmd.Run*` directly — no `logColorer`, no `runStats`, no `finalize`
(the user just had a full TTY session; the result line would be
redundant). Only print `✗ <name> failed: <err>` when the subprocess
returns non-zero or the prod-confirm returns `ErrCancelled` →
warn line.

Add a "Shell" section to `runHelp` between "Database" and "Docker":

```
Shell
  bash                    Bash session inside the Odoo container
  psql                    PostgreSQL client against the configured DB
  shell                   Odoo Python shell loaded against the configured DB
    --force               Skip the prod-stage confirmation prompt
```

## Dependencies

None new. Reuses `internal/docker` (new `ExecInteractive`),
`internal/cmd/init.go` (`ErrCancelled`, `BuildHuhTheme`), `huh` for the
prod-confirm prompt, and the existing `.env` loader.

## Verify when done

- [ ] `bash` opens a working shell inside the Odoo container; `exit` returns to the Echo prompt cleanly.
- [ ] `psql` opens a `psql` session against `cfg.DBName` using the role from `.env`; `\q` returns to the Echo prompt cleanly.
- [ ] `shell` opens the Odoo Python shell with the ORM ready against `cfg.DBName`; `exit()` / `Ctrl+D` returns cleanly.
- [ ] `Ctrl+C` inside the shell stops the subprocess (not Echo) and the prompt resumes.
- [ ] When `cfg.Stage == "prod"`, each command prompts for confirmation and aborts on `No`/`Esc`.
- [ ] `--force` skips the prod confirmation.
- [ ] When `cfg.Stage != "prod"`, no confirmation is shown.
- [ ] `go build ./...` and `go vet ./...` pass.
- [ ] Help shows the new Shell section.
