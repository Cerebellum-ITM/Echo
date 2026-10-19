# Unit 141 · db-push
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-19.

## Goal

`echo_cli db-push <dump> --to <target> [--filestore [<tar>]] [--neutralize]` replaces a remote target's database
(and optionally its filestore) with a local `db-pull` dump, under a checkpoint and an operation lock, and leaves
Odoo running on the new data. `--dry-run` shows the whole plan and touches nothing on the server. The first use is
resetting a dev instance from a staging snapshot; the same command later loads a new production instance (with
`--force`).

## Design

Why: Echo pulls remote databases (`db-pull`) and restores them locally (`db-restore`), but nothing loads a dump
into a remote target, and agents in this workflow may only reach instances through `echo_cli`. The pieces exist
in the checkpoint code (`remoteTerminateConns`, `remoteDropDB`, `remoteCreateDB`, `remoteRestoreDump`,
`createCheckpoint`, `checkpointPreflight` in `internal/cmd/checkpoint_remote.go`); this unit composes them into one
guarded flow ([deploy-safety](../../knowledge/deploy-safety.md), [database](../../knowledge/database.md)).

Decisions (the implementer makes none):

1. **CLI.** `db-push <dump> (--to <target> | --remote) [--filestore[=<tar>]] [--as <db>] [--neutralize]
   [--no-checkpoint] [--dry-run] [--force] [--json]`.
   - `<dump>`: one local path to a pg_dump custom-format file (first 5 bytes `PGDMP`, else exit 2
     `not a pg_dump custom-format file`). Missing file → exit 2.
   - `--to <target>`: a registered connect target (exact name; unknown → exit 1 `unknown connect target`).
     `--remote`: this directory's `[connect]` binding (none → exit 2). Exactly one of the two is required; no
     implicit binding and no picker (a destructive command never guesses its target). Both → exit 2.
   - `--filestore` bare: the sibling `<dump without .dump>.filestore.tar` (missing → exit 2 naming the path).
     `--filestore=<tar>` or `--filestore <tar>` (next arg not starting with `-` and ending in `.tar`): that file.
     Without `--filestore` the target's filestore is left as is.
   - `--as <db>`: target DB name; default the server profile's `db_name`. Validated with `sanitizeDBName`
     (changed → exit 2).
   - Unknown flags, extra positionals → exit 2. The parser consumes every value it reads
     ([trap](../../knowledge/architecture-and-traps.md#cross-cutting-traps)).
2. **Refused targets.** A Reverb-managed target (the same test `sideloadTargetError` uses) → exit 1
   `db-push does not support Reverb environments`. Its lifecycle goes through the Reverb API, out of scope.
3. **Stage gate.** The stage comes from the server profile through `remoteConnectTarget`, so undeclared or
   unknown counts as `prod`. On `prod`, without `--force` → exit 2 `refusing to replace the database of a prod
   target; pass --force`. With `--force` on a TTY, it still asks once, a confirm whose default is No, naming the
   target and the DB. Without a TTY, `--force` is the confirmation. `dev`/`staging`: no prompt. db-push never
   prompts for anything else, so without a TTY it runs or exits 2. `--dry-run` skips the gate (it changes
   nothing) but prints the gate decision.
4. **Operation lock (new).** File `<remote_path>/.echo/op.lock`, JSON `{"command","pid","client_host","user",
   "started"}` (RFC 3339 UTC). It is taken atomically with `mkdir <remote_path>/.echo/op.lock.d` then writing
   the file inside, removed with `rm -rf` in a deferred step that runs on success, failure, Ctrl+C and SIGTERM
   (`signal.NotifyContext`). If the lock is held, exit 1:
   `target busy: <command> from <client_host> since <started>`. `--force` takes over a lock older than 6 hours
   (WARNING line naming the old holder); a younger lock is never taken over. In this unit only db-push takes and
   checks the lock; Unit 142 makes deploy, watch, sideload and checkpoint respect it. The lock lives under
   `.echo/`, which Echo already keeps out of the server's git (`deploy_lock.go`).
5. **Flow**, in order. The first failure stops it; the cleanup in step 13 always runs.
   1. Resolve the target, read the server profile (`fetchRemoteProfile`), apply decisions 2 and 3.
   2. Preflight: SSH reachable; the DB service is running; `--as` DB exists (if not: no checkpoint, a note says
      so); free space on the project's filesystem (`checkpointFreeBytes`) ≥ dump size + tar size +
      `checkpointNeed(current DB size, method)` (when a checkpoint will be taken) + tar size again (the
      extracted filestore). If it is short, exit 1 with the numbers and no `--force` override.
   3. Take the operation lock (decision 4).
   4. Upload to `<remote_path>/backups/db-push-<ts>/` (`ts` = `YYYYMMDD-HHMMSS`, local time) by streaming each
      file over `ssh … 'mkdir -p <dir> && cat > <dir>/<name>.part && mv <name>.part <name>'`. Progress goes
      through `progressReporter` (`restore_progress.go`): every 10 % and a 30 s heartbeat, as `db-restore` does.
      The size is checked after the upload with `stat -c %s` and must match, else exit 1.
   5. Stop Odoo: `remoteStopApp` (the Odoo service only; the DB service stays up).
   6. Checkpoint the DB being replaced. Method by `resolveCheckpointMode` (the target's `[checkpoint]` config,
      else `db`), recorded with `config.AddCheckpoint` and pruned like deploy's. Skipped with `--no-checkpoint`
      or when the DB does not exist. A checkpoint failure aborts ([decision 2026-07-13](../../decisions.md), "failure to create one aborts")
      and Odoo is started again. `createCheckpoint` gains a `db` parameter instead of always using
      `rsc.prof.DBName`, so `--as` is covered; deploy passes `rsc.prof.DBName` (no behaviour change).
   7. Restore: `remoteTerminateConns`, `remoteDropDB`, `remoteCreateDB`, `remoteRestoreDump` (pg_restore
      `--no-owner --role=<pg user>`) on the uploaded file. From here on, a failure leaves Odoo stopped and prints
      the recovery line `restore the previous DB: echo_cli deploy --rollback --from <target> --force` (when a
      checkpoint exists).
   8. Filestore (only with `--filestore`): run inside a one-off Odoo container, `compose run --rm --no-deps
      --user 0 --entrypoint sh <odoo service> -c '…'` (the service's volumes, so it works with Odoo stopped).
      The script: if `<fs>/<db>` exists, `mv` it to `<fs>/<db>.pre-push-<ts>`; `mkdir <fs>/<db>`; extract the
      tar read from stdin (`cat <uploaded tar> |` on the host) with `--strip-components=1` (the tar's top folder
      is the source DB's name); `chown -R` to `stat -c %u:%g <fs>`. `<fs>` is the server profile's
      `filestore_path`, default `/var/lib/odoo/filestore` (decision 9).
   9. Neutralize (only with `--neutralize`): `odoo.Neutralize(conn with DB=<db>)` in the same one-off container
      form (`compose run --rm --no-deps <odoo service> …`), before Odoo serves anything, so crons and mail
      never run on un-neutralized data.
   10. Registry check: `odoo -d <db> --stop-after-init --no-http` plus the conn flags `odoo.Neutralize` uses, in
       the one-off container. Its output goes through `runFailureScanner` / `deployFailureRe` (deploy.go); a
       match or a non-zero exit fails the push (Odoo stays stopped, recovery line as in 7).
   11. Start Odoo: `compose up -d <odoo service>`.
   12. Report: `remoteModuleStates` on `<db>`. Text: the count of installed modules plus `base` and up to five
       non-core installed modules with their `latest_version`. JSON: all of them.
   13. Cleanup (always, also on failure and on Ctrl+C after the upload started): `rm -rf` the upload dir, then
       release the lock. A failure here is a WARNING, not a failure of the push.
6. **Dry run.** Steps 1-2 run read-only (SSH, profile, sizes, free space), then the plan prints: target,
   host, stage and gate decision, DB (existing or new), dump and tar sizes, upload dir, checkpoint method and the
   name it would get, lock state (free / held by …), and the numbered steps 3-13. Nothing is written on the
   server: no lock, no directory, no upload. Exit 0 when the real run would pass the preflight, else 1.
7. **Output.** Odoo-style log lines with logger `echo.db_push` and one `step N/M` line per step. `--json`:
   stdout carries one object, `{"target","host","stage","db","dump","dump_bytes","filestore","filestore_bytes",
   "checkpoint","previous_filestore","neutralized","dry_run","steps":[{"name","status","seconds"}],
   "modules":[{"name","state","version"}],"ok"}`; logs go to stderr. A failure still prints the object with
   `ok: false` and the steps reached, so an agent learns where it stopped. Exit codes per
   [code-standards](../../knowledge/code-standards.md): 0, 1 failure, 2 usage or gate, 3 cancelled confirm.
8. **Command log.** The record carries the target fields `noteRemoteResolved` already fills, with `RemoteDB`
   set to the DB actually replaced (the `--as` value), plus two new `recordExtras` / `CmdLogRecord` fields:
   `source_dump` (base name) and `checkpoint` (its name, empty if none). `logview --json` shows them.
9. **Remote filestore path.** `RemoteProfile` gains `FilestorePath` (from the server profile's
   `filestore_path`, default `/var/lib/odoo/filestore`). `db-pull`'s `remoteFilestoreTarCmd` switches from the
   local `Cfg.FilestorePath` to it. That closes the small fix the plan lists, because both commands must agree
   on where the filestore is.
10. **Odoo is stopped by this DB command.** This is an exception to decision 2026-05-13 "destructive DB commands
    abort rather than auto-stopping Odoo": db-push is a remote replace-everything flow like deploy, and it is
    never run on the active local stack. Recorded in `decisions.md`.

## Implementation

### `internal/cmd/db_push.go` (new)

- `RunDBPush(ctx, DBPushOpts) (DBPushResult, error)`; opts: `Cfg`, `Root`, `Args`, `Log`, `Stream`, `Confirm`
  seam. `parseDBPushArgs` (decision 1). Seams for tests: the package's existing `runSSH` / stream seams, plus
  `dbPushNow` for the timestamp.
- Upload helper `uploadFile(ctx, sshHost, local, remoteDir string, report *progressReporter) error` built on a
  new `runSSHStreamReader` (`runSSHStream` with an `io.Reader` stdin instead of `[]byte`).
- Lock helpers in `internal/cmd/op_lock.go` (new): `acquireOpLock`, `releaseOpLock`, `readOpLock`, the 6-hour
  takeover rule. Exported shape so Unit 142 reuses it.
- `oneOffOdooCmd(rsc, args...)` building `compose run --rm --no-deps …` (decision 5.8-5.10).

### `internal/cmd/checkpoint_remote.go`, `deploy.go`, `db_pull.go`, `internal/config`

- `createCheckpoint` takes the DB name; deploy's call passes `rsc.prof.DBName`.
- `RemoteProfile.FilestorePath` + parsing; `remoteFilestoreTarCmd` uses it (decision 9).

### `internal/repl` and wiring ([new command wiring](../../knowledge/code-standards.md#new-command-wiring))

- `runDBPush` handler (not `runDB`, which drops `ErrUsage`): JSON finish like `finishCheckpointJSON`; exit
  mapping for `ErrUsage`, `ErrNonInteractive`, `ErrCancelled`, `huh.ErrUserAborted`.
- `Registry` and `dispatchNames` after `db-pull`; `commandFlags["db-push"]` = `--to`, `--remote`, `--filestore`,
  `--as`, `--neutralize`, `--no-checkpoint`, `--dry-run`, `--force`, `--json`; a `commandDoc` with notes and
  examples (required: it has `--remote` and `--json`).
- `main.go` `projectlessOneShot`: `db-push` always (it never needs the local stack).
- Not a sequence command (destructive, needs its positional dump).
- `recordExtras` / `CmdLogRecord` fields (decision 8).

### Store and docs

- [database](../../knowledge/database.md): a `db-push` section (flow, lock, filestore rename, recovery).
- [deploy-safety](../../knowledge/deploy-safety.md): the operation lock and its takeover rule; `createCheckpoint`
  takes a DB.
- `decisions.md`: decision 10 and the operation lock (only db-push in this unit; Unit 142 extends it).
- `plan.md`: this unit and Unit 142 `op-lock-respect` (deploy, watch, sideload and checkpoint check and take
  the lock); drop the `remoteFilestoreTarCmd` small fix.
- README Database section; `CHANGELOG.md` `[Unreleased]` → `Added` (db-push) and `Fixed` (db-pull reads the
  server's `filestore_path`).

## Dependencies

- none (SSH, compose and the checkpoint primitives already exist).

## Verify when done

- [ ] Parser table: `--to` and `--remote` both or neither → exit 2; bare `--filestore` resolves the sibling tar
      and fails naming it when missing; a non-`PGDMP` file → exit 2; `--as` with invalid characters → exit 2.
- [ ] With a scripted SSH seam: the order of remote commands is preflight → lock → upload → stop → checkpoint →
      terminate/drop/create/restore → filestore → neutralize → registry check → up → module states → cleanup;
      a failure at each step runs the cleanup and releases the lock, and after step 7 Odoo is not started.
- [ ] Dry run issues only read commands (no `mkdir`, `cat >`, `mv`, `rm`, `compose`).
- [ ] Gate: stage `prod` and undeclared stage without `--force` → exit 2 and no remote write; with `--force`
      and no TTY it runs.
- [ ] Lock: a held lock → exit 1 naming the holder; one older than 6 h is taken over only with `--force`.
- [ ] Live on `habitta_dev` from a `habitta_staging` pull: `db-push backups/<staging>.dump --to habitta_dev
      --filestore --neutralize` ends ok; `compare --targets habitta_staging,habitta_dev` shows no version
      differences other than the lock source; an attachment opens in the web client; outgoing mail servers and
      crons are off; `--dry-run` leaves the server unchanged (same `ls` of `backups/` and `.echo/`);
      `deploy --rollback --from habitta_dev --force` brings the previous DB back. Recorded in the
      [ledger](../../knowledge/operations.md#live-verification-ledger).
- [ ] `echo_cli db-push --help` prints the page (Unit 140) and does nothing else.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` green; changed files `gofmt`-clean; Registry/help/flag
      cross-checks pass; `CHANGELOG.md` updated.
