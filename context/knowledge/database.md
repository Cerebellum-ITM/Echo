# Database

> Owns the `db-*` commands: backup and restore formats, filestore handling, guards, neutralize, `db-pull`, `db-admin` (password hashing and 1Password), and the transport used to run commands inside a remote Postgres. Last verified: 2026-09-30.

Code: `internal/cmd/db.go`, `db_pull.go`, `db_remote.go`, `onepassword.go`, `internal/docker/postgres.go`, `pgdump.go`, `internal/odoo/passlib.go`. Remote resolution (`resolveRemoteShell`, `--from`/`--remote`/`-E`) is owned by [remote-targets](remote-targets.md); checkpoints (which also copy databases) by [deploy-safety](deploy-safety.md).

## Where each command runs

| Command | Needs | Remote form |
|---|---|---|
| `db-list`, `db-use`, `db-backup`, `db-restore`, `db-drop`, `db-neutralize` | local compose stack with a configured `db_container` (`requireDBContainer`, error `ErrNoDBContainer`) | none. A remote backup is `db-pull` or a checkpoint |
| `db-pull` | nothing locally unless `--restore` | pulls FROM a remote target |
| `db-admin` | local stack, or a remote target with `--from`/`--remote`/`-E` | yes |

- `db-pull` is always projectless; `db-admin` is projectless only with a remote flag (`projectlessOneShot`, `main.go:236`). Reason: a directory linked to a remote has no compose file, and `FindRoot` only recognizes `docker-compose.yml`/`.yaml` (`internal/project/`). A command that demanded the compose file blocked `db-admin --remote` in a linked directory (Unit 116).
- Progress narration: commands receive `DBOpts.Log` and emit `echo.<cmd>.<step>` Odoo-style lines through it. It is nil-safe, so a caller without it stays silent (same shape as the connect logger). Source: Unit 67, `DBOpts.log`.
- **Trap: progress lines are not captured.** Lines emitted through `emitOdooLog` (this callback, orchestrator lines) go to the screen and the `--log` tee but not into `lastOutput`, so `report`, `copy-last` and command-log records do not contain them; only `sess.print`/`printStyled` lines are captured (`internal/repl/repl.go` `print`, `printStyled`; `logemit.go` `emitOdooLogTo`). Observed by reading, not run.
- **Trap: `ErrUsage` from a db command exits 1, not 2.** `runDB` (`internal/repl/repl.go`, the `db-*` switch) special-cases only cancel and `ErrNonInteractive`; everything else goes through `commandFailureLog` -> `scriptExitCode`, which has no `ErrUsage` branch. `deploy`, `checkpoint`, `actions` and `link` handle `ErrUsage` themselves. So `db-admin --password x --insecure` and `ErrDBExists` exit 1. Observed by reading, not run. See [exit codes](architecture-and-traps.md).

## Guards and `--force`

- `--force` on `db-drop` and `db-restore` means: skip the confirmation AND terminate every other backend on the target (`docker.TerminateConnections`, `pg_terminate_backend ... pid <> pg_backend_pid()` from the `postgres` maintenance DB), then drop. Without `--force`, `db-drop` runs `assertNoActiveConns` (blocks with `ErrActiveConns`, which names `--force` and `down odoo`) and then a red confirm. Echo never stops Odoo on its own. Why: an orphan DB left by a failed restore could not be dropped without manually stopping Odoo, while keeping the guard under `--force` would not have helped; without `--force` the guard still protects a live DB from a one-keystroke drop. Source: Unit 23, Unit 09.
- `assertNoActiveConns` swallows a failed connection-count query (treated as zero) so the real operation surfaces the real error. It is used only by `db-drop`; `db-restore` has no connection guard of its own (an existing target without `--force` fails with `ErrDBExists` first).
- `db-drop` confirms in every stage; there is no stage logic. `db-admin` and `db-neutralize` have their own risk rules below.
- `db-backup` has no connection guard: `pg_dump` reads an MVCC snapshot and is safe against a live DB (comment in `RunDBBackup`).
- `db-restore` takes no file argument. It always opens the backup picker, so it cannot run headless or as a recipe step (non-TTY -> `ErrNonInteractive`, exit 2). The headless restore path is `db-pull --restore`.

## Backup (`db-backup`)

