# Unit 151 · relay-probe-doctor
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-11-19.

## Goal

`probe` and `doctor` run on a Relay env, so the `odoo-probe` skill's two base steps work there unchanged: reads go
through `probe` (a Postgres `READ ONLY` transaction) instead of a rolled-back `shell-run`, and
`doctor --from <relay target> --json` reports the gate stage of a target the directory is not bound to. Every
non-Relay target behaves exactly as before.

Asked by [REQUEST-echo-relay-dev-envs](../../messages/REQUEST-echo-relay-dev-envs.md).

## Design

- **`probe` only needs the guard opened.** Its remote path already builds the command with
  `remoteContainerCmd(rsc.remotePath, rsc.target, odoo.ProbeShell(rsc.conn))` (`internal/cmd/probe.go:284`), and
  `remoteContainerCmd` emits `docker exec -i <container>` when `target.odooExec == dbExecDocker`
  (`internal/cmd/remote.go`), which `resolveRelayShell` sets. The `odoo.Conn` of a Relay env carries only `-d` and
  `--db_user`, as for `shell-run`. `probe` joins `relaySupported`; no other change to `probe.go`.
- **`doctor` gets a Relay branch with its own, smaller check list.** The classic checks read a server profile, a
  project directory, rsync, git-deploy, the deploy lock and a push destination; a Relay env has none of them.
  `RunDoctor` branches right after `resolveRemoteTarget`, when `config.RelaySpec(remotePath)` matches, into
  `runDoctorRelay`. The classic flow is not touched.
- **Relay checks, in order, none stopping the next unless stated:**

  | ID | ok | failed / warn |
  |---|---|---|
  | `relay` | the resolve answered. Message `relay env resolved`; fields `env`, `server`, `branch`, and `release` (deploy id) + `sha` (short) when the env has a release | failed: the resolve error (through `relayError`), or Relay not configured. Then `ssh` and `containers` are `skipped reason=unresolved` and the result is returned. warn: no release yet (`message` = `the env has no release yet`) |
  | `ssh` | the batch below ran. Message `reachable`; field `host` | failed: `ssh` missing on PATH, or the batch failed (message = the SSH error's text). Then `containers` is `skipped reason=unreachable` |
  | `containers` | both containers report `running=true`. Message `running`; fields `odoo`, `db` (container names) | failed: a container is missing or not running. Message names each one: `<name> is not running` / `<name> does not exist` |

- **One SSH batch**, through the existing seam `doctorRunSSH` with `doctorBatchTimeout`:
  `docker inspect -f '{{.Name}} {{.State.Running}}' <odoo> <db> 2>&1; true`. Container names are shell-quoted with
  `shellQuote`. A line `/<name> true` is running, `/<name> false` is stopped, a name with no such line does not
  exist. The trailing `true` keeps a missing container from reading as a transport failure.
- **Result shape is unchanged** (`DoctorResult`): `target.name` = the display name the classic flow uses
  (`statusProjectName`), `target.host` = the SSH host in use, `target.path` = the `relay:<app>/<env>` path,
  `target.stage` / `target.stage_declared` = the gate stage from `remoteConnectTarget` (an undeclared stage reads as
  `prod`, as everywhere), `target.db` = the resolved database. Exit rule unchanged: 1 when a check failed, 0 with
  warnings only (`finishDoctor`).
- **`resolveRelayShell` is called with a nil log** so `doctor` prints only its own `echo.doctor` lines; the target
  line the classic flow logs (`target`, with `target`, `host`, `path`) is logged the same way after the resolve.
- **Guard.** `doctor` moves from `relayAlwaysRemote` to `relaySupported`. `probe` joins `relaySupported`.
- **No new flags.** `parseDoctorArgs` and the probe parser are unchanged.

## Implementation

### `internal/cmd/relay.go`

- `relaySupported`: add `"probe": true` and `"doctor": true`. `relayAlwaysRemote`: remove `"doctor"`.

### `internal/cmd/doctor_relay.go` (new)

- `runDoctorRelay(ctx context.Context, opts DoctorOpts, host, remotePath, name string) (DoctorResult, error)`:
  resolve with `resolveRelayShell(ctx, opts.Cfg, host, remotePath, name, nil)`; on error emit the failed `relay`
  check and the two skipped ones; otherwise fill `DoctorTarget`, emit `relay`, check `lookPath("ssh")`, run the
  batch, emit `ssh` and `containers`. Returns `finishDoctor(opts, res), nil`.
- `doctorRelayScript(odoo, db string) string` and `evalRelayContainers(out string, odoo, db string) DoctorCheck`:
  the batch and its parser, separate so the parser has a table test.
- The `emit`/skip closures of `RunDoctor` are not exported today: either lift them into a small unexported helper
  both flows share, or repeat the few lines in this file. Do not change what the classic flow logs.

### `internal/cmd/doctor.go`

- `RunDoctor`: after `resolveRemoteTarget` and the `name == ""` fallback, `if _, ok := config.RelaySpec(remotePath);
  ok { return runDoctorRelay(ctx, opts, host, remotePath, name) }`.

### `internal/repl/helpdocs.go`

- `doctor`: one note, `On a Relay env the checks are relay (the resolve), ssh and containers; a Relay env has no
  server profile, rsync, git deploy, lock or push destination to check`.
- `link`: in the Relay note, add `probe` and `doctor` to the list of commands that run on a Relay target.
- `probe`: one note, `Runs on a Relay env through docker exec in its Odoo container`.

### Docs

- `CHANGELOG.md` `[Unreleased]` → `Added`: `probe` and `doctor` on Relay envs.
- `context/knowledge/remote-targets.md`, "Relay targets": the supported list gains `probe` and `doctor`, and one
  bullet says what `doctor` checks on a Relay env and why the classic checks do not apply. Update the file's
  `Last verified` date.
- `README.md`: if it lists the commands supported on a Relay target, add both.

## Dependencies

- none (Unit 149 is released).

## Verify when done

- [ ] `RelayGuard` lets `probe --from <relay target>`, `probe --remote` on a Relay binding, and `doctor --from
  <relay target>` through; `push --from <relay target>` is still refused (tests next to the existing guard tests).
- [ ] A probe test on a Relay target (fake Relay resolve + the SSH seam the existing Relay tests use) shows the
  remote command is `docker exec -i <odoo container> ...` with no `cd` and no compose.
- [ ] `evalRelayContainers` table test: both running, odoo stopped, db missing, empty output.
- [ ] `runDoctorRelay` tests: happy path (three `ok`, `target.stage` and `target.db` filled, exit 0), resolve
  refused (`relay` failed, two skipped), SSH batch failing (`ssh` failed, `containers` skipped).
- [ ] `doctor --from <classic target>` output is byte-identical to before (the existing doctor tests pass untouched).
- [ ] `echo_cli help doctor`, `help probe` and `help link` show the new notes.
- [ ] Live (reads only): `probe -c "env['res.partner'].search_count([])" --from fb-stage --json` and
  `doctor --from fb-stage --json` — run by the orchestrator, not by the implementer.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass.
