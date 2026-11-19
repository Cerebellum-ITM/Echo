# Unit 152 · relay-adhoc-target
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-11-19.

## Goal

A Relay env is reachable without registering it: `--from relay:<app>/<env>` works on every command a Relay target
supports, and the SSH alias of its receiver comes from a per-server map, `[relay.ssh_hosts]`. An agent on a dev env
that lives a few hours runs `echo_cli probe ... --from relay:habitta/ui` and leaves nothing behind in `global.toml`.
A receiver whose SSH port is not 22 and has no alias is an error that names the fix, not a warning followed by a
failing `ssh`. Registered targets and every non-Relay target keep working as they do.

Asked by [REQUEST-echo-relay-dev-envs](../../messages/REQUEST-echo-relay-dev-envs.md).

## Design

- **The reference is the path Echo already uses in memory.** A Relay target's `RemotePath` is
  `relay:<app>/<env>` (`config.RelayPath`). An ad-hoc `--from` value with that prefix (`config.RelaySpec(from)`)
  names the env directly: `resolvePullRemote` returns an empty SSH host and the value as `remotePath`, validated
  with `parseRelaySpec` (a malformed spec is `ErrUsage`). Nothing is saved. A registered target cannot be named
  `relay:...` in practice, and a name match is tried first, so no existing target changes meaning.
- **`fromName` stays the reference** (`relay:habitta/ui`), so status lines, `--json` `target` fields and run
  records name the env.
