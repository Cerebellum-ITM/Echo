# Unit 53: Odoo 19 i18n CLI — version-aware export/import

## Goal

Make `i18n-export`, `i18n-update` and `i18n-pull` work against **Odoo 19**
instances. Odoo 19 removed the server-flag form of translation handling
(`--modules=`, `--i18n-export=`, `--i18n-import=`, and the `--db_*`
connection flags on that path) and replaced it with a dedicated `odoo i18n`
subcommand whose only connection inputs are `-c <config>` and `-d <db>`.
Echo must emit the new subcommand form on 19+ and keep the legacy form on
17/18, selecting by the target's configured Odoo version. The observed
failure this fixes:

```
remote export: exit status 2: ... error: no such option: --modules
```

## Background — what changed in Odoo 19

Verified against `odoo/cli/i18n.py` @ 19.0. The new subcommand:

```
odoo i18n export -c <conf> -d <db> -l <lang> -o <file.po> <module>…
odoo i18n import -c <conf> -d <db> -l <lang> -w <file.po>
```

Key facts that shape this unit:

- Each `i18n` subparser accepts **only** `-c/--config` and `-d/--database`
  for the connection — *"connection details will be taken from the config
  file"*. `--db_host/--db_user/--db_password/--db_port` are **not** accepted
  (those live on the separate `odoo db` subcommand, a different parser).
- `run()` forwards `['--no-http', '-c?', '-d?']` to `config.parse_config`,
  so **do not** add `--no-http` or `--stop-after-init` — the subcommand
  injects the former and exits on its own.
- `export`: `-l/--languages` is `nargs='+'` (default `pot`); modules are
  positional; `-o/--output` writes a single file (`.po` by extension, `-`
  = stdout); only one language allowed with `-o`.
- `import`: positional `files`; `-l/--language` is **singular and required**;
  `-w/--overwrite` is the replacement for the legacy `--i18n-overwrite`;
  there is **no** module argument (terms are loaded from the file).

Because the 19 path can't take `--db_*` flags and Echo runs via
`compose exec` (which bypasses the image entrypoint that would translate
env → flags), the db credentials must arrive through `-c`. We supply them
via an **ephemeral odoo.conf written into the container per invocation**
(see Connection model).

## Design

### Version selection — configured, not detected

The target's Odoo major version is read from existing configuration; no
runtime `odoo --version` probe is added.

- **Local** (`i18n-export` / `i18n-update`): `cfg.OdooVersion` (already
  populated by `init`, persisted in `projects/<key>.toml` as `odoo_version`).
- **Remote** (`i18n-pull`): the remote project's own `odoo_version`. It is
  already written to the server's `projects/<key>.toml` by `init` there, but
  `RemoteProfile` currently drops it. This unit adds `OdooVersion` to
  `RemoteProfile`, parses it in `ParseRemoteProfile`, and threads it onto
  `connectTarget`.

Branch on major ≥ 19. An empty/unparseable version falls back to the
**legacy** form (safe default for the existing 17/18 fleet).

### Connection model on 19 — ephemeral container conf

For 19+ Echo generates an in-container odoo.conf holding the same
connection values it resolves today, passes it with `-c`, and removes it
after the run. The conf is:

- **Not in the repo, never persisted.** It is regenerated each invocation
  from values Echo already has (`db_host` = db container, `db_port`/
  `db_user`/`db_password` from the remote `.env` or local config). There is
  nothing new to store in global/project config.
- **Written inside the container** at a unique `/tmp/echo-i18n-*.conf`,
  mirroring the existing temp-`.po` lifecycle (`tmpPathInContainer`).
- **Removed best-effort** alongside the temp `.po` after the command.

Content (only non-empty fields emitted):

```
[options]
db_host = <host>
db_port = <port>
db_user = <user>
db_password = <password>
```

Security note: this is *better* than the legacy path, which exposes
`--db_password=…` in the container's process list (`ps`); a conf file does
not appear in argv.

Considered and rejected: passing `-e PGHOST/PGUSER/…` on `compose exec`
(libpq env) — avoids a file but spreads connection state across the exec
invocation per call-site and is less explicit than Echo's existing
"connection passed deliberately" model.

## Implementation

### `internal/odoo/cmd.go` — version-aware builders

- `Major(version string) int` — `"19.0"/"19"` → `19`, `""`/garbage → `0`.
  (Reuse/relocate the `odooSerie` parsing idea; keep it in the odoo pkg.)
- `RenderConf(c Conn) []byte` — emits the `[options]` body above, skipping
  empty fields. Pure function, unit-tested.
