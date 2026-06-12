# Unit 61: `deploy` — commit-driven remote update/install

## Goal

A new command `deploy [--from <target>] [--limit N] [--dry-run] [--force]`
that, from a local addons repo, opens a multi-select picker over the
repo's recent commits, maps each selected commit to an Odoo module,
decides per module between `install` and `update` by querying the remote
instance's installed modules, and then runs the deploy on the remote host
over SSH: `stop` → `up -d` (recreate the container so image/code changes
take effect) → one Odoo run with `-i`/`-u` for the resolved modules — all
streamed live through the Unit 60 transport. Pre-condition (out of scope
to verify): the user already pulled the new code on the server; `deploy`
only orchestrates the container and the module state.

```
deploy                  # picker over the last 20 commits, project's [connect]
deploy --from prod      # explicit named target
deploy --limit 50       # deeper commit list
deploy --dry-run        # resolve + plan + report, execute nothing remote
```

## Design

**Remote target.** Identical resolution to `i18n-pull` (Unit 50):
`--from <target>` → the project `[connect]` binding (written by `link`,
Unit 60) → global connect targets fallback (one auto-used, several open a
TTY-guarded picker, none → clean error). Container/db mapping and stage
come from the server's own Echo profile (`resolveConnectTarget` →
`fetchRemoteProfile`); Postgres credentials from the remote `.env`, so the
Odoo run carries explicit `--db_*` flags (same reason as `i18n-pull`:
`compose exec` bypasses the env-translating entrypoint).

**Commit selection.** `git log -n <limit>` (default 20) on the current
branch of the cwd repo, newest first, via `os/exec git` at the project
root (no git library dependency). Picker rows show
`<sha7>  <subject>` with the tab-toggle multi-select keymap used by the
module pickers. Not a git repo → clean error; empty selection → cancelled
(exit 3); non-TTY → `ErrNonInteractive` (invariant 9) — `deploy` is
interactive by design, there is no headless commit-selection mode in this
unit.

**Commit → module resolution.** Per selected commit, in order:

1. **Subject scheme** `[Tag] module_name: title` — regex
   `^\[[^\]]+\]\s*([A-Za-z0-9_]+)\s*:` over the subject. The captured
   name is valid only if `<repo>/<name>/__manifest__.py` exists (the local
   repo is the source of truth for what is an addon; commits touching
   non-addon names like `repl` or `docs` fall through).
2. **Diff fallback** — `git show --name-only --pretty=format: <sha>`; map
   each changed path to its top-level directory and keep those that
   contain a `__manifest__.py`. Exactly one module → use it. Zero or more
   than one → the commit is **unresolved**.
3. Unresolved commits are **excluded and reported**: a `WARNING skipped`
   line per commit during resolution and a recap in the final summary
   (`deployed=N skipped=M`). They never abort the run (per scope
   decision); if *every* selected commit is unresolved, `deploy` errors
   out before touching the remote.

Modules are deduplicated across commits, sorted, and reported in the plan.

**Install vs update.** Query the remote's installed module set the way
Unit 50's `--installed` does (`listRemoteModules`: `ir_module_module` via
psql in the remote DB container over SSH). A resolved module that is
`installed`/`to upgrade` → the `-u` set; anything else (not present,
`uninstalled`) → the `-i` set. The split is shown in the plan line
(`update=a,b install=c`) before anything runs.

**Plan + confirm.** Before executing, `deploy` emits the full plan as log
lines (target, db, stage, container, update set, install set, skipped
commits) and then:

- `--dry-run` → stop here, exit 0. No SSH mutation at all (the profile
  fetch and module query are reads and do run — the dry-run's whole value
  is showing the real split).
- Remote stage `prod` → `confirmProd`-style gate (same semantics as
  `maybeConfirmConnectProd`), bypassed by `--force`, fails closed without
  a TTY.

**Execution.** Three remote steps, every line streamed via `runSSHStream`
→ `emitStreamLine` (live, colorized, counted):

