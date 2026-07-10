# Unit 89: `deploy` — DB checkpoint & rollback + `checkpoint` command

## Goal

`deploy` can take a **server-side checkpoint of the remote database** right
before the Odoo `-i/-u` run and, when the run fails, **roll the database
back** to that checkpoint — automatically in headless mode, after a
confirmation in interactive mode. Two checkpoint methods: `db`
(`CREATE DATABASE … TEMPLATE`, fast file-level copy, the default) and
`dump` (`pg_dump -Fc` kept on the server, slower but low disk peak). A new
`checkpoint` command lists, sizes, creates and deletes checkpoints per
target, and `deploy --rollback` restores the last checkpoint after the
fact. `watch` inherits everything.

## Design

**Checkpoint wraps the only step that corrupts an Odoo DB.** Deploy's
danger is the `odoo -i/-u --stop-after-init` run (migrations/data), not
rsync or the restart. The checkpoint is taken **after `stop`** — the
containers are down, so the source DB has no sessions, which is exactly
the precondition `CREATE DATABASE … TEMPLATE` needs. New step order:
plan → prod gate → `--push` → `stop` → **checkpoint** → `up -d` →
odoo run → **verify**. On checkpoint-creation failure the deploy aborts
*before* touching the DB (fail-closed); nothing to roll back.

**Two methods, one flag.**
- `db` (default): `TerminateConnections` on the source, then
  `CREATE DATABASE "<ckpt>" TEMPLATE "<db>" OWNER "<user>"`, adding
  `STRATEGY FILE_COPY` when `SHOW server_version_num` ≥ 150000 (PG 15's
  default `WAL_LOG` is slow for big DBs). Rollback = drop broken DB +
  `ALTER DATABASE "<ckpt>" RENAME TO "<db>"` — near-instant, consumes the
  checkpoint.
- `dump`: `pg_dump -Fc -U <user> <db>` **inside the remote DB container**
  redirected to `<remotePath>/backups/checkpoints/<db>_<ts>.dump` (dir
  created on demand; mirrors the local `./backups/` convention). Rollback
  = terminate → drop → create → `pg_restore --no-owner --role=<user>`
  from that file; the dump survives its own restore.

Checkpoint names: `<db>__ckpt_<YYYYMMDD_HHMMSS>` (DB) /
`<db>_<YYYYMMDD_HHMMSS>.dump` (file). Multiple checkpoints per target can
coexist; retention prunes old ones.

**When it fires (no flags): stage decides.** Resolved remote `stage` ∈
{staging, prod} → checkpoint on; dev → off. Overrides, strongest first:
`--no-checkpoint` / `--checkpoint[=db|dump]` (mutually exclusive, usage
error together) → project `[checkpoint]` → global `[checkpoint]` → stage
default. New config section (global + per-project, project wins):
`[checkpoint]` with `mode = "auto"|"on"|"off"` (default `auto` = stage
rule), `method = "db"|"dump"` (default `db`), `keep = N` (default 2).

**Verify: exit code is not enough.** The odoo run fails the deploy if the
exit code is non-zero **or** the streamed output matched any of:
`CRITICAL`, a `Traceback (most recent call last)` line, `Failed to load
registry`. Matching lines are counted by a scanner wrapped around the
existing `StreamOut` path (reuse the level parsing already done for
coloring where possible); a zero-exit run with matches logs
`echo.deploy.verify: run reported errors — treating as failed` and takes
the failure path. Only the checkpointed run gets rollback; with
checkpoint off, behavior is exactly today's (abort with error).

**Failure path: ask in TTY, act headless.** On a failed run with a live
checkpoint:
- Interactive (TTY, no `--force`): red `huh.Confirm` — "Run failed.
  Roll back <db> to checkpoint <name>?". Yes → rollback. No → leave the
  broken DB *and* the checkpoint in place, print
  `echo.deploy.rollback: skipped — restore later with deploy --rollback`
  and exit non-zero.
- Headless (`--force` or no TTY — watch always): rollback automatically.

