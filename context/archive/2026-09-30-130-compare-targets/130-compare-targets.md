# Unit 130 · compare-targets
> Spec for one unit of [the plan](../../work/build/plan.md). Implement exactly this: no more, no less. Last verified: 2026-09-30.

## Goal

One read-only command answers "what does dev have that staging does not" before a partial deploy:
`compare --targets dev,staging` shows, per module, the source, sha and version each target's deploy lock
records, side by side. Today it takes `deploy --lock --from <t>` per target and comparing by eye
(`runDeployLockShow`, `internal/cmd/deploy_lock.go:364`).

## Design

### A. Command surface

- `compare --targets <a>,<b> [<mod>...] [--json] [--copy]` (also `--targets=`; the parser consumes the value,
  [trap](../../knowledge/architecture-and-traps.md#cross-cutting-traps)). Positionals filter modules.
- Exactly two names, each a connect target resolved by `resolvePullRemote(cfg, name)`
  (`internal/cmd/i18n_pull.go:155`), a pure lookup in `cfg.ConnectTargets`. No picker, no link binding, no
  `env:` reference (`-E` is going away, Unit 122).
- `ErrUsage` (exit 2) before any SSH: one or three names, the same name twice, an unknown name, two names with
  the same `ssh_host` and `remote_path` (one lock, nothing to compare), and `--targets` combined with
  `--all`, `--from`, `--remote`, `-E`/`--env`; `--json` without `--targets`. `compare` without `--targets` is unchanged.
- Read-only and never stage-gated (`compare` already is not, [remote-targets](../../knowledge/remote-targets.md)).
  No server profile or `.env` read: one SSH call per target, the `cat` of `.echo/lock.json`, `a` then `b`.

### B. Reading each side

Each side ends in one of four states: `found`, `absent` (no file: valid, every module reads `locked=none`),
`unreadable` (SSH failure) or `corrupt` (not JSON). Today `readDeployLock` (`deploy_lock.go:133`) folds the last
two into "absent" with a WARNING. Unit 131 (implemented before this unit) adds the split: `lockState`,
`fetchDeployLock` (raw bytes, transport error apart) and a pure `parseDeployLock`, under the warn-and-continue
wrapper deploy keeps ([131](../../archive/2026-09-30-131-doctor/131-doctor.md)). This unit reuses it.

`unreadable` and `corrupt` print one ERROR line naming the target and the reason; the other side is still shown
(every row `unknown`, see C). Both sides failing prints both ERROR lines and no table.

### C. What "differs" means

Per module in the union of both locks (or the filter), compared by content identity, not by sha:

| Status | When |
|---|---|
| `only` | one side has no entry (`only=<target>`). It means "Echo never recorded shipping it there", not "absent from the server" ([lock limits](../../knowledge/deploy-safety.md#deploy-lock)). |
| `unknown` | either entry is `dirty` (content that existed only on someone's disk), identity cannot be derived (below), or the other side is unreadable. |
| `same` | both identities are equal trees, or both entries are clean with the same `sha`. |
| `differs` | both identities are trees and differ. |

Identity is the entry's `tree`. A clean entry without one (`worktree`, or a pre-`tree` entry) derives it
locally with `git rev-parse <sha>:<moduleRepoPath>`, the call `lockEntries` already makes
(`deploy_lock.go:254`); when the sha is not in the local repo or the cwd is not a repo it stays sha-only. So a
module shipped as `worktree@X` (clean) to dev and as `ref@Y` to staging is `same` when both trees match.

On a git-deploy side with a lock `base`, a module without an entry is shown as `base@<sha7>` with its identity
from the base sha the same way: absent at that sha it is `only` for the other side; base sha not local, `unknown`.

Version is reported, never decisive: `newer=<target>` when `compareVersions` (`internal/cmd/modinfo.go:83`)
orders the two manifest versions, empty when equal or missing. `verified=false` is shown on its side.

### D. Output

One INFO per side first, then the table, then the closing line (logger `echo.compare.targets`):

```
INFO    echo.compare.targets: lock target=dev modules=14 unverified=1 base=main@3f2a9c1
INFO    echo.compare.targets: lock target=staging modules=12 unverified=0
  module               status        dev                                 staging
  ccima_flow_mail      differs       ref@99f2109 1.4.0                   commit@a1c4e02 1.3.0
  ccima_crm_reassign   only dev      worktree@0b6fc41+dirty 1.2.0 unv.   none
INFO    echo.compare.targets: targets compared a=dev b=staging same=11 differs=1 unknown=0 only_dev=1 only_staging=0
```

- Cells are `LockModule.label()` (`deploy_lock.go:78`) plus the version. The table is `Line{Kind:"table"}` like
  `compare --all` (`internal/repl/compare.go:120`): `differs` warn, `only` info, `unknown` faint. Only non-`same`
  rows print (as `compare --all` hides equal files), except modules named as positionals. All `same`: closing line only.
- `--copy` puts the plain table and closing line on the clipboard, as `compare --all` does.
- `--json`: one object on stdout, diagnostics on stderr ([ui](../../knowledge/ui.md#stdout-contract-and---json)).
  All modules regardless of status; empty collections `[]`, absent sides `null`:

```json
{"a": {"name": "dev", "host": "…", "path": "…", "lock": "found", "error": "", "base": {…}},
 "b": {"name": "staging", "host": "…", "path": "…", "lock": "found", "error": "", "base": null},
 "modules": [{"name": "ccima_flow_mail", "status": "differs", "only": "", "newer": "dev", "reason": "",
              "a": {"source": "ref", "sha": "…", "tree": "…", "version": "1.4.0", …}, "b": {…}}],
 "counts": {"same": 11, "differs": 1, "unknown": 0, "only_a": 1, "only_b": 0}}
```

`a`/`b` entries are the `LockModule` JSON as stored. `reason` is `dirty`, `no-identity` or `unreadable` for
`unknown`. `host` is the `ssh_host` alias; nothing else of the target config is emitted.

### E. Exit codes

0 when both locks were read (found or absent), whatever the differences (a viewer, like `compare`;
see Decisions). 1 when either
side is `unreadable` or `corrupt`; the output, JSON included, is still emitted. 2 for usage. No auto-copy on
failure: a read-only command ([code-standards](../../knowledge/code-standards.md#errors-and-exits)).

## Decisions

The three open points were settled by the user on 2026-09-30 with the recommended options.

- **Exit code when the targets differ**: always 0 when both locks were read (found or absent), whatever the
  differences; `compare` is a viewer, and "dev is ahead" is the expected answer, not a failure (E). An opt-in
  `--exit-code` as in `git diff` can come when a CI or agent asks. Rejected: exit 1 on any `differs` or `only` row.
- **The local working tree is not a side**: `deploy --dry-run` already prints `ship=` vs `locked=` per module
  (`logCodePlan`, `deploy_lock.go:335`), the local-vs-target question. Rejected: `.` as a side built with
  `lockEntries`; a reserved name `local`, which can collide with a target name.
- **Exactly two targets**: the question is pairwise, and a third column of labels no longer fits a normal
  terminal width. Rejected: 2 to 4 columns with the status relative to the first.

## Implementation

### internal/cmd

- `compare.go`: `parseCompareArgs` returns a `compareArgs` struct (module list, `copy`, `all`, `from`,
  `remote`, `targets`, `json`) instead of six values; it parses `--targets` and `--json` and validates A.
- `compare_targets.go` (new): `RunCompareTargets(ctx, CompareTargetsOpts) (CompareTargetsResult, error)` with a
  `Log` callback; `diffLocks(a, b sideLock, identity func(LockModule) string) []TargetRow` (pure);
  `lockIdentity` (C, local `git rev-parse` only). The result carries both sides, rows and counts, and marshals
  to the D shape.
- `deploy_lock.go`: nothing new; reuses the fetch split Unit 131 adds.

### internal/repl

- `compare.go`: `--targets` routes to `runCompareTargets` before the `--all` branch; renders D; `--json` writes
  only the object to `os.Stdout` and logs through `emitOdooLogTo(os.Stderr, …)`; `ErrUsage` goes through
  `sess.finalize` plus `exitUsage`.
- `commands.go`: `--targets` and `--json` in `commandFlags["compare"]`; build mode does not offer `--targets`.
  `repl.go`: a help row `compare --targets a,b [<mod>]`.

### main.go

- `projectlessOneShot`: `compare` also qualifies when its args carry `--targets`, checked for `compare` only,
  not by widening `hasRemoteFlag` for every command.

### Docs

- `knowledge/modules-and-odoo.md` (view and compare): the targets mode and its statuses;
  `knowledge/deploy-safety.md`: add it to the lock readers. `README.md` (compare) and `CHANGELOG.md`
  `[Unreleased]` Added. A ledger row in `knowledge/operations.md` (live check pending). `work/build/plan.md`:
  mark done.

## Dependencies

- Unit 124 (the lock, `a73d4db`); Unit 125 for `ref` entries (`1d29ccb`). Reuses the lock-fetch split from
  Unit 131. No new Go dependency.

## Verify when done

- [ ] With the fake `ssh` (`newFakeRemote`) and two remote paths holding hand-written locks,
      `compare --targets dev,staging` prints `differs`, `only`, `unknown` (dirty) and hides `same` rows; exit 0;
      the fake `ssh` log shows two `cat` calls and nothing else (no `mkdir`, `mv`, compose).
- [ ] Same tree under different shas is `same`; a clean `worktree` entry whose sha exists locally matches a
      `ref` entry with that tree; two different shas unknown locally give
      `unknown reason=no-identity`.
- [ ] A target without a lock prints `no deploy lock` and every row `only <other>`, exit 0. An unreachable
      side (`lockRunSSH` failing for that host) or a corrupt lock prints an ERROR, still shows the other side, exits 1;
      both failing exits 1 with no table.
- [ ] One name, three names, a repeated name, an unknown name, two names on the same host and path, and
      `--targets` with `--from` or `--all` exit 2 before any SSH. `compare <mod>` and `compare <mod> --all`
      behave as before (existing tests unchanged).
- [ ] `--json` writes one object on stdout and nothing else, `modules: []` when both locks are empty;
      `--copy` fills the clipboard with the plain table.
- [ ] `echo_cli compare --targets a,b` runs from a directory without `docker-compose.yml`.
- [ ] Table tests for `diffLocks` and `lockIdentity`.
- [ ] `go build ./...`, `go vet ./...` and `go test ./...` pass; new tests use the existing seams (temp `HOME`,
      fake `ssh` on `PATH`, `lockRunSSH`), never a real server.
