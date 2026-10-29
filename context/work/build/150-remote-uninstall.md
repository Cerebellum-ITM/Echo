# Unit 150 · remote-uninstall
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-29.

## Goal

`uninstall <mod...> --from <target> | --remote` uninstalls modules on a remote target over SSH, following the
remote `update` path: same target resolution, picker sources, prod gate and Relay support, with no local compose
project needed. `uninstall sale_custom --from staging --json` prints the same outcome object as a local run.

## Design

- **Routing mirrors `RunUpdate`.** `RunUninstall` checks `remoteFlagsIn(opts.Args)` before `requireOdooConfig`, so
  a pure addons repo works ([modules-and-odoo](../../knowledge/modules-and-odoo.md#install--update--uninstall--test)).
  The local path is unchanged.
- **Same mechanism as local.** The run is `odoo shell` (with `--log-level` from `--level`) in the remote Odoo
  container, fed `odoo.UninstallScript(modules)` on stdin: `remoteContainerCmd(rsc.remotePath, rsc.target,
  argv)` through `runSSHStream`, the transport remote `shell-run` uses. A module not installed in the remote DB
  still aborts the whole run before anything is removed.
- **Module resolution mirrors remote `update`.** Explicit names; otherwise a picker titled
  `Modules to uninstall on <target>`, tinted by the remote stage, over `remoteUpdateCandidates` (the remote's own
  addons, or with `--installed` every module installed in the remote DB). An empty list is
  `ErrNoModulesAvailable`; Esc or an empty confirm is `ErrCancelled`. The picker fails closed without a TTY.
- **Prod gate.** `confirmRemoteProd(opts.Palette, "uninstall", rsc, opts.Args)` after resolution: a red confirm on
  a remote `stage=prod` target, `--force` skips it, non-TTY exits 2. Same as `update`.
- **Flags accepted remotely:** `--from <t>` / `--from=<t>`, `--remote`, `-E`/`--env` (consumed like `update`),
  `--installed`, `--force`, `--level`, `--json`. `--all`, `--last`, `--i18n`, `--no-checkpoint` and any other flag
  are `unknown flag` usage errors (exit 2) before any SSH. Locally `--installed` and `--force` stay unknown flags.
- **Reverb.** No delegation: Reverb has no uninstall job, so a Reverb environment runs the classic
  `compose exec` path, as `update --i18n` does.
- **Relay.** `uninstall` joins `relaySupported`: it runs in the env's Odoo container through `docker exec`, the
  same path `shell-run` already uses on a Relay target.
- **JSON and history.** Nothing new: `resolveRemoteShell` reports the target through `OnRemoteResolved`, so the
  `--json` object and the run record name the remote target, as for `update`.

## Implementation

### `internal/cmd/uninstall_remote.go` (new)

- `runUninstallRemote(ctx, opts ModulesOpts, from string) ([]string, error)`: `extractLevel`,
  `parseRemoteUninstallFlags`, `resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, nil)`, picker
  when no names, `confirmRemoteProd`, `emitResolved(opts, modules)`, then
  `runSSHStream(ctx, rsc.sshHost, remoteContainerCmd(rsc.remotePath, rsc.target, odoo.WithLogLevel(odoo.Shell(rsc.conn), level)), []byte(odoo.UninstallScript(modules)), opts.StreamOut)`.
  Returns the modules with the run error.
- `parseRemoteUninstallFlags(rest []string) (installed bool, modules []string, err error)`: the switch of
  `parseRemoteUpdateFlags` reduced to the accepted flags above.

### `internal/cmd/modules.go`

- `RunUninstall` branches to `runUninstallRemote` first, like `RunUpdate` (same comment shape).

### `internal/cmd/relay.go`

- Add `"uninstall": true` to `relaySupported`.

### `internal/repl/commands.go`, `internal/repl/helpdocs.go`

- `commandFlags["uninstall"]`: add `--installed`, `--from`, `--remote`, `--force`, `-E`, `--env`.
- Help entry: usage `uninstall <mod...> [--installed] [--level <lvl>] [--from <target> | --remote] [--force] [--json]`;
  flags `--from`, `--remote`, `--force`, `--installed` with the wording of `update`'s; the "Local only" note
  becomes "Destructive: the modules' data is removed from the database"; add the remote note ("Remote runs the
  uninstall script through odoo shell in the target's running container over SSH and needs no local compose
  project") and the prod-confirm note; add the example `uninstall sale_custom --from staging --json`.

### Docs

- `CHANGELOG.md` `[Unreleased]` → `Added`: remote `uninstall`.
- `context/knowledge/modules-and-odoo.md`: the remote line names `uninstall` with `test` and `update`; the
  prod-gate line too.
- `README.md`: if it documents `uninstall`, add the remote form next to it.

## Dependencies

- none

## Verify when done

- [ ] `parseRemoteUninstallFlags` table test: names, `--installed`, `--from` value not a module, `--from=`/`--remote`/`--force`/`--json` consumed, `--all`/`--last`/`--i18n`/unknown rejected with `ErrUsage`.
- [ ] `RelayGuard` lets `uninstall --from <relay target>` through (test next to the existing Relay guard tests).
- [ ] `echo_cli help uninstall` shows the remote flags and the example; local `uninstall --installed` still exits 2.
- [ ] Live (only with the user's go, it writes): install then uninstall a throwaway module on `develop` with `--from`, and `--json` names the target.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass.
