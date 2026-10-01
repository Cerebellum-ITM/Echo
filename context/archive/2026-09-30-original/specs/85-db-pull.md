# Unit 85: `db-pull` — clone a remote database into the local stack

## Goal

New `db-pull [--from <t>|--remote] [--as <name>] [--no-neutralize]
[--filestore] [--force]` command: dump the remote target's database
over SSH, save it into the project's `./backups/`, restore it into the
local Postgres under a distinct name, and (by default, when the source
is prod) neutralize it — the classic "reproduce the prod bug locally"
workflow in one command, built entirely from existing pieces
(remote transport, `db-restore`, `db-neutralize`, Unit 25's
container-filestore handling).

## Design

**Read-only on the remote, mutations only local.** The remote side runs
exactly one `pg_dump` (plus an optional filestore tar) — no locks
beyond what pg_dump takes, no writes, hence **no remote prod gate**
(the i18n-pull precedent: reading from prod is normal use). All the
gated operations (create/replace DB, neutralize) happen locally through
the existing db commands' own guards.

**Dump: stream straight to a local backup file.** Target resolution via
`resolveRemoteShell` (profile gives DB container, DB name, stage,
`POSTGRES_*` from the remote env). The dump command runs in the remote
DB container and its **binary stdout streams over SSH into the local
file** — no remote temp file, no full-dump buffering in memory:

```
ssh <host> 'cd <path> && <compose> exec -T <db> \
  pg_dump -U <user> -Fc --no-owner <dbname>' > ./backups/<file>.dump
```

This needs a new `runSSHToFile(ctx, host, remoteCmd, destPath,
onProgress)` transport primitive: like `runSSH` but io.Copy into the
file, reporting bytes so the REPL can print a live
`pulled 84 MB…` progress line (the Unit 67 restore-progress pattern).
A non-zero SSH exit removes the partial file.

The backup lands as `./backups/<db>_<target>_<yyyymmdd-hhmmss>.dump` —
the same directory and `-Fc` format `db-backup`/`db-restore` already
speak, so the pulled dump is *also* a normal backup the picker can
restore again later (and `maybeAppendGitignore` already keeps
`backups/` out of git).

**Restore: reuse `db-restore`'s machinery, not a reimplementation.**
The local restore runs the existing restore path (pg_restore of a
`-Fc` dump, `--force` semantics for an existing target DB — terminate
connections + replace, prompted without `--force`). Target name:
`--as <name>` wins; default is `<remoteDB>_<targetName>` sanitized to
Postgres identifier rules (e.g. `muutrade_prod`). The active DB is
**not** switched — the close frame prints the
`db-use <name>` hint instead (switching implicitly would surprise more
than it helps).

**Neutralize by default when the source is prod.** A pulled prod DB
carries live cron jobs, outgoing mail servers and payment providers —
running it un-neutralized locally is the classic footgun. So:
source-profile `stage == "prod"` → the local `db-neutralize` runs
automatically after the restore (its own machinery, Unit 30);
`--no-neutralize` opts out. Non-prod sources default to *not*
neutralizing (a staging clone is usually already neutered) — the flag
is symmetric anyway (`--neutralize` forces it on for any source).

**`--filestore`.** Attachments often matter for reproducing (reports,
images). With the flag, after the dump: tar the remote filestore dir
for that DB inside the remote **Odoo** container
(`tar -cf - -C <filestore-parent> <db>` streamed via `runSSHToFile`),
then extract it into the local Odoo container's filestore under the
new DB name (`docker.CopyToContainer` + in-container `tar -xf`,
the Unit 25/53 write-into-container patterns). Filestore-less DBs (or
a missing dir) log a WARNING and continue — the DB pull already
succeeded.

**Frames.** One start line naming source (`target`, `db`, `stage`) and
destination name; step lines for dump (with size), restore, neutralize,
filestore; a close frame
`echo.db-pull: pull complete db=<name> size=<…> neutralized=<bool>`.
Failures name the step (the deploy step-runner pattern).