Rollback sequence (both methods): `stop` → terminate connections → drop
the broken DB → restore (rename / pg_restore) → `up -d`. Frames:
`echo.deploy.rollback: restoring checkpoint db=<db> method=<m>` and a
closing `database restored took=Ns`. **SHAs from a rolled-back deploy are
never marked deployed** (they stay pending for `deploy --auto` retry).

**Success path: keep the checkpoint, prune the tail.** After a passing
run, `MarkDeployed` as today, record the checkpoint metadata (with the
deployed SHAs), then apply retention: keep the newest `keep` checkpoints
for the target, drop/delete the rest (frame per pruned item under
`echo.deploy.checkpoint`).

**`deploy --rollback` — the "it passed but broke prod" escape.** Restores
a checkpoint *outside* a deploy: resolves the target, loads its
checkpoints (newest first), verifies existence remotely; with a TTY and
>1 checkpoint offers a log-framed picker, headless uses the newest.
Always a red confirm (skippable with `--force`; data captured since the
checkpoint is lost — say so); **age warning**: if the chosen checkpoint
is older than 1 h, the confirm text adds the exact age in red. After
restoring, `UpdateDeployedMarks` un-marks the SHAs recorded in that
checkpoint's metadata so the commits can be redeployed. `--rollback`
rejects being combined with selection flags (`--commits`, `--modules`,
`--auto`, `--push`).

**Metrics on every creation.** The checkpoint frame reports what it cost:
`echo.deploy.checkpoint: created db=<db> method=db size=4.2G took=6s`
(size via `pg_database_size` for `db`, file size for `dump`).

**Disk preflight.** Before creating: read `pg_database_size(<db>)` and
free space on the DB volume (`df -Pk` on the data dir inside the DB
container). Required free: `db` → 1.2× DB size; `dump` → 0.5×. Short →
abort the deploy pre-`stop`… — no: preflight runs **before `stop`** so a
doomed deploy never takes the service down; the error frame names both
numbers and suggests `checkpoint rm` / `--no-checkpoint`.

**`checkpoint` command — measure and clean.** New command, remote-target
scoped (same `--from <t>`/link resolution as deploy):
- `checkpoint list [--from <t>] [--json]` (default subcommand): aligned
  table — name, method, size, age, deploy SHAs (short) — plus a context
  footer: live DB name + `pg_database_size`, and free/total disk on the
  DB volume. That answers "how heavy is the system". Local metadata is
  reconciled against the server: entries whose DB/file no longer exists
  are flagged `stale` (and pruned from metadata); untracked
  `*__ckpt_*` DBs found remotely are listed as `orphan`.
- `checkpoint create [--from <t>] [--method db|dump]`: manual checkpoint
  (before a risky script/config change) using the exact deploy machinery
  — including `stop` → create → `up -d` for `db` (template copy needs no
  connections; say so in the confirm), preflight, metrics, retention.
  `dump` method needs no stop (`pg_dump` is MVCC-safe) — skip the
  restart for it.
- `checkpoint rm [<name>] [--all] [--from <t>] [--force]`: delete one
  (picker when unnamed + TTY; `ErrNonInteractive` otherwise), or all for
  the target. Red confirm unless `--force`. Removes the remote object
  *and* the metadata entry; also accepts `orphan` names.
- Prod targets: `create`/`rm` gate behind the existing red prod confirm.

**Storage model.** Checkpoint metadata lives in
`~/.config/echo/checkpoints/<projectKey>.toml`, keyed by
`DeployTargetKey` (same shape as `deploy-history`): list of
`{name, method, db, created_at, deploy_shas, dump_path}`. Nothing is
written to the local repo; remote artifacts live only in Postgres or
under `<remotePath>/backups/checkpoints/`.

**`watch` inherits.** Each cycle's headless deploy checkpoints per the
same resolution (watch targets are staging/prod-gated already); a failed
cycle rolls back automatically, logs ERROR, and the loop keeps polling
(existing semantics). New pass-through flag `watch --no-checkpoint` for
when the copy makes cycles too slow. The `watch stopped` summary gains
`rollbacks=N`.