1. `cd <remote_path> && <compose> stop` — bracketed by `echo.deploy` log
   lines.
2. `cd <remote_path> && <compose> up -d` — recreates containers when
   image/compose changed.
3. `cd <remote_path> && <compose> exec -T <odoo> <odoo argv>` — a single
   Odoo run combining both sets: `-i mod,…` and/or `-u mod,…` with `-d`,
   `--db_*`, `--stop-after-init` (argv from the existing `internal/odoo`
   builders; add a combiner if `Install`/`Update` can't compose today).
   Either set may be empty; both empty cannot happen (caught at the
   no-modules error above).

Fail-fast: a non-zero step aborts the run with the step named in the
failure line (`stop failed`, `up failed`, `odoo run failed`); the summary
still reports the skipped commits. A final
`deploy complete update=N install=M skipped=K` closes the run.

**Projectless.** Like `i18n-pull`/`link`, `deploy` runs from a pure addons
repo: `projectlessOneShot` in `main.go`, cwd (or `-C`) as root. It never
touches a local docker stack.

**Log lines.** `echo.deploy[.sub]` family via the shared
`Log(level, sub, msg, db, fields…)` callback: `target resolved` →
`reading remote profile` → `commits selected n=…` → per-commit
`resolved <sha7> module=<m>` / `WARNING skipped <sha7> reason=…` →
`querying installed modules` → `plan update=… install=…` → step brackets →
final summary.

## Implementation

### `internal/cmd/deploy.go`

- `DeployOpts{Cfg, Root, Args, Palette, Log, StreamOut}`.
- `parseDeployArgs(args)` → `{from string, limit int, dryRun, force bool}`
  (unknown flag / bad `--limit` → usage error).
- `gitRecentCommits(ctx, root, n)` → `[]commit{sha, subject}`;
  `gitCommitModules(ctx, root, sha)` → changed top-level dirs.
- `resolveCommitModule(root, c)` implementing the two-step scheme;
  `isAddonDir(root, name)` checks `__manifest__.py`.
- `RunDeploy(ctx, opts)`: resolve remote → commits picker → module
  resolution → remote installed query → plan/dry-run/prod gate → the
  three streamed steps → summary.

### `internal/repl/deploy.go` + wiring

- `runDeploy` mirroring `runI18nPull`/`runLink` (startLog, stats stream,
  finalize/commandFailureLog).
- `Registry`, `dispatchNames` (one-shot eligible), `commandFlags["deploy"]
  = {"--from", "--limit", "--dry-run", "--force"}`, `helpSections`
  (docker/remote group), `projectlessOneShot` in `main.go`.

## Dependencies

- Unit 60 (`runSSHStream`, the `link` binding) — must land first.
- none external (git via `os/exec`, reuses connect/i18n-pull plumbing and
  `internal/odoo` builders).

## Verify when done

- [ ] From a linked addons repo, `deploy` lists the last N commits,
      multi-select works, and the resolved plan splits modules into
      update/install according to the remote `ir_module_module` state.
- [ ] A commit titled `[FIX] my_module: …` resolves by subject; a commit
      with a non-addon subject but touching exactly one module dir
      resolves by diff; a commit touching two modules (or none) is
      skipped with a `WARNING` and listed in the summary.
- [ ] `--dry-run` performs the reads (profile, installed modules) and
      prints the plan but issues no `stop`/`up`/odoo run.
- [ ] Remote stage `prod` requires confirmation (or `--force`); non-TTY
      fails closed (exit 2).
- [ ] The three remote steps stream live through the Odoo log styling;
      a failing step aborts with a named failure line and non-zero exit.
- [ ] `parseDeployArgs`, `resolveCommitModule` (both schemes + skip
      cases), and the install/update split are unit-tested with fixtures.
- [ ] `go build/vet/test ./...` pass; `registry`/`commandhl` cross-checks
      stay green; `CHANGELOG.md` `[Unreleased]` gets an `Added` entry.