## Implementation

### `internal/cmd/db_pull.go` — new file

- `parseDBPullArgs(args)` → `(as string, neutralize *bool /*nil=auto*/,
  filestore, force bool, from string, remote bool, err error)`.
- `RunDBPull(ctx, opts DBOpts) error`:
  1. `resolveRemoteShell`; derive default `--as`
     (`sanitizeDBName(remoteDB + "_" + targetName)`).
  2. dump via `remoteContainerCmd`-style argv against the **DB**
     container (`remoteDBCmd`) piped through the new `runSSHToFile`;
     progress to `opts.StreamOut`.
  3. restore by delegating to the existing restore core with an
     explicit backup path + target name (extract the file-path entry
     point from `RunDBRestore` if the picker is currently welded in —
     `restoreBackupFile(ctx, opts, path, asName, force)`).
  4. auto/flag neutralize → existing neutralize core against the new
     DB.
  5. `--filestore` step as designed above.
- `sanitizeDBName`: lowercase, `[a-z0-9_]`, collapse the rest to `_`.

### `internal/cmd/remote.go` — `runSSHToFile`

Binary-safe streaming variant of `runSSH`: `exec.CommandContext(ssh…)`,
stdout → `os.Create(dest)` through an `io.Copy` wrapper that calls
`onProgress(bytesSoFar)` (throttled ~500ms); stderr folded into the
error; partial file removed on failure.

### `internal/repl/db.go` — dispatch

`db-pull` joins the `runDB` family (start/finalize frame named
`db-pull`). Help, Database section:
`{"db-pull", "Pull a remote DB into the local stack (dump → restore)"}` +
rows `--from <t>`/`--remote`, `--as <name>`,
`--neutralize` / `--no-neutralize`, `--filestore`, `--force`.

### Registration

`Registry` / `dispatchNames` / dispatch case / `commandFlags["db-pull"]`.
**Not** projectless: the restore needs the local docker stack, so a
compose project is required (unlike `push`/`watch`).

### Tests (`internal/cmd/db_pull_test.go`)

- `parseDBPullArgs` matrix (auto vs forced neutralize tristate; remote
  flags stripped).
- `sanitizeDBName` cases (`muutrade-PROD` → `muutrade_prod`).
- Default-name derivation from profile db + target name.
- `runSSHToFile`: happy path writes bytes + progress calls; failing
  command leaves no partial file (fake ssh via PATH shim or cmd seam).

## Dependencies

None new — `ssh`/`pg_dump`/`pg_restore`/`tar` all already assumed by
the existing db/remote commands. Requires Units 09 (db stack), 25
(container filestore), 30 (neutralize), 62/72 (remote profile
plumbing).

## Verify when done

- [ ] `db-pull --from prod` streams the dump into
      `./backups/<db>_prod_<ts>.dump` with live size progress, restores
      it locally as `<db>_prod`, and **auto-neutralizes** it; the close
      frame hints `db-use <db>_prod`.
- [ ] `db-pull --from staging` does not neutralize by default;
      `--neutralize` forces it, `--no-neutralize` suppresses it for a
      prod source.
- [ ] `--as clientx_debug` restores under that exact name; an existing
      local DB of that name prompts (or is replaced with `--force`,
      connections terminated).
- [ ] `--filestore` lands the attachments under the local container's
      filestore for the new DB name; a missing remote filestore warns
      and continues.
- [ ] The pulled `.dump` is visible to a later plain `db-restore`
      picker run.
- [ ] No remote prod confirm fires (read-only remote); all local
      guards (replace/neutralize) behave as their own commands do.
- [ ] A dump interrupted mid-stream leaves no partial file in
      `./backups/`.
- [ ] The active DB is unchanged after the pull.
- [ ] `help`/registry/dispatch consistency tests stay green.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/cmd/...
      ./internal/repl/...` pass.