- Output: `./backups/<db>_<YYYYMMDD-HHMMSS>.dump` (`pg_dump -Fc`), or `.zip` with `--with-filestore` containing `dump.backup` plus `filestore/<db>/...`. No filestore found in the container -> warning and a dump-only zip.
- Echo appends `backups/` to an EXISTING `.gitignore` and never creates one (`maybeAppendGitignore`). Why: creating files in the user's repo is the user's call.
- `db-list` creation date comes from `pg_stat_file(base/<oid>/PG_VERSION)`; where the role cannot call it (hosted Postgres) the query is retried without it and the column shows `—` (`ListDatabasesDetailed`).
- Stale code comment: the `RunDBBackup` doc comment still says the filestore is read from `~/.local/share/Odoo/filestore/<db>`. The code reads the container (below).

## Restore (`db-restore`, shared `restoreBackupFile`)

- Flow: derive target name -> if it exists: `ErrDBExists` without `--force`, else terminate + drop -> `CREATE DATABASE ... OWNER <user>` -> restore -> optional neutralize. `restoreBackupFile` is shared with `db-pull --restore`.
- Target name: `--as` wins; otherwise a pre-filled `huh.Input` ("Restore as") the user can shorten; no TTY falls back to the derived name. Why: odoo.sh dump names are long (`mycompany-prod-12345678`) and bloat every log line. `validateDBName` rejects empty and whitespace; Esc is `huh.ErrUserAborted`, a clean cancel with no DB created. Source: Unit 68.
- Derived name (`dbNameFromBackup`) strips two timestamp forms: Echo `_YYYYMMDD-HHMMSS` and Odoo `_YYYY-MM-DD_HH-MM-SS`. Not stripping the Odoo form once produced a wrong target name and a spurious "database already exists" (Unit 22).
- Picker lists top-level `*.dump` and `*.zip` of `./backups/`, newest mtime first.
- **Two archive flavors, detected from contents (no flag)** (`restoreFromZip`):
  - Echo zip: `dump.backup` (custom format -> `pg_restore`) + `filestore/<db>/<XX>/...`.
  - Odoo-native zip (DB manager, odoo.sh): `dump.sql` (plain SQL -> `psql -q` via stdin) + `filestore/<XX>/...` + `manifest.json` (ignored). Neither dump present -> error.
  - Filestore layout (`findFilestoreInDir`): if every directory under `filestore/` is a 2-char hex prefix it is the Odoo layout, otherwise the single db-named subdirectory is the Echo layout.
- `RestoreSQL` runs `psql -q` WITHOUT `ON_ERROR_STOP`, on purpose: it matches a manual `psql < dump.sql` into a fresh empty DB; non-zero exit only for fatal/connection errors (`docker/pgdump.go`). Hence a plain-SQL restore can "succeed" with errors in the stream.
- `pg_restore` runs with `--no-owner --role=<user>`. It is silent without `--verbose`, so Echo adds `--verbose` only when a progress callback exists, forwards every stderr line as DEBUG `echo.db-restore.restore`, and keeps only lines containing `error`/`fatal` for the failure message so the cause is not buried (`streamStderr`, 1 MB scanner buffer). Two layers by design: INFO milestones mark phases, the DEBUG stream shows activity. `db-backup` has no progress.
- Zip extraction rejects paths escaping the target dir (`unzip`, zip-slip check).

## Filestore lives in the Odoo container

- Echo targets dockerized Odoo: the filestore is at `<filestore_path>/<db>` INSIDE the Odoo container (`filestore_path`, project config, default `/var/lib/odoo/filestore`). Handling it on the host (`~/.local/share/Odoo/filestore`) restored the SQL fine but raised `FileNotFoundError` on every attachment (seen on `habitta_prod`). Source: Unit 25, `internal/config/defaults.go`.
- Restore: `mkdir -p` + `docker cp <src>/. <id>:<dst>` + best-effort chown. Backup: `test -d` + `docker cp` out to a temp dir, zipped (`copyFilestoreToContainer`, `pullContainerFilestore`).
- `docker cp` leaves files root-owned. The fix is `docker exec -u 0 ... chown -R "$(stat -c '%u:%g' <filestore_path>)" <dst>` (owner copied from the base dir) through raw docker, not compose (`docker.ExecAsRoot`). A chown failure is a warning: Odoo can read but may not write new attachments.
- **Trap: remote filestore path.** `db-pull` builds the remote `tar` against the LOCAL project's `filestore_path` (`remoteFilestoreTarCmd`, comment "same in-container path convention both sides"). A remote with a non-default path needs the same setting locally.

## Neutralize (`db-neutralize`, `db-restore --neutralize`)

