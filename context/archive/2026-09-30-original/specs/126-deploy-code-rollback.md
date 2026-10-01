# Unit 126: code rollback — a failed deploy puts the code back as it was

Depends on Unit 124 (the lock). Works with Unit 125 sources but does not need it.

## Goal

When a deploy fails, `--rollback-on-fail` restores the **database** from the
checkpoint. The **code** only comes back on git-deploy targets, and only the
deploy branch (`CheckpointEntry.CodeSHA`):

- On an **rsync target** the new code stays in the destination after a
  rollback. The database is back at the old schema and the code is the new
  one, so the target is left in a state that never existed and that the next
  restart or image build turns into a second incident.
- On a **git-deploy target** the dirty overlay (and the Unit 125 `ref`
  overlays) is not part of `CodeSHA`, so it is not restored either.
- Without a DB checkpoint (the default on dev), a failed deploy restores
  nothing at all, not even the code.

This unit makes the code part of every rollback: before a run writes any code,
Echo snapshots what it is about to overwrite, on the server, and restores it
when the run fails, with or without a DB checkpoint.

## Why a snapshot on the server and not "re-ship the previous SHAs"

The lock (Unit 124) says what was there before, but it cannot always rebuild
it: a `worktree` entry is content that only existed on someone's disk, and a
module that was never shipped by Echo has no entry at all. A copy taken on the
server right before the write restores exactly what was there, whatever its
origin, and needs nothing from the local checkout. The lock is saved with the
snapshot so the restore also puts the record back.

## Behavior

### A. The code snapshot

Taken in `runPush`, after the `pre_push` actions and **before the first code
write** (branch advance or rsync). Skipped on a dry-run (the plan prints
`code snapshot modules=… dest=…` instead) and under `--no-rollback-on-fail`
(nobody will restore it).

- **What**: the destination directory of every module the run will rsync
  (rsync targets: every pushed module; git targets: the overlay modules),
  plus `<remote_path>/.echo/lock.json`. A destination that does not exist yet
  (a module being installed) is recorded as **absent**, so the restore deletes
  it.
- **Git branch**: the pre-advance HEAD keeps being captured as today
  (`gitPreCodeSHA`), now **regardless of whether a DB checkpoint is on**.
- **Where**: `<remote_path>/backups/code/<name>.tar.gz` plus
  `<name>.json` (modules, destination per module, absent list, branch SHA).
  Same `backups/` tree as the dump checkpoints. `<name>` is
  `code-<db>-<UTC timestamp>`. `backups/code/` ignores itself the same way as
  the lock directory (Unit 124): a `.gitignore` with `*` written alongside.
- **Failure to create it** aborts the deploy before any code is written: the
  whole point is that a failure can be undone, and nothing has changed yet.

### B. When the code is restored

Any failure **after the first code write** triggers the rollback decision:
a push that fails half-way, a failing `post_push` action (an image build), a
failed `stop`/`up -d`, a failed `-u` or a verify hit. A failure before the
first write (lint, `pre_push`, snapshot creation) has nothing to restore.

The decision is the existing `rollbackDecision`, unchanged:
`--rollback-on-fail` / `--no-rollback-on-fail` win, then `--force` means roll
back, then a TTY asks, then headless rolls back. What changes is the scope:

| DB checkpoint | Rollback restores |
|---|---|
| on | database **and** code |
| off | code only. The confirm and the log line say the database is **not** restored. |
| on, but the failure came before it was taken (push, `post_push`, `stop`) | code only: the database was not touched yet. |

`post_deploy` failures keep today's rule: the deploy was verified green, no
rollback.

### C. Restore order

1. Stop the app (as today; skipped when the failure happened before it ran).
2. Restore the database, when there is a checkpoint (as today).
3. Restore the code:
   - git targets: move the branch back to the captured SHA (`gitRestoreCode`);
   - extract the snapshot over the destinations, after deleting each snapshot
     module directory first (so files the failed push added are gone), and
     delete the directories recorded as absent;
   - put the saved lock back.
4. Re-run the `pre_push` and `post_push` actions, so a target that builds an
   image from the destination rebuilds it from the restored code. Skipped with
   `--no-actions`, like any other action.
5. `up -d`.

A code-restore failure is logged as ERROR with what is left on the server; it
never masks the original deploy error (same as the git code restore today).

### D. Keeping and pruning snapshots

- **Green deploy**: the snapshot is linked to the run's checkpoint entry when
  there is one (`CheckpointEntry.CodeSnapshot`), so a later `deploy --rollback`
  restores database and code together. Without a checkpoint it is deleted.
- **Failed deploy, rolled back**: deleted after a successful restore.
- **Failed deploy, rollback declined**: kept and recorded as a checkpoint
  entry with method `code` (no DB part), so `deploy --rollback` can restore it
  later. It is pruned with the other checkpoints (same `keep`).
- `deploy --rollback` on an entry restores whatever it carries: DB, code, or
  both, in the order above.

## Implementation

| File | Change |
|---|---|
| `internal/cmd/deploy_codesnap.go` (new) | `createCodeSnapshot(ctx, rsc, dests)` (one remote `tar` over SSH, absent list, lock copy, sidecar JSON), `restoreCodeSnapshot`, `destroyCodeSnapshot`; dest resolution shared with `pushModuleSet` |
| `internal/cmd/deploy.go` | Snapshot in `runPush` before the first write; a `codeWritten` flag; every failure after it goes through the rollback path, including with checkpoints off; restore order; the confirm/log text for code-only |
| `internal/cmd/push.go` | Split destination resolution out of `pushModuleSet` so the snapshot knows every dest before the first rsync |
| `internal/config/checkpoints.go` | `CheckpointEntry.CodeSnapshot`; method `code` |
| `internal/cmd/checkpoint_remote.go` | `restoreCheckpoint` / `destroyCheckpointObject` / prune handle the code part and `code`-only entries |
| README.md, CHANGELOG.md | Rollback section: code is restored on every topology, code-only rollback without a checkpoint; `[Unreleased]` → Added + Changed |

**Tests** (transport seams only): snapshot command built for rsync and git
targets, absent dirs recorded; creation failure aborts before any write;
failure before the first write restores nothing; `-u` failure with and without
a checkpoint restores the code (and the DB only with one); a `post_push`
failure restores the code and re-runs the push actions; `--no-rollback-on-fail`
takes no snapshot; declined rollback records a `code` entry and
`deploy --rollback` restores it; green run links or deletes the snapshot;
restore failure keeps the original error.

## Out of scope

- Restoring code for the standalone `push` (it has no failure point after the
  write that Echo controls).
- Snapshots of the whole addons tree: only the directories the run writes.

## Verify when done

- [ ] On an rsync target with checkpoints off, a deploy whose `-u` fails
      leaves every pushed module's directory byte-identical to before, and a
      module that was being installed is gone.
- [ ] With a checkpoint, the same failure restores DB and code together.
- [ ] On an image target, a failing `post_push` build restores the
      destination and rebuilds from it.
- [ ] On a git target, branch and overlay both come back.
- [ ] `deploy --dry-run` prints the snapshot plan and creates nothing.
- [ ] A declined rollback is restorable later with `deploy --rollback`.
- [ ] `go build ./...` and `go test ./...` pass; README + CHANGELOG updated.
