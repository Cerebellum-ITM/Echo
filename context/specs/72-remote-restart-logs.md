# Unit 72: remote `restart` + remote `logs` — `--from <target>` / `--remote`

## Goal

Extend the two compose verbs that today only ever touch the **local**
stack — `restart` and `logs` — so they can also act on a **remote** Odoo
host, reusing the exact transport `deploy` / `shell` already use. No new
SSH machinery: `restart --from <target>` / `restart --remote` and
`logs --from <target>` / `logs --remote` resolve the same connect target,
build a `remoteComposeCmd`, and stream the output line by line through
`runSSHStream` into Echo's Odoo-styled renderer.

```
restart --from prod            # restart the remote profile's Odoo container
restart web --remote           # restart the `web` service on this dir's link binding
restart --from prod --force    # skip the prod confirmation
logs --from prod               # follow the remote Odoo logs over SSH
logs --from prod -t 200        # last 200 lines, then follow
logs --remote --no-follow      # bounded remote logs (no follow)
```

Without a remote flag, both commands behave **exactly as today** (local
compose against `opts.Root`). This unit adds the remote branch only.

## Design

**Remote resolution — shared, not reinvented.** Both commands adopt the
`--from <name>` / `--from=<name>` / `--remote` convention already used by
`shell` / `shell-run` / `deploy` (Units 60/62). `--from <name>` names a
global connect target (implies remote); bare `--remote` uses the
resolution chain without a name (project / `link` `[connect]` → global
targets fallback: one auto, several picker, none → clean error). The
shared `resolveRemoteTarget(cfg, palette, from, log)` and the richer
`resolveRemoteShell(...)` (which also fetches the remote Echo profile —
`composeCmd`, `OdooContainer`, `DBContainer`, stage) are reused verbatim.
The remote `compose` flavor and the Odoo container/service name come from
the **remote** profile, never from the local config.

**Transport.** Remote runs go through `remoteComposeCmd(remotePath,
target.composeCmd, <verb>, args...)` + `runSSHStream(ctx, sshHost,
remoteCmd, nil, StreamOut)` — the same primitives `deploy`'s
`stop`/`up -d` steps use. Output streams live (invariant 2: stream, never
buffer-and-dump) through the existing `emitStreamLine` / `logColorer`
wrap, so remote container progress and Odoo logs colorize identically to
local ones (Units 08/20).