- Runs `odoo neutralize -d <db>` inside the Odoo container (`odoo.Neutralize`) with explicit `--db_*` flags, because `compose exec` skips the image entrypoint (see [odoo shell connection](scripting.md#odoo-shell-connection)). Needs `odoo_container` AND an active `db_name` (`requireOdooConfig`) even when the target is passed as an argument.
- Neutralize is the inverse risk of drop: harmless on a throwaway copy, destructive on production (it wipes real mail and payment config). So there is NO active-connection guard (it runs with Odoo up), and a red confirm appears only when the target is the active DB or `cfg.Stage == prod`; `--force` skips. Rejected: copying drop's connection guard. Source: Unit 30, `RunDBNeutralize`.
- `db-restore --neutralize` runs it LAST so a failure surfaces with the data already in place, with no extra confirm (the target is a fresh copy; explicit opt-in).
- `db-pull` neutralizes automatically only when the source stage is prod (see below).

## `db-pull`

- Remote side is read-only: one `pg_dump -Fc --no-owner [-U user] <db>` run in the remote Postgres container, binary stdout streamed over SSH straight into `./backups/<db>_<target>_<yyyymmdd-hhmmss>.dump` (`runSSHToFile`: no remote temp file, no in-memory buffering, progress every 1 MiB, a failed or interrupted stream deletes the partial file). Reading prod is normal use, so there is no remote prod gate (same as `i18n-pull`). Source: Unit 85, `internal/cmd/remote.go`.
- **Download-only by default** (Unit 98). Why: link-only projects have no local stack; the project directory is just module source linked to a remote that holds compose, Postgres and Odoo. `--filestore` downloads the raw tar to `<db>_<target>_<ts>.filestore.tar`.
- `--restore` opts into load + optional filestore copy, guarded by `requireDBContainer`. `--as`, `--neutralize`, `--no-neutralize` and `--force` act only under `--restore` and are silently ignored otherwise.
  - **Trap:** the dump is fully downloaded BEFORE `requireDBContainer` runs, so `--restore` without a local stack fails after the download.
  - Default local name: `sanitizeDBName(<remoteDB>_<targetLabel>)` (`[a-z0-9_]`). The active DB is NOT switched; the closing line hints `db-use <name>`. Why not switch: it would surprise more than help.
  - Neutralize is tri-state: auto = only when the target stage is prod (a pulled prod DB carries live crons, mail servers, payment providers; staging is usually already neutered); `--neutralize` forces; `--no-neutralize` suppresses. Rejected: always neutralize. The stage read is the target's, `rsc.target.stage`.
  - Existing local target without `--force` -> `ErrDBExists`.
- Filestore under `--restore`: remote `tar -cf - -C <filestore_path> <db>` through the remote Odoo container, extracted locally (`extractTarReader`, streaming) and copied into the local container. A missing remote filestore warns and continues.
- A pulled dump is a normal backup (same dir and format); a later `db-restore` can pick it.

## `db-admin`

Resets `res_users` id 2 (Odoo's admin; id 1 is the system user) to login `admin` and a password. A missing row fails loudly (`no user with id 2`, via `UPDATE ... RETURNING id`) instead of doing nothing.

- **Default is a generated password, stored hashed.** 20 chars from `crypto/rand` with rejection sampling; the alphabet omits `il1O0` (copied by hand) and has no symbols. Stored as passlib `$pbkdf2-sha512$25000$<salt>$<checksum>`, 16-byte salt, "adapted base64" (unpadded, `+` -> `.`), using stdlib `crypto/pbkdf2` (Go 1.24+, no new dependency). The password is printed once; Echo stores only the hash. Validated against a Python `hashlib.pbkdf2_hmac` vector, and Odoo accepted and logged in with it (Unit 116). Source: `internal/odoo/passlib.go`, `generateAdminPassword`.
- Why not plaintext: the old behaviour (`admin`/`admin`, relying on Odoo's deprecated plaintext crypt scheme) was readable with `psql` and traveled in every `db-backup`/`db-pull` until the next login. Rejected: keeping plaintext even for strong passwords.
- Flags: `--password <p>` sets an explicit one; `--insecure` restores `admin`/`admin` for throwaway DBs (the default changed, the behaviour was kept); both together are `ErrUsage`. The login is fixed to `admin`: it is not the secret, and generating it adds a second thing to copy and breaks notes and demos that mention `admin`.
- **The confirm guard measures risk, not stage**: red confirm when the target stage is `prod` (the reset locks out whoever knows the real password) or when the resulting credential is publicly known (`--insecure`) in any stage; `--force` skips. `resolveAdminCredential`.
- **Remote** (`--from`/`--remote`/`-E`, `runDBAdminRemote`): the hash is computed locally, so only the hash crosses SSH. The guard must read `rsc.target.stage`, NOT `opts.Cfg.Stage`; otherwise `db-admin --from prod` run from a dev checkout would not ask (same convention as `deploy`, `watch`, `shell`, `db-pull`). `parseDBArgs` must consume the value of `--from`/`-E`/`--env`, or the target name is read as the DB name. Remote live-verified on `morwi/fuentebuena` (Unit 116).

### 1Password (`--save`, `--vault`)

- `--save` is opt-in: a vault write is a persistent out-of-process effect and must not fire just because `op` exists. `--vault` without `--save` is `ErrUsage`.
- Item title `<project> (<db>)`; tags `echo`, `odoo` and, for remote, the SSH host. Remote project = `fromName`, falling back to the basename of `remote_path` (`remoteProjectName`); `targetLabel` is wrong here because its fallback is `ssh_host`, which names the server (all of one user's targets hang off a single host). Rejected: title `Odoo <target> (<db>)`. Source: Unit 117, commit 89f4ade.
- Order matters: `opAvailable` runs BEFORE the `UPDATE`. A locked vault found afterwards would leave the DB reset and the credential stored nowhere. After the reset, a save failure is only a WARNING and the password is still printed.
- Probe with `op vault list`, not `op whoami`: under the desktop-app integration there is no classic session token and `whoami` reports "not signed in" while every real command works (commit 00da980).
- Update, do not duplicate (1Password keeps history): `op item get <title>`; the text `isn't an item` means missing (the CLI has no exit code for it, checked against op 2.34), any other error propagates so a network blip cannot create a duplicate. Edit patches the FULL item JSON so manual sections, notes and custom fields survive; `op item edit --tags` replaces the list, so `mergeTags` re-declares existing plus Echo tags.
- The password goes through stdin, never argv (`op` warns argv is visible to other processes); the URL goes through `--url` in both create and edit. `op` recognizes template `-` only when stdin is a pipe, which `os/exec` always provides.
- URL comes from `ir_config_parameter.web.base.url`. Odoo rewrites it from the first request's host unless `web.base.url.freeze` is set, so a local-looking value (`isLocalBaseURL`) or an empty one is dropped with a notice and the item is saved without URL; a wrong URL would autofill on the wrong site.
- Pending live checks (update path, no-URL item): [ledger](operations.md#live-verification-ledger).

## `db-use`

Switches `cfg.DBName` and persists it with `config.SaveProject`. The session and `DBOpts.Cfg` share one `*config.Config`, so the prompt shows the new DB on the next render without a restart. It verifies the DB exists, reports a no-op when already active, and has no prod guard (switching is harmless; dangerous commands keep their own guards). Source: Unit 66 (db-use half).

## Postgres exec transport for remote targets

- Everything Echo runs INSIDE a remote Postgres container assumed `db_container` is a compose SERVICE (`cd <path> && <compose> exec -T <db> ...`). For Reverb the DB belongs to the project's compose, not the environment's, so that failed with `no such service` although the container was up. Source: Unit 119, commit 2a17a5b.
- `connectTarget.dbExec` is `compose` (default) or `docker` (`docker exec -i <container>`, resolved by name, no `cd`). Docker form uses `-i` not `-it`: stdin matters (`pg_restore < file`) and SSH has no TTY. `remoteConnectTarget` sets `docker` when the server profile carries the `[reverb]` marker table; the `-E` path sets it in `internal/cmd/reverb.go`. The marker is read only from the project profile (a property of the environment, not the host).
- `withDBExecFallback` retries ONCE with `docker exec -i` when a compose exec error contains `no such service` (covers hand-built targets whose DB left the compose). Not memoized; no INFO line on fallback (`internal/cmd` has no ambient logger and the wiring cost more than the clarity). A deliberate deviation from the Reverb plan.
- Scope: `remotePsqlScalar`/`remotePsqlExec`, checkpoint dump/restore and `df` pre-flight, `db-pull`'s `pg_dump`, `db-admin`, deploy and `i18n-pull` module queries. `db-list` is local-only and untouched. It also fixed the DB half of `-E`, which had mapped `containers.db` (a name) onto a field consumed as a service.
- **Trap:** build remote targets through `remoteConnectTarget`, the single place the transport is derived; a hand-built `connectTarget` is born with the compose default. The local target in `resolveConnectTarget` is intentionally not remote.