- `ExportI18n(c Conn, version, module, lang, outPath, confPath string) Cmd`:
  - **19+**: `odoo i18n export -c <confPath> -d <c.DB> -l <lang> -o <outPath> <module>`
    (`-d` only when `c.DB != ""`).
  - **legacy**: unchanged — `odoo <c.flags()…> --modules=<module> -l <lang>
    --i18n-export=<outPath> --stop-after-init`.
- `UpdateI18n(c Conn, version, module, lang, inPath, confPath string) Cmd`:
  - **19+**: `odoo i18n import -c <confPath> -d <c.DB> -l <lang> -w <inPath>`
    (no module positional on 19).
  - **legacy**: unchanged — `--modules=<module> -l <lang>
    --i18n-import=<inPath> --i18n-overwrite --stop-after-init`.

`confPath` is ignored on the legacy branch; `c.flags()` is unused on the
19 branch. `module` is accepted by both signatures but unused on the 19
import branch (kept for symmetry / legacy).

### `internal/config/config.go` — surface the remote version

- Add `OdooVersion string` to `RemoteProfile`.
- In `ParseRemoteProfile`, set `OdooVersion: p.OdooVersion` (the
  `projectFile.OdooVersion` / `odoo_version` field already exists).

### `internal/cmd/connect.go` — carry version onto the target

- Add `odooVersion string` to `connectTarget`.
- Populate it from `RemoteProfile.OdooVersion` where the remote target is
  assembled (and leave it empty / from `cfg.OdooVersion` for local targets
  if a local target is ever built here).

### `internal/cmd/i18n.go` — local export/update

Both `RunI18nExport` and `RunI18nUpdate`:

- Compute `ver := opts.Cfg.OdooVersion`.
- When `odoo.Major(ver) >= 19`: render `odoo.RenderConf(buildI18nConn(opts))`,
  write it into the container at a `tmpConfInContainer()` path, and pass that
  path as `confPath`. Cleanup the conf next to the temp `.po`
  (`cleanupContainerTmp`, generalized to any path).
  - Writing locally reuses existing docker primitives: write conf bytes to a
    host temp file and `docker.CopyToContainer` it, OR add a small
    `docker.WriteFile(ctx, id, path, bytes)` helper — pick the
    CopyToContainer route to avoid new docker surface.
- Pass `ver` and `confPath` through to `odoo.ExportI18n` / `odoo.UpdateI18n`.
- Legacy path: `confPath == ""`, no conf written — byte-for-byte the current
  behavior.

`tmpPathInContainer` is generalized (or a sibling `tmpConfInContainer`
added) so the `.conf` gets a distinct suffix.

### `internal/cmd/i18n_pull.go` — remote pull

- `pullRemotePO` takes the target's `odooVersion`. On 19+:
  1. `runSSH` write the conf into the container:
     `cd <path> && <compose> exec -T <odoo> sh -c 'cat > <tmp.conf>'` with the
     conf bytes piped via `runSSH`'s stdin.
  2. `odoo.ExportI18n(conn, ver, module, lang, tmpPO, tmpConf)` → run.
  3. `cat <tmpPO>` → bytes; `rm -f <tmpPO> <tmpConf>` (best-effort).
- Legacy path unchanged (no conf, `--i18n-export=`).

## Dependencies

- none (reuses `internal/odoo`, `internal/docker`, `internal/config`,
  `internal/env`, and the existing connect/i18n helpers).

## Verify when done

- [ ] `i18n-pull <mod> es_MX --from <o19-target>` succeeds against an Odoo 19
      instance and writes the `.po` locally (the original `--modules` error
      is gone).
- [ ] `i18n-export <mod>` and `i18n-update <mod>` work against a **local**
      Odoo 19 stack.
- [ ] A 17/18 target still emits the legacy `--modules=`/`--i18n-export=`
      form, byte-for-byte unchanged (golden-style argv test).
- [ ] `odoo.Major` parses `"19"`, `"19.0"`, `"18.0"`, `""` correctly; an
      empty/garbage version falls back to legacy.
- [ ] `odoo.RenderConf` emits only non-empty fields under `[options]` and is
      unit-tested.
- [ ] The temp `.conf` and temp `.po` are both removed from the container
      after the run (remote and local).
- [ ] No `--db_*` flag and no `--no-http`/`--stop-after-init` appears on the
      19 `i18n` argv.
- [ ] `go build/vet/test ./...` pass; `registry`/`commandhl` cross-checks
      stay green; `CHANGELOG.md` `[Unreleased]` gets an `Added`/`Fixed` entry
      and the affected context files are updated if architecture shifts.
```
