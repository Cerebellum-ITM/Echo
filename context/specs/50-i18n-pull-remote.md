# Unit 50: `i18n-pull` — pull module translations from a remote instance

## Goal

A new command `i18n-pull [<mod>] [<lang>] [--from <target>] [--all]` that
exports a module's translations **from a remote Odoo instance** (reached
over SSH the same way `connect` does) and writes the resulting `.po` into
the **local repo** at `<addons>/<mod>/i18n/<lang>.po`. Use case: someone
edited translations in a remote prod/staging UI and you want them back in
the repo to commit. The remote database is never modified — this is a read.

```
i18n-pull sale es_MX        # pull sale's es_MX from the project's remote
i18n-pull --all fr_FR       # pull fr_FR for every local-repo module
i18n-pull sale --from prod  # use the named connect target "prod"
```

## Design

This bridges two existing pieces: `connect`'s remote-exec machinery
(`resolveConnectTarget`, `fetchRemoteProfile`, `runSSH`, `shellQuote`) and
`i18n-export`'s local destination logic (`resolveModuleDir`,
`defaultExportDest`, `tmpPathInContainer`). No new transport or path code —
it composes what's there.

**Remote source.** Resolution order: `--from <target>` (a named
`connect_target`) → the project's own `[connect]` config (`ssh_host` /
`remote_path`) → the global connect targets as a fallback (a single one is
used automatically, several open a TTY-guarded picker, none →
`ErrNoPullRemote`). The fallback is what makes projectless `i18n-pull`
usable whenever connect targets are configured — without it the command
ignored them and demanded `[connect]`/`--from`. The remote's container/db mapping comes from the
server's own Echo profile (`resolveConnectTarget` → `fetchRemoteProfile`),
and its Postgres credentials from the remote `.env` (read over SSH), so the
`odoo --i18n-export` invocation carries explicit `--db_*` flags (the same
reason the local commands do: `compose exec` bypasses the entrypoint that
would otherwise translate env → flags).

**Per module, three SSH calls** (`pullRemotePO`):
1. `odoo … --i18n-export=/tmp/echo-i18n-*.po --stop-after-init` inside the
   remote container (argv built by the existing `odoo.ExportI18n`).
2. `cat <tmp>` → the `.po` bytes come back on stdout.
3. `rm -f <tmp>` (best-effort cleanup).

All three run as `cd <remote_path> && <compose> exec -T <odoo> <argv…>`
(`remoteContainerCmd`), each argv token shell-quoted; the compose command
is emitted raw so a two-word `docker compose` splits correctly.

**Module scope.** A single module by default (fuzzy picker when omitted,
TTY-guarded); `--all` pulls every candidate. Candidates come from the
**remote** instance — the local project we run from is often unrelated (or
has no addons), so a local scan is wrong and produced the "no modules
found" failure. Two remote sources:

- **default — the remote project's own modules** (`listRemoteConfModules`):
  the directories with a `__manifest__.py` under the remote `addons_path`,
  taken from the addons paths stored in the remote Echo profile, or by
  reading and parsing the remote `odoo.conf` (`parseAddonsPath`, same as the
  local conf-mode listing) when they aren't stored. This is the set the
  developer maintains — not every stock Odoo module — which is what the
  picker should show.
- **`--installed`** (`listRemoteModules`): every installed module from
  `ir_module_module`, queried over SSH in the remote Postgres container.
  Kept as an escape hatch for when you really want the full set.

Under `--all`, a module whose remote export fails is skipped with a
warning; a single-module run surfaces the error. An empty `.po` (no
translations for that lang) is skipped rather than clobbering the local
file.

**Destination.** `pullDest` writes to the module's real addons dir on the
host (`<addons>/<mod>/i18n/<lang>.po`, via `resolveModuleDir`) when the
module exists on disk — the host-mode dev flow is unchanged. When it
doesn't (conf-mode / staging whose addons live only in the container, so
there is no host folder), it falls back to a cwd-relative
`<root>/<mod>/i18n/<lang>.po` so the file can still be pulled and committed.

**Language.** One per run, default `es_MX` (matches `i18n-export`). With
`--all` the single optional positional is the language; otherwise the
positionals are module then language.

`MkdirAll` on the parent, overwrite on write — the whole point is to bring
the remote translations into the working tree.

Output is the standard streamed/`finalize` frame (`echo.i18n-pull`), with a
`pulling <lang> from <host>:<path>` line, a `→ <dest>` per module, and a
`pulled N skipped M` summary under `--all`.

**No local compose project required.** `i18n-pull` never drives a local
docker stack — it only reads/writes local files and talks to the remote.
So, like `connect`, it must run even outside a `docker-compose.yml`
directory (the common case is a pure addons repo whose Odoo runs on a
server). `main.go` treats it as a `projectlessOneShot`: when `FindRoot`
fails for a one-shot `i18n-pull`, the working directory falls back to cwd
(or `-C <dir>`) instead of exiting "not inside a project". Without a
project there is no `[connect]`, so `--from <target>` is required (else the
clean `ErrNoPullRemote`). Inside a project it behaves as before (project
root + the project's `[connect]`).

## Implementation

### `internal/cmd/i18n_pull.go`

- `I18nPullOpts{Cfg, Root, Args, Palette, StreamOut}`.
- `parseI18nPullArgs(args)` → `{module, lang, from, all}` (rules above;
  unknown flag / too many positionals / `--all`+module → error).
- `resolvePullRemote(cfg, from)` → `(sshHost, remotePath, err)` from a named
  target or the project `[connect]`; `ErrNoPullRemote` when neither works.
- `RunI18nPull(ctx, opts)`: resolve remote → `resolveConnectTarget` on a cfg
  copy with the chosen ssh host/path → `odoo.Conn` from `remotePullEnv` →
  module set → per-module `pullRemotePO` + local write.
- `remotePullEnv`, `pullRemotePO`, `remoteContainerCmd` as designed.

### `internal/repl/i18n_pull.go`

`runI18nPull` mirrors `runI18n`: `startLog`, a `runStats`-wrapped
`StreamOut`, then `commandFailureLog` on error / `finalize` otherwise.

### Wiring

`Registry`, `dispatchNames`, `dispatchParsed` (`case "i18n-pull"`),
`commandFlags["i18n-pull"] = {"--from", "--all"}`, and a `helpSections`
(i18n) block. One-shot eligible via `dispatchNames`
(`echo i18n-pull sale es_MX`), so it works in recipes too.

## Dependencies

- none (reuses `internal/cmd` connect + i18n helpers, `internal/odoo`,
  `internal/env`).

## Verify when done

- [ ] `i18n-pull sale es_MX` exports `sale`'s `es_MX` on the remote and
      writes it to the local `<addons>/sale/i18n/es_MX.po`.
- [ ] `--from <target>` selects a named connect target; default uses the
      project's `[connect]`; neither configured → clean error (exit 1).
- [ ] `--all` pulls every local module, skipping unresolved/failed ones
      with a warning and a `pulled N skipped M` summary.
- [ ] No module + TTY → picker; no module + non-TTY → fails closed.
- [ ] The remote DB is never written to (export only); the temp `.po` is
      removed from the remote container.
- [ ] `parseI18nPullArgs`, `resolvePullRemote`, and `remoteContainerCmd`
      (quoting) are unit-tested.
- [ ] `go build/vet/test ./...` pass; `registry`/`commandhl` cross-checks
      stay green; `CHANGELOG.md` `[Unreleased]` gets an `Added` entry.
