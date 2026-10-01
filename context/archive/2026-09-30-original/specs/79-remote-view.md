# Unit 79: remote `view` — `--from <target>` / `--remote`

## Goal

Extend the `view` command (Unit 43) so it can browse and display a module
file that lives on a **remote** host — the deployed copy inside the
server's Odoo container — reusing the SSH transport of Units 60/62/72/75.
`view [<mod>] --from <target>` / `--remote` resolves the same connect
target as `deploy`/`test`/`logs`, lists the remote module's files with the
usual fuzzy pickers (which stay local UI), fetches the chosen file's
content over SSH, and displays it through the existing bat/internal path.
`--copy` and `--last` keep working against the remote source. Without a
remote flag, `view` behaves **exactly as today**.

## Design

**Same flag surface as every remote verb.** The remote branch is opt-in
via the shared `--from <name>` / `--from=<name>` / `--remote` convention
(`remoteFlagsIn`, `shell_remote.go`): `--from` names a global connect
target (implies remote); bare `--remote` walks the resolution chain (this
dir's `link` binding → global-targets fallback). Nothing new to learn.

**Resolution comes from the remote profile.** The remote branch delegates
to `resolveRemoteShell(...)` for target + profile (`composeCmd`,
`OdooContainer`, `AddonsPaths`, `stage`, `DBName`). No prod gate: `view`
is strictly read-only (`cat`/`find`/`test -f` only), same rationale as
remote `logs` (Unit 72).

**Two remote layouts, one probe order.** Remote deployments come in the
same two shapes as local ones, so `remoteModuleBase` mirrors `moduleBase`:

1. **Remote host filesystem first** (host-mode remote, the `deploy`
   assumption — code pulled server-side under `remotePath`): for each
   stored addons path `b`, probe
   `ssh <host> test -f <remotePath>/<b>/<mod>/__manifest__.py` via
   `runSSH`. Relative paths are joined under `remotePath`.
2. **Container fallback** (conf-mode remote, addons only inside the
   image): for each **absolute** addons path, probe
   `remoteContainerCmd(remotePath, target, odoo.Cmd{"test","-f",p})`.

The first hit wins and fixes the transport (`hostFS` vs `container`) for
the subsequent `find` (listing) and `cat` (read) of that invocation —
exactly the `inContainer` split `view` already has locally.

**Pickers stay local and identical.** Module picker (when no positional):
the local checkout's module list via `resolveModules`, same as remote
`test` (Unit 75) — the `link` binding ties this dir to the target. File
picker: title `"File in <mod>"`, fed by the remote `find` listing,
filtered through the existing `skipViewPath`. Display, `--copy`, the
`echo.view` log frame (`module=… file=… via=…`) and exit codes are all
unchanged — only the content source differs.

**`--last` replays the remote too.** `sess.lastViewModule/lastViewFile`
already exist; the session additionally remembers the last view's remote
flags so `view --last --copy` re-reads the same file from the same source.
Simplest true-to-behavior rule: `--last` re-runs with the remote flags
present in the *current* args (documented), falling back to the stored
ones when none are given.

**Flag stripping.** `--from <val>` / `--from=<val>` / `--remote` are
consumed off the args before the positional parse (today `RunView` errors
on unknown flags and would take the bare `--from` value as the module) —
same gap-closing as Unit 75.

## Implementation

### `internal/cmd/view_remote.go` — new file

- `type viewSource interface`? No — keep it concrete like the rest of the
  codebase. Add a `remoteView` struct wrapping `remoteShellContext` +
  the resolved `(base string, inContainer bool)`.
- `resolveRemoteView(ctx, opts ViewOpts, from string) (remoteView, error)`
  → `resolveRemoteShell` + `remoteModuleBase` probe order above.
- `remoteModuleFiles(ctx, rv, module) ([]string, error)` — `find <dir>
  -type f` over `runSSH` (plain SSH for hostFS, `remoteContainerCmd` for
  container), trimming to module-relative paths and filtering
  `skipViewPath`, sorted.
- `remoteReadModuleFile(ctx, rv, module, rel) (string, error)` — `cat`
  over the same transport (`shellQuote` the path).

### `internal/cmd/view.go` — branch `RunView` / `RunViewLast`

- Strip remote flags first: `from, remote := remoteFlagsIn(opts.Args)`
  and skip those tokens in the flag loop (including the value after a
  bare `--from`).
- When `from != "" || remote`: skip the local `ErrNoOdooContainer` guard
  (the remote profile provides the container), resolve the module (same
  picker), then use the `remote*` helpers for base/list/read. The local
  branch is untouched.
- `RunViewLast` gains the same branch so `--last` can re-read remotely.
  Signature grows by `(from string, remote bool)` — callers updated.

### `internal/repl/view.go` + session state

- Persist `sess.lastViewFrom` / `sess.lastViewRemote` next to the existing
  `lastViewModule`/`lastViewFile`; pass them into `RunViewLast` when the
  current args carry no remote flag.
- Log frame unchanged; add field `from=<target>` to the `displayed` /
  `copied to clipboard` lines when remote, matching how other remote verbs
  surface their target.

### `internal/repl/commands.go` / `repl.go` — flags + help

- `commandFlags["view"]` gains `--from`, `--remote`.
- Help rows under the `view` block:
  `{"  --from <t>", "View the file from a remote target (or --remote for the link binding)"}`.
- One-shot: `view` with a remote flag becomes `projectlessOneShot`-style
  eligible exactly as `logs`/`restart` did in Unit 72 (runs from a linked
  addons dir without local `[docker]` config).

### Tests

`internal/cmd/view_remote_test.go`:

- Flag stripping: `view sale --from prod` resolves module `sale` (never
  `prod`); `--from=prod` / `--remote` variants consumed.
- `remoteModuleFiles` path trimming + `skipViewPath` filtering from a
  canned `find` output (seam the SSH runner like `docker_remote_test.go`).
- `remoteModuleBase` probe order: hostFS hit short-circuits; relative
  paths joined under `remotePath`; absolute-only paths reach the container
  probe.

## Dependencies

None new — all reuse (`resolveRemoteShell`, `remoteContainerCmd`,
`runSSH`, `remoteFlagsIn`, `skipViewPath`, `ShowWithBat`).

## Verify when done

- [ ] `view sale --from prod` lists the **remote** `sale` files in the
      fuzzy picker and displays the chosen one via bat, with
      `echo.view … from=prod` in the log line.
- [ ] `view --remote` (linked dir) opens the module picker from the local
      checkout, then browses the remote files of the picked module.
- [ ] `view sale --from prod --copy` puts the remote file's content on the
      clipboard.
- [ ] `view --last` after a remote view re-reads the same remote file.
- [ ] Host-mode remote (module under `<remotePath>/<addons>/`) and
      conf-mode remote (module only in the container) both resolve; probe
      order is hostFS first.
- [ ] `--from <val>` / `--remote` never leak into the module positional.
- [ ] No prod gate fires (read-only command), on any stage.
- [ ] `view <mod>` with no remote flag is byte-for-byte today's local
      behavior.
- [ ] `help` shows the `--from <t>` row under `view`; registry/help/
      dispatch consistency tests stay green.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/cmd/...
      ./internal/repl/...` pass.