**Out of scope (documented as known limitations):** filestore snapshot
(a `-u` may write assets; DB rollback can orphan new files — harmless —
or miss overwrites; hardlink `cp -al` snapshot is phase 2), local
(non-remote) checkpoints, and scheduled/periodic checkpoints.

## Implementation

### `internal/config/checkpoints.go` (new)

- `CheckpointEntry{Name, Method, DB, CreatedAt, DeploySHAs, DumpPath}`;
  `LoadCheckpoints(projectKey, targetKey)`, `SaveCheckpoints(…)`,
  `AddCheckpoint`, `RemoveCheckpoint` — atomic writes, modeled on
  `deploy_history.go`.
- `CheckpointConfig{Mode, Method, Keep}` merged into `config.Config`
  (global `[checkpoint]` + project `[checkpoint]`, project wins,
  defaults `auto`/`db`/`2` in `applyDefaults`).

### `internal/cmd/checkpoint_remote.go` (new — shared remote primitives)

Thin helpers over `remoteDBCmd` + `runSSH`/`runSSHStream` (buffered for
scalars, streamed for long ops), all taking the resolved
`remoteShellContext`:
- `remotePGVersionNum`, `remoteDBSize`, `remoteDiskFree` (`df -Pk` of the
  data dir inside the DB container), `remoteTerminateConns`,
  `remoteCreateFromTemplate` (adds `STRATEGY FILE_COPY` ≥ 150000),
  `remoteRenameDB`, `remoteDropDB`, `remoteListCkptDBs` (catalog query
  for `%__ckpt_%`), `remoteDumpToFile`, `remoteRestoreDump`,
  `remoteRemoveFile`, `remoteFileSize`.
- `createCheckpoint(ctx, opts, rsc, method) (CheckpointEntry, error)` and
  `restoreCheckpoint(ctx, opts, rsc, entry) error` — the two composites
  deploy, `--rollback` and `checkpoint create` all share. Both emit the
  metric frames (`size=`, `took=`).
- Package-level seams (`var …`) for the exec-shaped funcs so tests can
  script them.

### `internal/cmd/deploy.go`

- `deployArgs` gains `checkpoint string` (`""`/`"db"`/`"dump"`),
  `noCheckpoint bool`, `rollback bool`; `parseDeployArgs` accepts
  `--checkpoint[=db|dump]`, `--no-checkpoint`, `--rollback` (validate
  exclusivity; `--rollback` rejects selection/push flags).
- `resolveCheckpointMode(args, cfg, stage) (enabled bool, method string)`
  — pure, unit-tested: flag > project cfg > global cfg > stage default.
- `RunDeploy` wiring: preflight (size vs free) before `stop`; checkpoint
  step between `stop` and `up -d` (`--dry-run` plan gains a
  `checkpoint method=<m>` line); wrap the odoo-run `StreamOut` in a
  failure-pattern scanner; on failure with checkpoint → confirm/auto
  rollback path; on success → metadata + retention prune. `MarkDeployed`
  only on verified success.
- `--rollback` short-circuits into `runDeployRollback` (target resolve →
  picker/newest → age-aware red confirm → `restoreCheckpoint` →
  `UpdateDeployedMarks` un-mark → metadata cleanup).
- `DeployResult` gains `Checkpoint *CheckpointInfo{Name, Method, Size,
  TookSec}` and `RolledBack bool` (surfaced in `--json`).

### `internal/cmd/checkpoint.go` (new)

- `RunCheckpoint(ctx, opts, args)` + `parseCheckpointArgs`: subcommands
  `list` (default) / `create` / `rm`; flags `--from`, `--json`,
  `--method`, `--all`, `--force`. Target resolution via
  `resolveRemoteShell`. List reconciles metadata ↔ server (`stale`,
  `orphan`) and prints the size/disk footer. TTY guards per invariant 9;
  prod confirms for `create`/`rm`.

### `internal/cmd/watch.go`

- `parseWatchArgs` accepts `--no-checkpoint`; `deployCommitsHeadless`
  appends it when set. Track `rollbacks` from `DeployResult.RolledBack`
  and add `rollbacks=N` to the summary frame.

