# Unit 142 · op-lock-respect
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-20.

## Goal

Every command that changes a remote target's code or database takes the operation lock that `db-push` introduced
(`<remote_path>/.echo/op.lock.d`), so two of them never run against the same target at once and none of them runs
while a `db-push` replaces the database.

## Design

Today only `db-push` takes and checks the lock ([deploy-safety](../../knowledge/deploy-safety.md#operation-lock),
`internal/cmd/op_lock.go`). A `deploy`, a `watch` cycle, a `sideload` or a checkpoint restore started during a
`db-push` would write code or restore a database under it; two deploys from two machines can also interleave
their rsync, `-u` and lock writes.

Decisions:

1. **Who takes it** (acquire before the first remote write, release on every exit path with `defer`, including
   errors, cancellation and Ctrl+C):
   - `deploy` real runs, including `--apply`, `--rollback`, `--set-code`, `--set-branch` and `--test`;
   - each `watch` cycle that deploys (taken per cycle, not for the whole watch);
   - `sideload` real runs;
   - `checkpoint create`, `checkpoint restore` and `checkpoint rm`.
   `push` is not included: it only rsyncs module files and is the building block deploy uses.
2. **Who only reads it**: `deploy --dry-run` (and `--save-plan`) reads the lock and, when held, logs
   `WARNING echo.deploy.lock: target busy command=<c> host=<h> since=<t>`; the dry run still completes. Nothing
   else reads it.
3. **Nesting**: a command takes the lock once at its top level. Steps it runs internally (deploy's checkpoint,
   deploy's rollback after a failure, sideload's module install) never take it again. `db-push` keeps its own
   acquisition unchanged.
4. **Held lock**: the command fails before any write with the existing `errOpLockHeld` message
   (`target busy: <command> from <host> since <started>`), exit 1. `--force` keeps its db-push meaning here too:
   it takes over a lock older than 6 hours or one whose holder file cannot be read, with the existing WARNING;
   a younger lock is never taken over. `watch` already deploys with `--force`; a cycle that finds a young lock
   logs `WARNING echo.watch: target busy, cycle skipped command=<c> host=<h> since=<t>` and retries on the next
   poll instead of exiting.
5. **Holder record**: the `command` field names the top-level command as typed (`deploy`, `watch`, `sideload`,
   `checkpoint create`, `checkpoint restore`, `checkpoint rm`).
6. **Reverb**: the lock is a directory under `remote_path`, which every target has, so Reverb targets take it
   the same way. A Reverb checkpoint goes through the Reverb API but still takes the lock first.
7. **Release failure** is best-effort, as in `db-push`: a WARNING naming the lock directory, never an error that
   changes the command's exit code.

## Implementation

- `internal/cmd/op_lock.go`: a helper `withOpLock(ctx, rsc, command string, force bool, log logFn, fn func() error) error`
  that acquires, runs `fn`, and releases in a `defer`; `db-push` may keep its current calls.
- `internal/cmd/deploy*.go`, `watch.go`, `sideload*.go`, `checkpoint*.go`: wrap the write phase of each entry
  point listed in decision 1 with `withOpLock`, after argument validation, target resolution, dry-run returns and
  the confirms, and before the first remote write. Internal helpers they call are not wrapped.
- `deploy --dry-run`: read the lock with `readOpLock` and log the WARNING of decision 2.
- `watch`: map `errOpLockHeld` from a cycle to the WARNING of decision 4 and continue.
- Help: one note on `deploy`, `watch`, `sideload` and `checkpoint` (`internal/repl/helpdocs.go`) saying the
  command takes the target's operation lock and how `--force` treats a stale one.
- Tests through the existing seams (`ckptRunSSH`, the fake-`ssh` harness): a held lock makes each entry point
  fail before writing; release after success and after a failure; a stale lock with `--force`; nested deploy
  checkpoint does not re-acquire; a watch cycle skips on a held lock.
- Knowledge: [deploy-safety](../../knowledge/deploy-safety.md#operation-lock) "Only `db-push` takes and checks it
  today" becomes the list of decision 1.
- `CHANGELOG.md` `[Unreleased]` `Changed`.

## Out of scope

`push`, remote `update`/`install`/`uninstall`/`test`, showing the lock in `link --show` or `doctor`.

## Done

Build/vet/test green, help cross-checks green, CHANGELOG entry, ledger row 142 "live verification pending"
(a `deploy` refused while another session holds the lock on a dev target).
