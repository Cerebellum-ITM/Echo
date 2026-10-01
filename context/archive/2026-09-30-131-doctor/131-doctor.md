# Unit 131 · doctor
> Spec for one unit of [the plan](../../work/build/plan.md). Implement exactly this: no more, no less. Last verified: 2026-09-30.

## Goal

`doctor --from <target>` answers "is this target ready for deploy, push and checkpoints" in one read-only
report: server profile and stage, SSH and rsync on both ends, git-deploy preflight, checkpoint disk, deploy lock,
push destination and whether another target shares it. Today each fact surfaces only when a deploy reaches the
step that needs it: `requireRsync` checks the local side only (`internal/cmd/push.go:574`), so a server without
rsync fails inside the push, after the code snapshot. Doctor runs every check, never stops at the first failure,
and changes nothing on either side.

## Design

### A. Command surface

- `doctor [--from <target> | --remote] [--json]`; the parser consumes the `--from` value
  ([trap](../../knowledge/architecture-and-traps.md#cross-cutting-traps)). Target from `resolveRemoteTarget`
  (`internal/cmd/deploy.go:2179`): `--from` › link binding › one global target, or the TTY-guarded picker
  ([decisions](#decisions)).
  `-E`/`--env` (Unit 122), positionals and unknown flags: `ErrUsage` before any SSH.
- Read-only: no prod gate, no confirm, no auto-copy. Always projectless (`projectlessOneShot`, `main.go:236`).
- Not `resolveRemoteShell` (`internal/cmd/shell_remote.go:67`): it returns at the first profile error, which
  doctor must survive; doctor reuses its pure parts (`ParseRemoteProfile`, `remoteConnectTarget`).

### B. Transport: three read-only batches

Each batch is one `runSSH` call through a new seam `doctorRunSSH`, 30 s deadline. The script prints
`@@echo-doctor <section> <rc>` after each section and always exits 0: a non-zero `ssh` exit means transport, and
a failing section never hides the next. No `mkdir`, `mv`, `rm`, redirect into a file, or compose verb but `exec`.

1. **Host**: the two server profile files `fetchRemoteProfile` reads (`internal/cmd/connect.go:330`) and the
   project profiles of the other connect targets with the same `ssh_host` (keys computed locally with
   `config.ProjectKey`); `command -v rsync`; `git --version`; on git targets, via `remoteGitCmd`,
   `rev-parse --is-inside-work-tree`, `cat-file -e <local root commit>`, `rev-parse --verify
   refs/heads/<git_branch>`, the `status --porcelain` count; the lock (D); `git ls-files -- .echo`; `.env`,
   parsed in memory like `remotePullEnv` (`internal/cmd/i18n_pull.go:450`) for `POSTGRES_USER` only, never
   logged; `df -Pk <remote_path>`.
2. **Destination** (needs the parsed profiles): `test -d` and `readlink -f` of the destination or auto-detect
   candidates of this target and of each same-host target.
3. **DB**: one exec in the DB container (`dbExecCmd` under `withDBExecFallback`, `internal/cmd/db_remote.go:73`)
   running `psql -At` for `pg_database_size(<db>)` and `data_directory`, then `df -Pk` on that directory.

Batches 2 and 3 are skipped when batch 1 fails or the profile is unusable.

### C. Checks

Each check ends `ok`, `warn`, `failed` or `skipped` (with `reason`), in this order:

| id | failed when | warn when |
|---|---|---|
| `ssh` | local `ssh` missing; host unreachable (last stderr line) | `ssh_host` contains `@`: a literal `user@ip` matches no `Host` block ([remote-targets](../../knowledge/remote-targets.md#connect-targets)) |
| `profile` | no project profile; parse error with file and line (Unit 127); empty `db_name`, `odoo_container` or `db_container` | `stageDeclared=false` (gated as prod, Unit 127); empty `odoo_version`; `config.ValidateDeployActions` error |
| `rsync` | missing locally (`lookPath`) or on the server while `resolveDeployPush` (`deploy.go:547`) is true | missing while push is off |
| `git` | the `gitPreflight` failures (`deploy_git.go:135`): no git on the server, not a work tree, local root commit absent | cwd is not a repository |
| `disk` | a deploy would checkpoint (`resolveCheckpointPolicy`, `resolveCheckpointMode(deployArgs{}, …)`, `deploy.go:742`, `:771`) and free < need | checkpoint off but free < need; size or free unmeasurable |
| `lock` | — | `unreadable`; `corrupt` (the next write replaces it); unverified entries; the repo tracks `.echo/` |
| `dest` | `resolvePushDest(pushArgs{}, prof, cfg)` (`push_dest.go:66`) names a missing directory without `mkdir`, or the compose root; auto-detect finds no existing candidate (`remoteAddonsCandidates`, `push.go:347`) | — |
| `dest.shared` | — | the sharing target declares another stage (same stage: `ok`, INFO) |

Skipped: `git` on rsync targets (`reason=git_deploy_off`, `resolveGitDeploy`); `disk` when the target has a
Reverb context (`reverbEnvFromProfile`, `internal/cmd/reverb.go:142`; checkpoints are server snapshots); all
checks after `ssh` when it failed (`reason=unreachable`); `disk` and `dest` when `profile` failed.

- **Disk need**: `db` needs 1.2× the DB size free on the data directory; `dump` 0.5× free on the host
  filesystem of `<remote_path>`, where `remoteDumpToFile` writes it (`internal/cmd/checkpoint_remote.go:206`).
  The factors move from `checkpointPreflight` (`checkpoint_remote.go:427`) into `checkpointNeed(size, method)`.
- **Auto-detect**: deploy first looks where each module already lives (`pushDest`, `push.go:318`); doctor has no
  module, so it reports the base a new module would land in (`detect=base`).
- **Shared destination**: another target with the same `ssh_host` whose destination (its server `[push] path`,
  else the local one, else its first existing candidate) has the same `readlink -f`. Sharing is legal
  ([decisions](../../decisions.md#deploy), "Declined: an exclusive push destination per target"). Two `Host`
  aliases of one machine are not detected.
- **Lock** absent is `ok` (`no deploy lock`); found is `ok` with the fields of `reportDeployLock`
  (`internal/cmd/deploy_lock.go:398`).

### D. The lock reader split (shared with 129 and 130)

Specs 129 and 130 need a lock read that tells its states apart, added by whichever unit lands first. Doctor does,
so this unit adds to `deploy_lock.go`:

- `lockState`: `lockFound`, `lockAbsent`, `lockUnreadable`, `lockCorrupt`.
- `fetchDeployLock(ctx, rsc) (raw []byte, state lockState, err error)` over `lockRunSSH`: the script prints
  `@@absent` when there is no file, so absent, permission denied and transport failure differ; today
  `cat … 2>/dev/null || true` folds all three into "no lock" (`deploy_lock.go:138`).
- `parseDeployLock(raw) (DeployLock, lockState, error)`, pure.
- `readDeployLock` keeps its signature and warn-and-continue behaviour on top of both; deploy does not change.
  129 hashes `raw`, 130 fetches per side, doctor parses the lock section of batch 1.

### E. Output and exit codes

One line per check, logger `echo.doctor.<id>`, level by status (`ok`/`skipped` INFO, `warn` WARNING, `failed`
ERROR), `status=` plus the check's fields; the `profile` line carries the `statusFields` of the system line.

```
INFO    echo.doctor: target target=habitta_prod host=habitta path=/srv/habitta
ERROR   echo.doctor.rsync: rsync not found on the server status=failed side=remote
WARNING echo.doctor.dest.shared: push destination shared status=warn dest=/srv/.cache/all_odoo with=habitta_dev stage=dev
INFO    echo.doctor: doctor summary target=habitta_prod ok=5 warn=1 failed=1 skipped=1
```

- Exit 0 when nothing failed (warnings included), 1 when any check failed, 2 for usage or a picker without a
  TTY, 3 for a cancelled picker. The handler passes the failed count as `errorCount` to `sess.finalize`
  (`internal/repl/repl.go:899`), which already maps it to exit 1 and `doctor finished with errors`.
- `--json`: one object on stdout, lines on stderr ([ui](../../knowledge/ui.md#stdout-contract-and---json)):
  `{"target": {name, host, path, stage, stage_declared, db}, "checks": [{id, status, reason, message, fields}],
  "counts": {ok, warn, failed, skipped}}`. `fields` are strings; never `.env` values or the Reverb token.

## Decisions

The three open points were settled by the user on 2026-09-30 with the recommended options.

- **`doctor` without `--from`** uses the usual resolution chain of every remote verb: the link binding, else the
  one registered target, else the TTY-guarded picker (no TTY: `ErrNonInteractive`, exit 2). Checking every
  registered target at once can come later as `--all`. Rejected: requiring `--from`/`--remote` (bare `doctor` as
  `ErrUsage`); checking all targets by default.
- **A shared push destination** is `warn` (WARNING) when the sharing target declares another normalized stage
  (dev sharing with prod, the shape of the 2026-09-30 incident) and `ok` (INFO) when the stage matches, so a
  deliberate setup does not warn on every run. One `dest.shared` line per sharing target; none when nothing
  shares. Rejected: always WARNING; always INFO.
- **Deploy's own dump preflight is fixed in this unit**: `checkpointPreflight` measured the data directory for
  both methods, but a dump lands on the host filesystem under `<remote_path>`. The `dump` method now measures
  `df -Pk <remote_path>` on the host, the `db` method keeps the data directory inside the DB container, and both
  compare against the shared `checkpointNeed(size, method)`, so doctor and deploy agree. Rejected: leaving it as
  a small-fix row.

## Implementation

### internal/cmd

- `doctor.go` (new): `DoctorOpts`, `DoctorResult`, `DoctorCheck`, `RunDoctor`, `parseDoctorArgs`, the three
  script builders, `splitDoctorSections` and one evaluator per check (all pure), seam `doctorRunSSH = runSSH`.
- `deploy_lock.go`: D. `deploy_git.go`: the `gitPreflight` errors become constructors shared with the `git`
  evaluator. `checkpoint_remote.go`: `checkpointNeed`, and the dump preflight on the host filesystem.

### internal/repl

- `doctor.go`: `runDoctor` renders E; `ErrUsage` through `sess.finalize` plus `exitUsage`.
- `commands.go`: `doctor` in `Registry` after `link`; `commandFlags["doctor"]`: `--from`, `--remote`, `--json`.
  `repl.go`: `dispatchNames`, the `dispatchParsed` case, a help row next to `link`. Not in `sequence`.

### main.go

- `doctor` in the always-projectless list of `projectlessOneShot`.

### Docs

- `knowledge/remote-targets.md` (doctor beside `link --show`), `knowledge/deploy-safety.md` (lock states, disk
  rule, doctor as a reader), `knowledge/deploy.md` (server rsync is checked only by doctor). `README.md`,
  `CHANGELOG.md` `[Unreleased]` Added, a ledger row in `knowledge/operations.md`; `work/build/plan.md`: done.

## Dependencies

- Unit 127: `ParseRemoteProfile` returning `ParseError` with the position, the normalized stage and
  `stageDeclared`; without it a broken profile reads as an empty one. Unit 124 (the lock).
- No new Go dependency.

## Verify when done

- [x] With the fake `ssh` (`newFakeRemote`) a healthy rsync target reports every check `ok` and `git` `skipped`,
      exits 0, and the fake `ssh` log shows three calls without `mkdir`, `mv`, `rm` or a compose verb but `exec`.
- [x] Each fault prints its line while the other checks still run: no rsync on the server, no project profile, a
      profile syntax error (file and line), no `stage` (warn), a git target that is not a clone. Any `failed`
      exits 1; warnings only exit 0.
- [x] Unreachable host (fake `ssh` exits 255): `ssh` failed, the rest `skipped reason=unreachable`, one call, exit 1.
- [x] Disk: checkpoint on and free below need is `failed`; `dump` is judged on the host `df`.
- [x] Table tests for `parseDeployLock`/`fetchDeployLock` cover the four states; existing lock tests pass unchanged.
- [x] Two targets on one `ssh_host` sharing a `readlink -f` print `dest.shared` naming the other; different hosts
      print none.
- [x] `-E`, a positional or an unknown flag exits 2 before any SSH; several targets, no `--from`, no TTY exits 2.
- [x] `--json` writes one object on stdout; the `.env` password appears in neither stream.
- [x] `echo_cli doctor --from <t>` runs outside a compose project.
- [x] `go build ./...`, `go vet ./...` and `go test ./...` pass; new tests use the existing seams (temp `HOME`,
      fake `ssh` on `PATH`, `doctorRunSSH`, `lockRunSSH`, `lookPath`, `stdinIsTTY`), never a real server.
