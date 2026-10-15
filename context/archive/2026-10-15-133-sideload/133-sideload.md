# Unit 133 · sideload
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-14.

## Goal

`sideload <path> --from <target>` installs or updates one or more addons that live **outside the project's
repo** on a remote dev/staging Odoo, without the code entering the client's repo, `odoo.conf` or
`docker-compose.yml`. Re-running after a local edit syncs and updates only what changed; re-running with no change
is a no-op. What was sideloaded is visible to `deploy --lock`, `doctor` and `compare --targets`.

This unit re-bases the July prototype (commit `2ba4e48`, numbered "Unit 75" on a branch that never merged; that
number now belongs to `remote-test`) onto the current `main` and closes the gaps the deploy-safety work opened
since. The rebased code is in the working tree of `feature/sideload`, uncommitted: build, vet and tests are green
on top of `3f62780` with one change (`remoteModuleStates` now returns three values).

Out of this unit, each a later row of the plan: checkpoint and rollback (134), `--rm` and graduation (135), local
mode (136). Reverb targets and Odoo.sh are out of scope.

## Design

Kept from the prototype (`context/specs/75-sideload.md` in `2ba4e48`):

- **File transport**, not `base_import_module`: that route loads data files only and ignores Python.
- **In-memory `tar.gz` over SSH stdin**, replace-then-untar (`rm -rf <dir> && mkdir -p <dir> && tar xzf - -C <dir>`),
  excludes `.git`, `__pycache__`, `*.pyc`, `.DS_Store`. Only `tar` is needed on the server.
- **Nested per-addon bind mounts** in an Echo-owned `docker-compose.override.yml` (marker header, deterministic,
  union of every addon sideloaded to the target). A foreign override (no marker) aborts and prints the volume
  lines. Mount base: first `addons_path` entry not under `/usr/`, from the server profile, else the container's
  `odoo.conf`.
- **Content hash per (target, addon)** in `~/.config/echo/sideload-history/<projectKey>.toml`, written only after
  a fully successful run. The hash gates the file sync; `ir_module_module` decides install vs update.

Changed in this unit:

1. **Server location outside the client's checkout.** The prototype wrote `<remotePath>/echo-addons/`, which shows
   up as untracked when `remotePath` is the repo root (every git-deploy target). Addons go to
   `<remotePath>/.echo/sideload/<addon>`, next to the deploy lock (`lockDirName = ".echo"`,
   `internal/cmd/deploy_lock.go`). The sync creates `.echo/.gitignore` (`*`) when missing, as the lock write
   does, so `.echo/` ignores itself in any repository at `remotePath`; `.git/info/exclude` is not touched.
2. **Deploy lock.** Each shipped addon is recorded with `source=sideload` (new constant next to
   `lockSourceWorktree`), `via=sideload`, `version` from the manifest, `dest` = the server path, `verified=false`
   on write and `true` after a green Odoo run, through `updateDeployLock`. The lock has no digest field: the
   content hash goes in `Tree` prefixed `sha256:` so it never reads as a git tree id. `deploy --set-code` and
   `push --clean` leave `source=sideload` entries alone (they are outside the module paths they clean).
3. **Prod is refused.** A target whose stage is `prod` (including an undeclared stage) fails with `ErrUsage`
   unless `--allow-prod` is given, and then still requires the interactive red confirm; without a TTY it fails
   closed. There is no `--force`: Echo has no staging confirm for it to skip, and on prod it must not answer.
4. **Version check.** Before any SSH write, a full five-part manifest `version` must start with the target's
   series (`<major>.0`); a mismatch is `ErrUsage` naming both. Shorter versions pass, because Odoo prefixes the
   series to them. Echo supports 17, 18 and 19.
