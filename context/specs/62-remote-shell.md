# Unit 62: remote Odoo shell — `shell --from/--remote` + `shell-run --from/--remote`

## Goal

Run things through a **remote** instance's Odoo shell, reusing the Unit 60
transport: `shell --from <target>` (or `--remote` for the directory's
`link` binding) opens an interactive Odoo Python shell on the remote host
over `ssh -tt`, with the same PTY capture and startup-log colorizing the
local `shell` has; `shell-run <file> --from <target>` pipes a **local**
`.py` through the remote Odoo shell (`runSSHStream` with the script as
stdin), streaming the output and auto-copying the script's prints exactly
like the local run.

```
shell --from prod              # interactive remote Odoo shell
shell --remote                 # same, against this directory's link binding
shell-run fix_taxes.py --from prod    # run a local .py on the remote
shell-run --remote             # picker over local .py, run on the link's remote
```

## Design

**Remote resolution — shared with deploy.** `--from <target>` names a
global connect target; `--remote` uses the resolution chain without a
name (project/link `[connect]` → global targets fallback: one auto,
several picker, none → clean error). `deploy`'s `resolveDeployRemote` is
refactored into a shared `resolveRemoteTarget(cfg, palette, from, log)`
in `internal/cmd` that deploy, shell and shell-run all call. `--from`
implies remote; bare `--remote` is the explicit switch for the binding.
Without either flag both commands behave exactly as today (local).

**Remote container/db mapping** comes from the server's Echo profile
(`fetchRemoteProfile` → `connectTarget`), credentials from the remote
`.env` (`remotePullEnv`) — the same recipe as `i18n-pull`/`deploy`. The
Odoo argv is the existing `odoo.Shell(conn)` builder. A remote stage
`prod` gates on `confirmProd` (bypassed by `--force`), keyed off the
**remote** profile's stage, not the local config.

**`shell-run` remote path.** `ShellScriptOpts` gains `From string` /
`Remote bool` (parsed by the REPL, which today swallows unknown flags —
it now consumes `--from <v>`/`--from=`/`--remote` so the value is not
mistaken for a script name). When remote, `RunShellScript`:

1. resolves the target + profile + conn,
2. reads the local script file,
3. `runSSHStream(ctx, host, remoteContainerCmd(path, target, odoo.Shell(conn)), script, StreamOut)`.

The remote `compose exec -T` gets the script on stdin through ssh — the
exact remote analog of `docker.ExecWithStdin`. Output lines flow through
the same `emitStreamLine` wrap, so the auto-copy filter
(`scriptOutputLines`: drop Odoo-format log lines, keep prints) works
unchanged. Progress (`target resolved`, system-status line) is emitted
via the shared `Log` callback under `echo.shell-run`.

**`shell` remote path.** The PTY machinery in `docker.ExecInteractive`
(raw mode, SIGWINCH, ETX detection, tee-capture, `LineTransform`) is
extracted into an exported `docker.RunInteractive(ctx, argv, dir,
transform)`; `ExecInteractive` becomes a thin wrapper that prepends
`<compose> exec <container>`. The remote shell then runs
`docker.RunInteractive(ctx, ["ssh","-tt",host, remoteCmd], "", transform)`
— `-tt` forces a remote TTY through the PTY chain so the in-container
shell is fully interactive, and the local PTY tee keeps capture +
startup-log colorizing identical to the local `shell`. `RunOdooShell`
parses `--from`/`--remote` from `opts.Args` and branches; the REPL wiring
is untouched apart from flag registration.

**Projectless.** Both commands become `projectlessOneShot` **only when
the remote flags are present** — `projectlessOneShot` gains the args to
check. A local `shell`/`shell-run` outside a compose project keeps
failing with "not inside a project" as before.

**Non-TTY.** `shell-run` remote is non-interactive end to end (script on
stdin) — works headless like the local one. Interactive `shell` over a
pipe falls into `RunInteractive`'s existing non-TTY fallback.

## Implementation

### `internal/cmd/deploy.go` → shared resolution

- Extract `resolveRemoteTarget(cfg *config.Config, palette theme.Palette,
  from string, log func(...)) (sshHost, remotePath, fromName string, err
  error)`; `deploy` keeps its behavior via the shared helper.

### `internal/docker/shell.go`

- `RunInteractive(ctx, argv []string, dir string, transform LineTransform)
  (string, bool, error)` — the existing `ExecInteractive` body with the
  argv passed in (empty dir → inherit cwd); `ExecInteractive` delegates.

### `internal/cmd/shellrun.go`

- `ShellScriptOpts` + `From`, `Remote`, `Log`.
- `RunShellScript`: local path unchanged; remote branch as designed
  (resolve → profile → conn → read script → `runSSHStream` with stdin).

### `internal/cmd/shell.go`

- `RunOdooShell`: parse `--from`/`--remote` from `opts.Args`; remote
  branch builds the ssh argv and calls `docker.RunInteractive`.

### `internal/repl/shellrun.go` + wiring

- Flag parsing consumes `--from <v>`/`--from=<v>`/`--remote`; passes them
  in `ShellScriptOpts` with `Log: sess.cmdOdooLogger("shell-run")`.
- `commandFlags["shell-run"]` += `--from`, `--remote`;
  `commandFlags["shell"]` = `--from`, `--remote`, `--force`; help entries
  for both; `projectlessOneShot(name, args)` in `main.go`.

## Dependencies

- Unit 60 (`runSSHStream`, target resolution, `link` binding).

## Verify when done

- [ ] `shell-run x.py --from <target>` runs the local script on the
      remote instance, streams Odoo-colored output, and auto-copies only
      the script's prints; `--no-copy` still opts out.
- [ ] `shell --from <target>` opens an interactive remote Odoo shell with
      colorized startup logs; exiting returns cleanly to the REPL prompt.
- [ ] `--remote` uses the directory's `link` binding; neither flag →
      both commands behave exactly as before (local).
- [ ] Remote stage `prod` asks for confirmation (`--force` skips); the
      gate uses the remote profile's stage.
- [ ] From a linked addons repo (no compose), `echo shell-run --remote x.py`
      works; without the remote flags the old "not inside a project"
      error is preserved.
- [ ] Flag parsing (`--from` value not mistaken for a script) and the
      shared `resolveRemoteTarget` are unit-tested.
- [ ] `go build/vet/test ./...` pass; registry/commandhl cross-checks stay
      green; `CHANGELOG.md` `[Unreleased]` gets an `Added` entry.
