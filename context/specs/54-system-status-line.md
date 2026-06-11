# Unit 54: system-status line for connect / run / i18n-pull

## Goal

Emit a single Odoo-style "system status" log line **at the start** of the
`connect`, `run`, and `i18n-pull` commands (once per invocation, not per
executed sub-command), so an operator running Echo — especially headless /
one-shot, without the REPL banner — immediately sees which environment is in
play: the Echo CLI version (with build metadata / `dirty`) and the resolved
Odoo environment (version, project name/alias, database). This doubles as a
diagnostic: if the Odoo version shows blank/wrong, the version-dependent
behaviour (e.g. the Odoo 19 i18n CLI form) is mis-targeted and it's visible
up front.

Example (i18n-pull against a remote Odoo 19):

```
2026-06-11 11:02:03,114 10354 INFO develop echo.system.status: system cli=0.10.0+abc1234.dirty odoo=19.0 project=dvz_ny_odoo_19 db=develop
```

This is **not** per-command exec logging — running `up`, `update`, etc. does
not each get a line. One status line at the start of the command, period.

## Design

A single shared logger `echo.system.status` (not namespaced per command),
emitted at INFO with message `system` and four key=value fields rendered by
the existing log-field machinery. The per-command logger wrappers map the
sentinel `sub == "system"` to this shared logger:

| field     | local (`run`, local `connect`)        | remote (`i18n-pull`, remote `connect`)      |
| --------- | ------------------------------------- | ------------------------------------------- |
| `cli`     | `repl.FullVersion()` (`+sha`/`.dirty`)| same                                        |
| `odoo`    | `cfg.OdooVersion`                     | `prof.OdooVersion` / `target.odooVersion`   |
| `project` | compose project name (alias/basename) | `--from` target name, else `base(remotePath)` |
| `db`      | `cfg.DBName`                          | `prof.DBName` / `target.dbName`             |

Empty values render as `unknown` (for `odoo`) / `-` (for `db`/`project`) so a
missing remote `odoo_version` is loud rather than silently absent.

**Emit point — as early as the data allows.** The status line must precede
the first log of the command's actual work. For `run` (all-local) it is the
very first line, from `repl.RunRecipe`. For `connect` it is emitted right
after `resolveConnectTarget`, before any minting work. For `i18n-pull` the
Odoo version is **remote** — unknowable until the target is picked and its
Echo profile is read over SSH — so the line is emitted the instant
`fetchRemoteProfile` returns, and it **replaces** the old `connected` line
(the status line itself signals a successful connection and carries more
detail). Its preceding lines are only the unavoidable connection bracket
(`selecting remote target` → picker → `target resolved` → `reading remote
profile`), and it sits immediately before the first work line (`listing
modules`). Because both the interactive REPL path and the one-shot path
funnel through `cmd.RunConnect` / `cmd.RunI18nPull`, emitting there covers
both modes with one call site each.

**Useless start line removed.** `i18n-pull` no longer emits the generic
`echo.i18n-pull.start: i18n-pull` line (it carried no information). The first
meaningful line is `selecting remote target` (before the picker, when no
`--from`/target resolves) or `target resolved` (when one does).

**Echo version across the import boundary.** `FullVersion()` lives in
`internal/repl`, which `internal/cmd` cannot import (cycle). A package-level
`var cmd.EchoVersion string` is introduced and set once from `main.go`
(`cmd.EchoVersion = repl.FullVersion()`) before any dispatch, for every entry
path (interactive `Start`, `RunOnce`, `RunRecipe`, `RunDirectConnect`). The
`cmd` status emitters read `cmd.EchoVersion`; `repl.RunRecipe` may use
`FullVersion()` directly.

## Implementation

### `internal/cmd/status.go` (new)

- `var EchoVersion string` — set by `main.go`; empty in tests is fine.
- `func statusProjectName(cfg *config.Config, remote bool, remotePath, fromName string) string`
  — remote: `fromName` if non-empty, else `filepath.Base(remotePath)`; local:
  `cfg.ComposeProject` if set, else `filepath.Base(cfg.ProjectPath)`; `-` when
  nothing resolves.
- `func statusFields(odooVer, project, db string) [][2]string` — builds the
  `cli`/`odoo`/`project`/`db` pairs with the `unknown`/`-` fallbacks, using
  `EchoVersion` for `cli`.

### `internal/cmd/connect.go`

In `RunConnect`, right after `resolveConnectTarget` succeeds, call
`opts.log("INFO", "system", "system", target.dbName, statusFields(...)...)`
with `odoo = target.odooVersion`, `project = statusProjectName(opts.Cfg,
target.remote, opts.Cfg.ConnectRemotePath, "")`, `db = target.dbName`.

### `internal/cmd/i18n_pull.go`

In `RunI18nPull`, right after the `target` is built from the remote profile
(after the `connected` log), emit the same line via `opts.log("INFO",
"status", "system", target.dbName, ...)` with `odoo = target.odooVersion`,
`project = statusProjectName(opts.Cfg, true, remotePath, p.from)`, `db =
prof.DBName`.

### `internal/repl/recipe.go`

In `RunRecipe`, after the session is built and before the first step, emit
`emitOdooLog("INFO", "echo.run.status", "system", fields, …)` with `cli =
FullVersion()` (or `cmd.EchoVersion`), `odoo = cfg.OdooVersion`, `project =
resolveComposeProject(cfg)`, `db = cfg.DBName`.

### `main.go`

Set `cmd.EchoVersion = repl.FullVersion()` once near the top of `main`,
before the connect short-circuit and the one-shot/REPL dispatch, so every
path has it.

## Dependencies

- none (reuses `internal/repl` log emit, `internal/cmd` opts loggers,
  `internal/config`).

## Verify when done

- [ ] `echo i18n-pull <mod> <lang> --from <t>` prints one `echo.i18n-pull.status:
      system cli=… odoo=… project=… db=…` line right after connecting, before
      the per-module export lines.
- [ ] `connect` (interactive and one-shot/direct) prints `echo.connect.status`
      once at start with the resolved odoo/project/db.
- [ ] `run <recipe>` prints `echo.run.status` exactly once at the start, not
      per step.
- [ ] `cli` shows `.dirty` for a dirty build and the bare `+sha` for a clean
      one; a plain `go build` (empty `VersionMeta`) shows just the semver.
- [ ] A remote target without `odoo_version` shows `odoo=unknown`, making the
      mis-config visible.
- [ ] No secret (db password) appears in the status line.
- [ ] `go build/vet/test ./...` pass; `CHANGELOG.md` `[Unreleased]` gets an
      `Added` entry.
```
