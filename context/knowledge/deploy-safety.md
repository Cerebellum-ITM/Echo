# Deploy safety
> Owns what makes a deploy undoable and inspectable: checkpoints, the rollback decision, DB+code rollback, the code snapshot, the deploy lock, the dependency check, and deploy actions. The flow itself (selection, sources, step order, push, config precedence) is in [deploy.md](deploy.md). Last verified: 2026-09-30.

Code authority: `internal/cmd/deploy.go` (`handleDeployFailure`, `rollbackDecision`, `runDeployRollback`), `checkpoint*.go`, `deploy_codesnap.go`, `deploy_lock.go`, `deploy_depcheck.go` (with the pure `internal/depcheck`), `deploy_actions.go`, `actions*.go`, `internal/config/checkpoints.go`. Live-verification status of everything here (most of it ran only against a fake `ssh`) is tracked in [operations.md](operations.md), not repeated.

## Context: the 2026-09-30 incident

Units 124-126 and 128 exist because of one incident on `habitta_prod`. Two targets (`habitta_dev`, `habitta_prod`) shared one build cache directory (`.cache/all_odoo`). A partial deploy shipped one module's code (at the time a commit selection on an rsync target shipped the module's working tree, not the commit) and removed a method that a module left behind on the server still used. Afterwards nobody could ask the target which version of a module (`ccima_flow_mail`) it ran, a rollback restored only the DB so the code stayed new against the old schema, and Echo had no way to put the code back. The answers shipped: ship the commit's tree ([deploy.md](deploy.md#module-sources), Unit 125), the [lock](#deploy-lock) (Unit 124), the [code snapshot](#code-snapshot) (Unit 126) and the [dependency check](#dependency-check) (Unit 128; the removed method was `_get_promotion_from_sale_order` in `ccima_crm_reassign`, still called by `ccima_flow_mail`). Sources: specs 124-128, user briefing 2026-09-30.

## What a failed deploy leaves, and what comes back

