# Unit 148 · remote-module-state
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-20.

## Goal

`modstate` and `modinfo` answer for a remote target as they do for the local stack, and both return JSON, so an
agent asks "which modules are installed there, and which of mine still need `-u`?" in one call instead of a probe.

## Design

From the 2026-10-19 audit: both commands are local-only (`modstate` reads the local DB container; `modinfo` takes
one module and opens a picker without one), and remote module state was the most frequent probe the `odoo-probe`
skill made agents write.

Decisions:

1. **`modstate [mods...] [--all] [--json] [--from <t> | --remote]`**
   - Remote rows come from the same `ir_module_module` query deploy already runs on the server (through
     `runRemoteDBCmd`, so the DB exec transport and its fallback apply); local stays `docker.ModuleStates`.
   - New positional filter: only the named modules, local and remote. A named module that is not in the result
     appears with state `uninstalled` when `--all` would show it, and is simply absent otherwise (no error).
   - JSON unchanged: the same array of `{name, state, latest_version}`.
2. **`modinfo <mod...> | --all [--json] [--from <t> | --remote]`**
   - Several modules in one call. `--all` means every module of the local project's addons (`resolveModules`);
     it excludes module names. No names and no `--all` keeps today's picker (fails closed without a TTY).
   - Versions per module:
     - `db_version` and `state`: from the target's DB (local or remote);
     - `manifest_version`: the local working tree manifest (what you would ship), as today;
     - `deployed_version` (remote only): the version the target's deploy lock records for the module, or
       empty when the lock has no entry.
   - `status` keeps today's verdicts (`in sync`, `update pending`, `db ahead`, `not installed`, `no version`),
     computed from `db_version` against `manifest_version` exactly as `computeStatus` does.
   - Human output: one `echo.modinfo` line per module, as today, plus `deployed=` when remote; with several
     modules, a closing line `echo.modinfo: checked modules=N pending=N ahead=N missing=N`.
   - `--json`: an array of `{module, state, db_version, manifest_version, deployed_version, status}`
     (`deployed_version` omitted locally); logs to stderr.
   - A module name not found locally (no manifest) is not an error: `manifest_version` empty, status from the DB
     only (`not installed` or `no version`).
   - `--last` keeps its current meaning and stays local-only; with `--from`/`--remote` it is `ErrUsage`.
3. **Read-only, no prod gate**, like the other remote reads. Remote calls go through `resolveRemoteShell`.
4. **Exit codes**: 0 when the query ran (whatever the verdicts), 1 on a query failure, 2 on usage errors
   (unknown flag, `--all` with names, `--last` with a remote flag).

## Implementation

- `internal/cmd/modstate.go`, `internal/cmd/modinfo.go`: parsers (consume `--from` values), the remote branches
  reusing the deploy module-state query and `readDeployLock`, the multi-module loop.
- `internal/repl/modstate.go`, `internal/repl/modinfo.go`: JSON and multi-line rendering, exit mapping.
- Wiring: `commandFlags` (`--from`, `--remote`, `--json` for both; `--all` for `modinfo`), `commandDocs` notes and
  examples, `projectlessOneShot` for both with a remote flag (`main.go`).
- Tests: parsers; remote rows over the fake-`ssh` harness; `modinfo` with three modules (in sync, pending,
  missing manifest) in JSON and human form; `--all` with names and `--last --remote` exit 2.
- Knowledge: [modules-and-odoo](../../knowledge/modules-and-odoo.md#modinfo-and-modstate).
- `CHANGELOG.md` `[Unreleased]` `Added`.

## Done

Build/vet/test green, help cross-checks green, CHANGELOG entry, ledger row 148 "live verification pending"
(`modstate --remote --json` and `modinfo --all --remote --json` on `develop`).