5. **Reverb targets** fail with `ErrUsage` (Reverb owns the environment's compose and may regenerate it).
6. **Pre-flight lint** of each addon directory, same blocking rule as deploy (`err` blocks, `warn` does not),
   `--no-lint` per run.
7. **`--list`** (read-only): the addons recorded in the lock with `source=sideload`, their version, hash prefix,
   `at` and `verified`. **`--json`** on both the run (one result object on stdout, logs on stderr, as
   `deploy --json`) and `--list`.
8. **English log text.** Logger `echo.sideload[.sub]` with subs `remote`, `mount`, `sync`, `plan`, `history`,
   `lock`.

Flags after this unit: `--from`, `--dry-run`, `--allow-prod`, `--resync`, `--no-lint`, `--json`, `--list`.

The override is written (or a foreign one refused) before any addon file travels, so a refusal leaves the server
untouched.

## Implementation

- `internal/cmd/sideload.go`: the location change, the `.echo/.gitignore`, version check, Reverb refusal, prod rule,
  lint call, lock writes, `--list`, `--json` result type (`SideloadResult`: target, addons with name, action,
  version, hash, synced bool; mounts; `recreate`).
- `internal/cmd/deploy_lock.go`: `lockSourceSideload`; `--lock` rendering shows it like any other source.
- `internal/cmd/deploy_setcode.go`, `push_clean.go`: skip `source=sideload` entries when rebasing or forgetting.
- `internal/repl/sideload.go`, `commands.go`, `repl.go`: flags and help text for the new flags.
- Tests: the existing pure tests, plus the location and exclude script, the version check, the prod rule with and
  without TTY, `--list` over a fake lock, and a run against the fake `ssh` on `PATH`
  ([code-standards](../../knowledge/code-standards.md)).
- Docs: README section; [deploy](../../knowledge/deploy.md) gains a "Sideload" section (what it is for, where the
  code lives, override ownership, the lock entry); `CHANGELOG.md` `[Unreleased]` → `Added`.

## Dependencies

- none (standard library only).

## Verify when done

Test case for every live check: the `forced_config_parameter` module
(`~/Documents/Projects/odoo-forced-config-parameter`, branch matching the target's Odoo major) with
`FORCED_PARAMS = {'database.expiration_date': '2200-10-15 12:00:00'}`. The edit is made in a scratch copy of the
addon, never committed to the module's repo.

- [ ] Unit tests listed above; done gate from [code-standards](../../knowledge/code-standards.md#done-gate):
      `go build ./...`, `go vet ./...`, `go test ./...`, changed files `gofmt`-clean, `CHANGELOG.md` entry.
- [ ] `sideload <copy> --from <dev-target> --dry-run`: mount base, install plan, zero server writes.
- [ ] First run: `.echo/sideload/forced_config_parameter` exists on the server, the override has the marker,
      the container is recreated, `modstate forced_config_parameter --from <t>` is `installed`, and
      `get_param('database.expiration_date')` returns `2200-10-15 12:00:00` (`shell-run --from <t>`).
- [ ] The value holds: `set_param('database.expiration_date', '2020-01-01 00:00:00')` and a raw
      `UPDATE ir_config_parameter` both leave `2200-10-15 12:00:00`.
- [ ] Second run with no change: `up to date`, no transfer, no restart, exit 0.
- [ ] Edit the copy (add a second key), re-run: only this addon syncs, `-u` runs, both keys are pinned.
- [ ] Client cleanliness: `git status` in the server checkout is empty; `odoo.conf` and `docker-compose.yml`
      are byte-identical to before.
- [ ] `deploy --lock --from <t>` shows `forced_config_parameter source=sideload verified=true`; `sideload --list`
      agrees.
- [ ] A prod target fails with `ErrUsage` without `--allow-prod`, and fails closed headless with it.
- [ ] Register the results in the [ledger](../../knowledge/operations.md#live-verification-ledger).
- [ ] **Hypothesis to settle live:** when the mount base is itself a bind of the client's checkout, Docker creates
      an empty `<addon>/` mountpoint directory in the host checkout. Record what happens; if it does, the fix goes
      in a follow-up, not this unit.