Every failure from the first code write on goes through `handleDeployFailure` (see [step order](deploy.md#step-order)). What is restored depends on where it failed:

| Failure point | Code | Database |
|---|---|---|
| `pre_push`, or anything before the first code write (and no checkpoint yet) | nothing to undo | untouched |
| after the code write, before the checkpoint exists (`post_push`, disk preflight, `pre_deploy`, `stop`, checkpoint creation) | restored | NOT restored; the log says so |
| after the checkpoint (`up -d`, Odoo run, verify, tests) | restored | restored from the checkpoint |
| `post_deploy` | kept | kept: a verified-green deploy is never undone for a hook |

Without push there is no code to restore; a checkpoint alone can still restore the DB. A failed run's commits are never marked deployed. The code is part of every rollback with or without a DB checkpoint (default on dev); `--no-rollback-on-fail` takes no snapshot.

## Checkpoints

A checkpoint is a restore point of the target's database taken inside a deploy (or manually), with the app stopped.

- **Policy**: `mode` auto|on|off, `method` db|dump, `keep` N. Resolved server-first, field by field, then local, then defaults `auto/db/2` ([precedence](deploy.md#config-precedence)). `auto` = on for stage staging/prod, off for dev, using the stage read from the server profile. Per run: `--checkpoint[=db|dump]` / `--no-checkpoint` (exclusive).
- **When**: after the app is stopped, before `up -d`. Only the Odoo app service is stopped (`remoteStopApp`, `compose stop <odoo>`): `psql`/`pg_dump` run INSIDE the db container, so a full stop makes the checkpoint fail (`service "db" is not running`, found on habitta_prod).
- **`db` method**: terminate connections on the live DB, then `CREATE DATABASE <name> TEMPLATE <db>`, with `STRATEGY FILE_COPY` when PG ≥ 15 (the default WAL_LOG strategy is slow). Name `<db>__ckpt_<yyyymmdd_hhmmss>`, db part truncated so the whole stays within 63 bytes (`ckptDBName`). The copy gets `ALLOW_CONNECTIONS false` so Odoo's DB selector hides it (best-effort, WARNING on failure); restore re-enables connections on the live DB.
- **`dump` method**: `pg_dump -Fc` to `<remote_path>/backups/checkpoints/<db>_<ts>.dump`; a manual `checkpoint create --method dump` runs live without stopping the app.
- **Disk preflight** (`checkpointPreflight`, `internal/cmd/checkpoint_remote.go:441`): the need is `checkpointNeed(size, method)` (`:481`), ~1.2x the DB size for `db`, ~0.5x for `dump`, measured where each method writes (`checkpointFreeBytes`, `:468`): `db` on the cluster's `data_directory` (`df` inside the DB container), `dump` on the host filesystem of `<remote_path>` (`df -Pk` over SSH), because `remoteDumpToFile` redirects `pg_dump` on the host. A failure names both numbers and suggests `--no-checkpoint` or `checkpoint rm`. If size or free space cannot be measured it warns and proceeds. It runs AFTER the push (see step order), so a refusal still restores the code. `doctor`'s `disk` check uses the same need and the same filesystems: failed when a deploy would checkpoint (`resolveCheckpointMode(deployArgs{}, …)`) and the disk is short, a warning when checkpoints are off; skipped on Reverb targets (Unit 131).
- **Index**: LOCAL `~/.config/echo/checkpoints/<projectKey>.toml`, keyed by the same `DeployTargetKey` as deploy history; the objects live on the server. Entry: `name`, `method` (`db|dump|code`), `db`, `created_at`, `deploy_shas`, `dump_path`, `code_sha` (git target's branch HEAD before the advance), `code_snapshot`. Because the index is local, another machine does not see this machine's checkpoints (`deploy --rollback` there reports none recorded).
- **Retention**: after a deploy records its entry, the newest `keep` survive; older entries are destroyed (copy DB or dump file, plus their code snapshot) and unindexed (`pruneCheckpoints`, best-effort).
- **`checkpoint list`** (default) reconciles against the server: an entry whose object is gone is `stale` and is dropped from the index; an untracked `*__ckpt_*` database is shown as `orphan` (never removed automatically). `checkpoint create [--method]` is manual (prod-gated; `db` method stops the app, then runs `up -d`), `checkpoint rm [name|--all] [--force]` behind a red confirm, `--json` on list. Reverb environments keep snapshots server-side and never touch this index (see [remote-targets.md](remote-targets.md)).
- **Not covered**: the filestore is not snapshotted. Rollback restores DB and code, not attachments written by the failed run. No unit exists for it.
- **Fail-closed**: a checkpoint creation failure aborts the deploy before the Odoo run.

## Rollback decision

Order (`rollbackDecision`, pure): `--rollback-on-fail` / `--no-rollback-on-fail` (explicit pair, exclusive) › `--force` (roll back) › TTY: red confirm (`confirmRollback`; declining keeps the broken DB or new code for inspection) › headless: roll back.

- Why the flags: an agent in a pty looks like a TTY and would hang on the prompt.
- `watch` always rolls back (its inner deploy carries `--force`; the flag pair is not threaded through). No config key exists for this decision: an unattended monitor must never leave a broken DB.
- The flags govern the code restore too; `--no-rollback-on-fail` also suppresses the snapshot (nobody would restore it).
- A declined rollback is not lost: the leftovers are recorded as a restore point (`restorePoint`): the DB checkpoint (with `code_sha`/`code_snapshot` attached) or, when no DB checkpoint exists, a `code`-method entry, restorable later with `deploy --rollback` and pruned with `keep`. If there is nothing to restore (no checkpoint, no snapshot, no branch move) the decision is skipped.

## DB and code rollback

On acceptance `handleDeployFailure` runs, in this order:

1. Stop the app (only it) if the run had touched it or a checkpoint exists.
2. Restore the DB from the checkpoint with `consume=true` (the checkpoint is renamed over the live DB; the point of the checkpoint is served). Without a checkpoint it warns `restoring the code only`. If the DB restore itself fails: ERROR, the checkpoint entry (with its code parts) is kept, and the function returns a combined error WITHOUT restoring code and with the app left stopped.
3. Restore code (`restoreDeployedCode`): git target, the branch moves back to the pre-advance `CodeSHA` with no FF gate and provenance cleared (`gitRestoreCode`); then the [snapshot](#code-snapshot) is extracted (covered paths deleted first). Failures are logged ERROR and listed, never returned, so the original deploy error is never masked.
4. Re-run `pre_push`/`post_push` actions so an image-built target rebuilds from the restored code (skipped under `--no-actions`; a failure is ERROR only).
5. `up -d`.
6. Bookkeeping: whatever was not restored is re-recorded as a restore point; the snapshot is destroyed when its code came back; `RolledBack` is set (watch counts it); a final `rolled back — commits not marked deployed` line carries `database=` and `code=`.

`deploy --rollback` is the standalone version (`runDeployRollback`): newest entry, or a picker when several and a TTY (so the newest may be a `code` entry, which restores code only); red confirm with the entry's age (age over 1 h printed in red as a data-loss warning; `--force` skips); stop app, restore DB, restore code from `code_sha`/`code_snapshot` (+ re-run push actions), `up -d`, un-mark the entry's `deploy_shas` from deploy history.

- **`--rollback` PRESERVES a `db` checkpoint**: it copies back through `CREATE DATABASE … TEMPLATE` so the same point stays restorable (needs about one extra DB of disk; the just-dropped broken DB frees it). `--consume-checkpoint` opts into the cheaper rename that destroys it. The automatic on-failure rollback consumes. A `dump` entry is never consumed. `--consume-checkpoint` outside `--rollback` is `ErrUsage`.
- A `post_deploy` failure never reaches this path.

## Code snapshot

A server-side copy of exactly what a deploy is about to overwrite (`deploy_codesnap.go`, Unit 126).

- **Why a tarball and not "re-ship the previous SHAs from the lock"**: a `worktree` lock entry is content that only existed on someone's disk, and modules Echo never shipped have no entry. A tar taken right before the write restores what was there, whatever its origin, and needs nothing from the local checkout.
- **What**: the destination directory of every module the run rsyncs (worktree and archived modules) plus, on git targets, the overlay directories about to be reverted (released pins), plus `.echo/lock.json`. Modules that ride the git branch are covered by the branch SHA instead. No snapshot when there is nothing to tar.
- **Where**: `<remote_path>/backups/code/<name>.tar.gz` plus `<name>.json` sidecar (`name`, `dests`, `absent`); `<name>` = `code_<db>_<yyyymmdd_hhmmss>` (local time). The directory ignores itself (`.gitignore` with `*`), like `.echo/`. Paths are tarred relative to `/`; paths that did not exist are recorded as `absent` and deleted on restore (a module being installed vanishes).
- **When**: in `runPush`, after the destination is resolved and BEFORE the first write (pin release, branch advance, rsync), after `pre_push` actions. Skipped on `--dry-run` (a `code snapshot` plan line is printed) and under `--no-rollback-on-fail`. A creation failure aborts the deploy before any write.
- **Restore**: `rm -rf` of every covered path, then `tar -xP` (`-P` so extraction passes through symlinked parents; members are relative either way). `checkSnapshotPaths` refuses any path a `rm -rf` must not touch: it must be absolute, clean and at least two levels deep.
- **Lifecycle**: green deploy with a checkpoint links the snapshot to the entry (`CodeSnapshot`); green without one deletes it; rolled back (code restored) deletes it; declined rollback keeps it inside a restore-point entry. Standalone `push` takes no snapshot.

## Deploy lock

`<remote_path>/.echo/lock.json`: what code Echo shipped to the target and where it came from (`deploy_lock.go`, Unit 124).

- **Server-side, under `remote_path`**: anyone who reaches the target (another machine, an agent, a person over SSH) reads the same answer. `remote_path` is the one directory every target owns; the push destination can be shared between targets, so the lock cannot live there. Local deploy history stays; it answers a different question ([deploy.md](deploy.md#deploy-history-vs-the-lock)).
- **Self-ignoring**: `.echo/.gitignore` contains `*`, so the repo's own `.gitignore` is untouched, `git status` never lists it, and `push --clean` / `git clean` (no `-x`) never treat it as overlay. If the repo already TRACKS `.echo/`, the write script's `git ls-files -- .echo` detects it and a WARNING gives the fix (`git rm --cached -r .echo`); Echo never runs it.
- **Schema** (`schema: 1`): `target`, `base` (git targets: `branch`, `sha`, `ref`, `at`), `modules.<name>`: `source` (`worktree|commit|branch|ref`), `ref`, `sha`, `tree` (git tree id of the module dir at `sha`; omitted for `worktree`), `dirty`, `version` (manifest version AS SHIPPED), `dest`, `via` (`deploy|push|watch`), `at`, `by` (git user email), `verified`.
- **`worktree` sha is not the content**: it is the local HEAD at ship time; the module may carry uncommitted edits (`dirty`). For committed sources `tree` is the content identity.
- **`verified=false`** from the push until a green `-u` run marks it true. `push` alone leaves entries unverified.
- **Writers**: deploy (after the push; `verified` after verify), standalone `push` (`via=push`), `push --clean` (forgets the cleaned modules), `deploy --set-code` and `--restore-code` (rebase the base; branch-sourced entries are dropped, overlay entries kept only with `--keep-overlay`/restore), and rollback (the lock is in the snapshot, so a restore puts the record back).
- **Write procedure**: read-modify-write, temp file + `mv`. **Never fails a deploy**: a read or write failure is a WARNING; an unparsable file warns, reads as fresh and is replaced by the next write. `--dry-run` never writes.
- **Read states** (`lockState`, `internal/cmd/deploy_lock.go`): `found`, `absent`, `unreadable`, `corrupt`. `fetchDeployLock` (`:176`) runs `lockReadScript` (`:160`), which prints `@@absent` when the file does not exist, so an absent lock is told apart from a failed read (a permission error or an SSH failure, both `unreadable` with the error); the old `cat … 2>/dev/null || true` turned a permission error into "no lock"; the pure `parseDeployLock` (`:190`) tells `found` from `corrupt` (empty bytes read as `absent`). `readDeployLock` (`:207`) keeps its signature and warn-and-continue behaviour on top of both; units that need the state itself (`doctor`, `compare --targets`, and the saved plan of Unit 129) use the split directly (Unit 131).
- **Readers**: `deploy --lock [--json]` (read-only, no selection; mutually exclusive with deploy options), the `deploy lock modules=N unverified=N last=… overlay=…` line of `link --show` (`lockSummaryFields`, `deploy_lock.go:477`, also printed by `doctor`'s `lock` check, which warns on `unreadable`, `corrupt`, unverified entries and a repository tracking `.echo/`), the plan line per module (`ship=… locked=…`), `compare --targets <a>,<b>`, which sets two targets' locks side by side by module tree ([modules-and-odoo](modules-and-odoo.md#view-and-compare), Unit 130), and `DeployModule.source/sha/version` in `deploy --json` (so `logview --json` shows what shipped).
- **It records what Echo shipped, not what is on disk**: there are no content hashes and no drift detection; a hand edit on the server is invisible to it. A drift check before the build was considered and declined (2026-09-30), as was an exclusive push destination per target.
- **Exact sync**: content shipped from a commit tree or ref syncs with `--delete` scoped per module directory (per-module rsync, trailing slashes; excludes stay protected without `--delete-excluded`). Working-tree pushes stay no-delete; `push` without `--delete` never deletes.

## Dependency check

Before a pushing deploy ships, it looks for symbols the shipped modules drop while a module staying on the server still uses them (`checkDeployDependencies`, `internal/cmd/deploy_depcheck.go:52`, called right after the `code` plan lines, `internal/cmd/deploy.go:1420`; Unit 128). No push, no check.

- **Old vs new**: old = the module directory on the server at the push destination (`moduleDestDir`; for a module riding the git branch, its directory inside the checkout, `absGitDir`); new = the archive dir (`commit`/`ref`), the working tree (`worktree`), or for branch-riding modules the tree at `gitTip`, archived for the check only (`shippedModuleTrees`). A module whose server directory does not exist (being installed) is not compared.
- **Symbols** (`depcheck.Symbols`, regex, `tests/` and `migrations/` skipped): `def <name>(` and `<name> = fields.<Type>(` at a class body's indentation (nested defs and module-level functions are not members), and `id="…"` on `record`, `template`, `menuitem`, `act_window`, `report` (a dotted id overrides another module's record and is ignored).
- **Removed** (`depcheck.Removed`): in old, not in new, minus dunders, the ORM override denylist (`create`, `write`, `unlink`, `search`, `name_get`, `_compute_display_name`, `action_archive`, …), and methods/fields still defined by another module shipped in the same run (moved). A moved xml id is still reported: its qualified name changes.
- **Staying modules** = directories with a `__manifest__.py` next to any shipped module's destination that this run does not ship. A staying module that defines the removed method or field itself (a `def`/`fields.` line among its hits) is skipped for that symbol.
- **Transport**: two `runSSH` round trips through the seam `depRunSSH`: a tar (relative to `/`) of the shipped modules' `*.py`/`*.xml` plus the siblings' manifests, extracted with `extractTar`, which is also how the staying set is learned; then one script with one `grep -rnwHE --include='*.py' --include='*.xml'` per removed symbol over the staying dirs, each capped at 100 lines (`depGrepCap`) and preceded by an `@@<index>` marker. A use is the word `<name>`, or `<module>.<id>` for an xml id (unqualified local refs are not checked). Any failure, including destination resolution, logs `WARNING … dependency check skipped reason=…` and the deploy goes on.
- **Output**: one `WARNING echo.deploy.plan: dependency removed= kind= from= used_by= at=` per removed symbol in use, `at=` holding up to five `file:line` (`depShownRefs`) then `more=N`; `INFO … dependency check clean modules=<compared> removed=<removed, none in use>` otherwise. `--json`: `DeployResult.Dependencies` (`module, symbol, kind, used_by, file, line`; `file` is relative to the addons dir, so it starts with the module).
- **Blocking**: `dev` warns only. Any other normalized stage (`staging`, `prod`, undeclared, see [architecture-and-traps](architecture-and-traps.md#prod-gate-rule)) with findings on a real run asks `Deploy anyway? N symbols removed while still used` (`confirmDependencyRisk`), or fails closed without a TTY (`ErrNonInteractive`, exit 2); `--force` skips it. The gate sits after the dry-run return and before the test-on-prod and prod gates (`internal/cmd/deploy.go:1573`), so a block writes nothing. `--dry-run` never blocks; `watch` deploys with `--force`, so it only logs.
- **`--no-dep-check`** skips it for one run and logs `WARNING … dependency check skipped flag=--no-dep-check`; no config key, same reason as `--no-lint` ([decisions](../decisions.md)).
- **Limits** (by design): regex, not a parser, so no class awareness (a method removed from one model but defined on another class of the same module counts as present), no signature changes, no JS/OWL, no QWeb `t-call` of a template by short name; a word match can be a comment or an unrelated variable, so a common field name (`state`, `name`) produces noise, which is why it asks instead of refusing; modules baked into an image and absent from the server's filesystem are invisible. On git targets the branch advance moves every module in the tree, but only the selected modules are compared.

## Deploy actions

Named commands hooked into fixed points of a deploy (`deploy_actions.go`, `actions*.go`, Units 92, 93, 105). Motivating case: an image-built remote needs the code pushed into the build context and then a remote image build after the push.

- **Shape**: `[[deploy.actions]]` with `name` (unique), `phase` (`pre_push|post_push|pre_deploy|post_deploy`), `where` (`local|remote`), `exec_path`, `run` (executed with `sh -c`). `exec_path`: empty = root (`remote_path` for remote, project root for local), relative joins under that root, absolute as-is; stored as a plain path (the wizard's "Addons directory" preset resolves to a literal path). Validated at resolution (`ValidateDeployActions`), before any deploy step.
- **Environment**: `ECHO_STAGE`, `ECHO_DB`, `ECHO_REMOTE_PATH`, `ECHO_MODULES` (space-separated update+install set), `ECHO_PHASE`. Remote actions export them on the SSH command line.
- **Resolution is WHOLESALE, server-first**: `--no-actions` › non-empty server list › non-empty local list ([table](deploy.md#config-precedence)). Merging lists would give an unpredictable order across machines.
- **Failure semantics**: fail-fast within a phase. `pre_push`: abort, nothing to undo. `post_push` and `pre_deploy`: the code is already written, so they go through [rollback](#db-and-code-rollback) (code restored, push actions re-run). `post_deploy`: run marked failed, no rollback. The two push phases are skipped with an INFO when the run does not push. In `watch`, an action failure fails only that cycle.
- **Per-target authoring** (`actions add|edit|rm`): scope is chosen by flag. `--from <t>` / `--remote` read-modify-write THAT target's server profile (`~/.config/echo/projects/<ProjectKey(remote_path)>.toml` on the server, `uploadActionsToServer`; prod target = confirm via `confirmRemoteProd`, `--force` bypasses; the first server-scoped write creates the profile). No flag = the local project list, zero SSH (a bare `actions` opens no connection). `actions` (list) with a remote shows the effective list and which side wins.
- **Traps**: wholesale means an EMPTY server list falls back to the local list, so `actions rm --from` that empties it warns with the local count. Per-target actions are always representable because the server profile is keyed by `remote_path`; never upload one local list to several targets. Targets that share a build cache but differ in actions (habitta dev and prod) end up running each other's build steps when one list is pushed to both.
- Test seams: `actionRunLocal`, `actionRunRemote`, `actionsRunSSH`.

## Known gaps

- **The dependency check compares only the selected modules.** On git targets the whole branch tree advances ([deploy.md](deploy.md#module-sources)), so a module that rides along unselected can still drop something a staying module uses; see the [limits](#dependency-check).
- Filestore is outside checkpoints; a persisted rollback-on-fail default does not exist.
- The code rollback and the lock have never run against a real server; see the ledger in [operations.md](operations.md).