### `internal/repl/` + registration

- Wrappers `internal/repl/checkpoint.go` (and `--rollback` handling in
  the existing deploy wrapper): map errors to exit codes, stream via
  `emitStreamLine`.
- Registry + `dispatchNames` + dispatch cases for `checkpoint`;
  `commandFlags`: `deploy` += `--checkpoint`, `--no-checkpoint`,
  `--rollback`; `watch` += `--no-checkpoint`; new `checkpoint` entry.
  Help rows for all of the above; `checkpoint` is one-shot eligible
  (`IsScriptCommand`), not offered in `sequence`.

### `README.md` / `CHANGELOG.md`

- README: `checkpoint` row + deploy/watch flag rows; prose paragraph in
  the deploy section (checkpoint → verify → rollback story, methods,
  stage defaults, disk cost). CHANGELOG `[Unreleased]` `### Added` (in
  Spanish, house style), same commit as the code.

### Tests

- `parseDeployArgs`/`parseCheckpointArgs`/`parseWatchArgs` flag matrices
  (exclusivity, `--checkpoint=dump`, `--rollback` rejections).
- `resolveCheckpointMode` table test (flags × config × stage).
- Failure-pattern scanner: CRITICAL / Traceback / registry lines flip a
  zero-exit run to failed; INFO/WARNING lines don't.
- `createCheckpoint`/`restoreCheckpoint` against scripted seams: db vs
  dump command shapes, `STRATEGY FILE_COPY` gated on version, ordering
  (terminate before drop, drop before rename), metric frames emitted.
- Preflight: short disk aborts before any `stop` seam call.
- Retention: creating with `keep=2` prunes the oldest, metadata matches.
- Rollback un-marks exactly the checkpoint's SHAs
  (`UpdateDeployedMarks`).
- Metadata store round-trip + reconcile (`stale`/`orphan`) logic.

## Dependencies

None new — reuses the deploy pipeline (61/65/78), the SSH transports and
remote exec helpers (`runSSH`, `runSSHStream`, `remoteDBCmd`), the
`db-pull` dump-over-SSH patterns (85), and the postgres helpers'
SQL shapes (`internal/docker/postgres.go`).

## Verify when done

- [ ] `deploy` to a staging target with no flags takes a checkpoint
      between `stop` and `up -d`, logging `created … method=db size=…
      took=…`; the same deploy to a dev target takes none.
- [ ] A deploy whose odoo run exits non-zero (or exits 0 while printing a
      `Traceback`) prompts for rollback in a TTY; accepting leaves the
      remote DB byte-identical to pre-deploy (spot-check
      `ir_module_module` versions), containers up, and the commits still
      undeployed for `deploy --auto`.
- [ ] The same failure under `watch` (headless) rolls back with no
      prompt, logs ERROR, keeps polling, and the final summary shows
      `rollbacks=1`.
- [ ] `deploy --rollback` on a target with two checkpoints offers a
      picker, warns with the exact age when >1 h old, restores, and
      un-marks the SHAs so a re-deploy re-offers them.
- [ ] `--checkpoint=dump` produces a `.dump` under
      `<remotePath>/backups/checkpoints/` and its rollback restores via
      `pg_restore`, keeping the file.
- [ ] `checkpoint list` shows name/method/size/age plus the live DB size
      and disk-free footer; `--json` is clean on stdout; a manually
      dropped checkpoint DB shows as `stale`, an untracked `__ckpt_` DB
      as `orphan`.
- [ ] `checkpoint create` / `checkpoint rm --all` work end-to-end
      (create restarts containers for `db`, not for `dump`; rm cleans
      server + metadata); both red-confirm on prod.
- [ ] Insufficient disk aborts the deploy before `stop` with both
      numbers in the message.
- [ ] Retention `keep=2`: a third checkpoint prunes the oldest,
      with a frame saying so.
- [ ] Help/registry/autocomplete show the new command and flags; README
      and CHANGELOG updated in the same commit.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/cmd/...
      ./internal/repl/... ./internal/config/...` pass.