**`restart` remote path.**
- New flags parsed off `opts.Args`: `--from <v>` / `--from=<v>` /
  `--remote` (consumed so they're not mistaken for service names). The
  remaining positionals are the compose **service** names.
- **Default target (no service):** restart the **remote profile's Odoo
  container** (`prof.OdooContainer`), symmetric with `logs`' local
  default. Explicit service args restart exactly those.
- **Prod gate:** when the **remote** profile's stage is `prod`, gate on
  `confirmRemoteProd(palette, "restart", rsc, args)` (`--force` bypass,
  non-TTY fails closed). This is intentionally stricter than the local
  `restart`, which never gates — a remote prod restart is a real
  outage-causing action. The local `restart` path is unchanged.
- Remote command: `remoteComposeCmd(remotePath, target.composeCmd,
  "restart", services...)`. Health invalidation (`sess.prompt.health`)
  applies to the **local** prompt only; a remote restart does not touch
  local health, so the existing `Invalidate()` call is skipped on the
  remote branch.

**`logs` remote path.**
- Same flag parsing prepended to the existing `RunLogs` flag loop
  (`-f/--follow`, `--no-follow`, `-c/--copy`, `--all`, `-t/--tail N`,
  service positionals). The remote flags are stripped first so they don't
  land in `services`.
- **Follow stays the default** (decision): a remote `logs` with no
  `--no-follow`/`--copy` streams live over SSH until the user interrupts
  (Ctrl+C ends the `ssh` subprocess) or the connection closes. Unlike the
  local follow it does **not** allocate a container TTY — it's a plain
  `runSSHStream` over the long-running `compose logs -f`, which is exactly
  how `deploy` already streams a long remote run.
- Remote command built from the same pieces as `docker.Logs` /
  `docker.LogsFollow`: `remoteComposeCmd(remotePath, target.composeCmd,
  "logs", "--no-log-prefix", ["-f"]?, ["--tail", tail]?, services...)`.
  When no service and not `--all`, default to the **remote** profile's
  `OdooContainer` (mirror of the local default at `docker.go:122`).
- `--copy` forces bounded mode (`follow=false`) and copies the captured
  remote output to the local clipboard via the existing `runLogsAndCopy`
  buffering wrapper — the buffer just wraps the remote `StreamOut`
  instead of the local one. `logs` is read-only, so **no prod gate**.

**Projectless.** Both commands become a `projectlessOneShot` **only when
a remote flag is present** (so `restart --from prod` / `logs --remote`
work from a pure addons repo with no `docker-compose.yml`, like
`shell --remote` in Unit 62). A local `restart` / `logs` outside a
compose project keeps failing with the same "not inside a project" error
as today.

## Implementation

### `internal/cmd/docker.go`
- Add remote-flag parsing helpers shared by both verbs (or reuse the
  `shell_remote.go` parser): extract `from string, remote bool` and the
  cleaned positional args.
- `RunRestart`: when remote, resolve via `resolveRemoteShell(...)`, run
  `confirmRemoteProd(palette, "restart", rsc, opts.Args)`, default the
  service list to `[rsc.target.odooContainer]` when empty, then
  `runSSHStream(ctx, rsc.sshHost, remoteComposeCmd(rsc.remotePath,
  rsc.target.composeCmd, "restart", services...), nil, opts.StreamOut)`.
  Local branch unchanged.
- `RunLogs`: strip remote flags first; when remote, resolve target,
  default the service to the remote `OdooContainer`, and dispatch to a
  remote analog of `docker.Logs` / `docker.LogsFollow` / `runLogsAndCopy`
  built on `remoteComposeCmd` + `runSSHStream`. Local branch unchanged.

### `internal/cmd/remote.go` (or `shell_remote.go`)
- Reuse `resolveRemoteTarget` / `resolveRemoteShell` / `confirmRemoteProd`
  / `remoteComposeCmd` / `runSSHStream` as-is. If a `composeCmd`-with-args
  remote logs string needs `--no-log-prefix` + `--tail`, build the arg
  slice and pass through `remoteComposeCmd` (every arg is shell-quoted;
  the compose command stays raw so `docker compose` keeps its two
  tokens).

### `internal/repl/repl.go` / `commands.go`
- Register `--from`, `--remote` (and for `logs`, confirm `-f`/`--follow`/
  `--no-follow`/`-t`/`--tail`/`-c`/`--copy`/`--all`) under `restart` and
  `logs` in the `commandFlags` registry so highlight + Tab-complete
  (Units 21/24) know them.
- `runDocker`: the remote branch must skip `sess.prompt.health.Invalidate()`
  for `restart` (local health is irrelevant to a remote run). Pass
  `Palette` + a `Log` callback (`sess.cmdOdooLogger(name)`) into
  `DockerOpts` so `target resolved` / `system` lines render like the
  other remote commands.
- Make `restart` / `logs` eligible for the projectless one-shot path when
  a remote flag is present (same hook `shell`/`shell-run` use in Unit 62).
- Help text (`helpSections`): add `--from <target>` / `--remote` subflag
  lines under `restart` and `logs`, mirroring the `shell` entries.

### `internal/cmd/docker.go` — DockerOpts
- `DockerOpts` already carries `Cfg`, `Root`, `Args`, `Palette`,
  `StreamOut`. Add a `Log func(level, sub, msg, db string, fields
  ...[2]string)` field (like `DeployOpts.Log`) so the remote branch can
  emit `target resolved` / `system` progress lines; nil-safe (no-op when
  unset, preserving the local path's silence).

## Dependencies

- **Unit 60 (remote-link)** — `runSSHStream`, `remoteComposeCmd`,
  `link` `[connect]` binding, target resolution chain.
- **Unit 62 (remote-shell)** — `resolveRemoteTarget` /
  `resolveRemoteShell` / `confirmRemoteProd` and the
  projectless-when-remote pattern this unit copies.

No new Go packages.

## Verify when done

- `restart --from <t>` restarts the remote Odoo container (verified via a
  remote `ps`/health), streaming the compose progress lines Odoo-styled;
  passing a service name restarts that service instead.
- `restart --from <t>` against a `stage=prod` remote prompts a red
  confirm; `--force` skips it; declining cancels cleanly
  (`echo.restart.cancelled`).
- `logs --from <t>` follows the remote Odoo logs live over SSH; `-t N`
  bounds the initial tail; `--no-follow` and `--copy` produce bounded
  output, with `--copy` landing the text on the local clipboard.
- A local `restart` / `logs` (no remote flag) behaves byte-for-byte as
  before — no prod gate on local `restart`, follow default on local
  `logs`.
- `restart --from <t>` / `logs --remote` work from an addons repo with no
  `docker-compose.yml`; a local invocation there still errors "not inside
  a project".
- `restart` / `logs` highlight `--from` / `--remote` as known flags and
  Tab-completes them.
- `go build ./...`, `go vet ./...`, and `go test ./...` pass; new tests
  cover the remote-flag parsing and the remote command-string assembly
  for both verbs (table-driven, hooking `sshStreamCommand` like the
  existing remote tests).