- **The SSH host is chosen in one place**, `relaySSHHost(cfg, explicit string, re relay.Env) (string, error)`, in
  this order:
  1. `explicit` when not empty (a registered target's `ssh_host`, or `--ssh-host` at registration);
  2. `cfg.RelaySSHHosts[re.Server]` (the map, keyed by the server name the resolve reports);
  3. `re.SSH.Host` when `re.SSH.Port` is 0 or 22;
  4. otherwise `ErrUsage`: `the receiver <server> listens for SSH on port <port> and Echo passes no port — add
     <server> = "<an alias from ~/.ssh/config>" under [relay.ssh_hosts] in global.toml, or register the target
     with link --add <name> --relay <app>/<env> --ssh-host <alias>`.
  `resolveRelayShell` and `runLinkAddRelay` both use it, replacing their `if sshHost == ""` blocks. The WARNING
  `the receiver's SSH port is not 22 and a bare host carries no port` is deleted: rule 4 replaces it.
- **Echo still has one SSH-configuration channel.** The map holds aliases of `~/.ssh/config`, never a port, user
  or key ([decisions](../../decisions.md#remote-targets)).
- **`[relay.ssh_hosts]` is global-only**, a TOML table of `server = "alias"` under `[relay]`. It is modeled in
  `relayConfig`, so every writer of `global.toml` keeps it.
- **Registration remembers the alias.** After `link --add <name> --relay <app>/<env> --ssh-host <alias>` saves the
  target, Echo saves `<server> = "<alias>"` into `[relay.ssh_hosts]` when the map has no entry for that server
  (an existing entry is never overwritten), and logs one INFO `echo.relay` line `remembered the receiver's SSH
  alias` with `server` and `host`. A failure to save it is a WARNING; the registration stands.
- **The guard knows the ad-hoc form.** `relayTargetFor(cfg, from)` returns `ok` for a `from` that is a Relay
  reference, so `push --from relay:habitta/ui` is refused at dispatch like on a registered target.
- **`link relay:<app>/<env>` is not a binding form.** Binding needs a named target; `link` with that positional
  keeps failing as an unknown target. Its error gains the hint `register it first: link --add <name> --relay
  <app>/<env>` when the name is a Relay reference.
- **Every remote parser already consumes the `--from` value**; no parser changes.

## Implementation

### `internal/config/config.go`

- `relayConfig`: add `SSHHosts map[string]string \`toml:"ssh_hosts,omitempty"\``.
- `Config`: add `RelaySSHHosts map[string]string`; `applyRelay` copies it.
- `SaveRelaySSHHost(server, alias string) (saved bool, err error)`: load-modify-write of `global.toml` in the same
  style as `SaveConnectTarget`; sets the entry only when absent and reports whether it wrote.
- Tests: a `global.toml` with `[relay.ssh_hosts]` loads into `RelaySSHHosts`; a save of an unrelated connect target
  leaves the table byte-identical; `SaveRelaySSHHost` adds, and does not overwrite.

### `internal/cmd/i18n_pull.go`

- `resolvePullRemote`: when `from` matches no target and `config.RelaySpec(from)` matches, validate with
  `parseRelaySpec` and return `"", from, nil`. The `unknown connect target` error stays for everything else.

### `internal/cmd/relay.go`

- `relaySSHHost` as designed; used by `resolveRelayShell` and `runLinkAddRelay`.
- `runLinkAddRelay`: after `SaveConnectTarget`, when `--ssh-host` was passed, call `config.SaveRelaySSHHost` and
  log as designed; mirror the entry into `opts.Cfg.RelaySSHHosts`.
- `relayTargetFor`: a Relay-reference `from` returns `"", from, from, true`.

### `internal/cmd/link.go`

- The unknown-target error of a positional that is a Relay reference gains the hint.

### `internal/cmd/doctor.go` / `doctor_relay.go`

- Nothing new if `runDoctorRelay` (Unit 151) passes its host through `resolveRelayShell`; check that
  `doctor --from relay:<app>/<env>` reports `target.host` as the host `relaySSHHost` chose.
- `sameHostTargets` and `resolveGitDeploy` are classic-only; make sure the Relay branch runs before they see an
  empty host.

### `internal/repl/helpdocs.go`

- `link`: usage and notes gain the ad-hoc form (`--from relay:<app>/<env>` reaches an env without registering it)
  and `[relay.ssh_hosts]`; the `--ssh-host` flag text says the alias is remembered for the receiver.
- `help agents` (the headless contract): one line on `--from relay:<app>/<env>`.
- An example on `probe`: `echo_cli probe -c "env['res.partner'].search_count([])" --from relay:habitta/ui --json`.

### Docs

- `CHANGELOG.md` `[Unreleased]` → `Added`: the ad-hoc Relay reference and `[relay.ssh_hosts]`; `Changed`: a
  receiver on a non-22 SSH port without an alias is now an error at registration and at resolve.
- `context/knowledge/remote-targets.md`, "Relay targets": the ad-hoc form, the host order of `relaySSHHost`, the
  map. `context/decisions.md`: one entry, "A Relay env is reachable ad hoc; the receiver's SSH alias is a
  per-server map" (why: dev envs are short-lived, a target per fork does not scale; rejected: a `--relay` flag on
  every command, a target that follows the current branch, a port field in Echo).
- `README.md`: next to the Relay target section if it has one.

## Dependencies

- Unit 151, for `doctor` on a Relay env (the ad-hoc form must work with it).

## Verify when done

- [ ] `relaySSHHost` table test: explicit wins; map entry wins over the resolve host; port 22 and port 0 fall back
  to the resolve host; port 1024 with no alias is `ErrUsage` and the message names the server and both fixes.
- [ ] `resolvePullRemote`: `relay:habitta/ui` → empty host and that path; `relay:bad` → `ErrUsage`; a registered
  name still resolves to its target; an unknown plain name is still `unknown connect target`.
- [ ] A `shell-run` (or `probe`) test with `--from relay:<app>/<env>` and a `[relay.ssh_hosts]` entry reaches the
  mapped alias with `docker exec -i`, and writes nothing to `global.toml`.
- [ ] `RelayGuard` refuses `push --from relay:habitta/ui` and lets `probe --from relay:habitta/ui` through.
- [ ] `link --add <name> --relay <app>/<env>` on a port-1024 receiver without `--ssh-host` and without a map entry
  exits 2 and saves nothing; with `--ssh-host` it saves the target and the map entry; a second registration on the
  same server with another alias leaves the map entry as it was.
- [ ] Every existing Relay and connect-target test passes untouched, except the one asserting the deleted WARNING.
- [ ] Live (reads only): `ps` and `probe` with `--from relay:fb/stage` — run by the orchestrator.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass.
