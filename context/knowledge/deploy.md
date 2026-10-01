# Deploy
> Owns how code and module state reach a remote target: selection, module sources, push and destinations, git-deploy topology, deploy-branch re-basing, watch, promote, deploy history, and the config precedence table. Safety machinery (checkpoints, rollback, snapshot, lock, actions) lives in [deploy-safety.md](deploy-safety.md). Last verified: 2026-09-30.

Code authority: `internal/cmd/deploy.go` (`RunDeploy`), `deploy_source.go`, `deploy_git.go`, `deploy_setcode.go`, `deploy_setbranch.go`, `push*.go`, `watch.go`, `promote*.go`. All of `deploy`, `push`, `watch`, `promote`, `checkpoint`, `actions` are projectless one-shots (`projectlessOneShot`, `main.go:236`); the project key is the git toplevel. Target resolution, link bindings and the remote profile are in [remote-targets.md](remote-targets.md); the prod-gate rule (remote stage, fails closed without TTY) is in [architecture-and-traps.md](architecture-and-traps.md). A Reverb environment addressed with the deprecated `-E` refuses `deploy`, `watch` and `i18n-pull` (`requireNoReverb`, `internal/cmd/reverb.go:438`); a link-mode Reverb target deploys like any classic target, but Reverb's own deploy replaces the addons directory wholesale, so code Echo ships there lasts until the next Reverb deploy.

## Mental model

- `deploy` = ship code (optional) + one Odoo run `-i <new> -u <existing>` on the target, with checkpoint/rollback around it. Install vs update is decided per module from the target's `ir_module_module` state: `installed` / `to upgrade` become `-u`, anything else `-i` (`splitInstallUpdate`). Both go in ONE `odoo` run (`odoo.InstallUpdate`), executed by `exec` inside the already-running Odoo container.
- Code transport is separate from module selection. Two topologies: **rsync** (default) and **git-deploy** (opt-in per target). Each module also has a **source** (where its content comes from): see [Module sources](#module-sources).
- Without push (`--no-push`, or push default off) the selection only names which modules to `-u`; whatever code is on the server runs. This is why `mod@ref` refuses `--no-push`.
- **Metadata never fails a deploy**: lock, provenance keys, deploy history, checkpoint index writes are WARNINGs on failure (`updateDeployLock`, `recordDeployedRef`, `MarkDeployed`). Lint that fails to *run* is also a WARNING.

## Selection

- Three mutually exclusive ways in. **Picker** (TTY only): last `--limit` (20) commits of the current branch, dirty modules first as `~ <module> · uncommitted (N files)`, already-deployed commits muted. **`--auto`**: commits in `@{upstream}..HEAD` minus deployed history, plus every dirty module; nothing pending logs `nothing to deploy` and exits 0; no upstream degrades to dirty-only. **`--commits <sha,…>` / `--modules a,b`**: non-interactive. No TTY and no selection flag fails closed with `ErrNonInteractive` (exit 2) before any picker (`requireTTY` in `RunDeploy`).
- `--modules` entries are validated against the local repo (`__manifest__.py` must exist, else `ErrUsage`, exit 2) before any remote contact.
- Commit to module: subject `[Tag] module: title` counts only if `<module>/__manifest__.py` exists; otherwise the commit's changed paths must map to exactly one addon. Zero or several = skipped with a WARNING, never fatal unless every commit is skipped (`resolveCommitModule`).
- **`--modules foo` (no ref) is a dirty-style selection**, not "foo as committed": it ships the working tree (source `worktree`), even if the module is clean (`deploySelectionFromFlags` builds a bare `dirtyModule`). Use `mod@ref` to ship committed content.
- Picker marks: `ctrl+d` toggles "already deployed" on a commit row, `ctrl+a` marks or unmarks every visible commit (`internal/cmd/picker.go:182`). Toggles are written once, on Enter, before the prod gate, as a net delta (`config.UpdateDeployedMarks`); esc discards. Dirty rows are never markable. Build mode (`sequence`) opens the picker read-only: it mutes by history but cannot mark.
- The WARNING "selected modules have uncommitted changes — deploy updates them on the server but does not push the code" is printed even when push is on and the working tree is in fact shipped; the log line is stale, behavior is in [Module sources](#module-sources).
- Per-run flags that shape the run: `--i18n` / `--no-i18n` (mutually exclusive): `--i18n-overwrite` is auto-on when a selected commit (or dirty path) touches `<module>/i18n/` of an *update-set* module; it is process-global in the single Odoo run, so one trigger overwrites translations of ALL updated modules, and the plan line says so (`i18n=on|off|forced|suppressed`). Install-set modules never trigger it. `--no-lint`: see below. `--dry-run`: still performs the reads and the plan (`code`, `modules resolved`, the dependency check), runs the push in dry mode (rsync `-n`, git preflight, no snapshot, no lock write) and stops before the prod gate.
- `--json`: logs and stream go to stderr, one `DeployResult` object to stdout (modules with `source/sha/version`, `skipped`, `planned`, `checkpoint`, `rolled_back`, `code_sha`, `dependencies`); the rsync change tree is suppressed so stdout stays parseable.

## Module sources

| Source (lock value) | Chosen when | rsync target | git-deploy target |
|---|---|---|---|
| `worktree` | dirty module, `--modules m` (no ref), standalone `push` | rsync from disk, no `--delete` | same (an overlay on top of the branch) |
| `commit` / `branch` | module resolved from selected commits | rsync from `git archive` at the module's newest selected commit, `--delete` (lock: `commit`) | rides the deploy branch (lock: `branch`) |
| `ref` | `mod@ref`, or `--at <ref>` for every un-pinned `--modules` entry | rsync from `git archive` at the ref, `--delete` | overlay from the archive, branch untouched |

Source: `RunDeploy` (`sources`, `archived`, `branchMods`, `worktreeMods`), `deploy_source.go`.

- On rsync targets a commit selection ships the **tree at the commit**, not the disk: later commits and uncommitted edits to that module do not travel (a WARNING says a dirty edit is ignored). Origin: Unit 125, commit `1d29ccb`.
- **On git targets the branch advances as a whole** (`gitAdvance` does `reset --keep <tip>`), so every module in that commit's tree changes on disk, but only the selected modules get `-u` and only they go through the [dependency check](deploy-safety.md#dependency-check). There is no per-module partial on the branch path.
- A module is located in the REF's tree (`locateModuleAt`): same relative path as on disk if it holds the module there, else a unique `__manifest__.py` directory at depth 1-2; several matches = `ErrUsage`. The module need not exist on disk.
- `ref` rules: refs resolve locally (`resolveLocalRef`); a `<remote>/<branch>` ref is fetched first, `--fetch` forces and `--no-fetch` suppresses it (failure = WARNING); `--at` needs `--modules` and rejects `--commits`; `ref` + `--no-push` is `ErrUsage`; a module both in the commit selection and pinned is `ErrUsage`. A `ref` deploy marks NO commit in deploy history.
- Non-linear commits: for one module on rsync, or for the whole selection on git, is `ErrUsage` (`resolveGitTip`); escape for git is `--no-git`.
- **Last ship wins**: re-shipping a module pinned by `ref` through another source first drops the pinned tree (rsync: `--delete`; git: its overlay is reverted, log `pin released`). `deploy --set-code` replaces pins unless `--keep-overlay`.
- Plan shows per module `ship=<source>@<sha>`, `version` at the source, `installed` (from `ir_module_module.latest_version`) and `locked=` (what the [lock](deploy-safety.md#deploy-lock) says is there now). A pinned module's i18n change is detected by comparing the `i18n/` tree id at the ref with the lock's `sha`; no lock entry means "unknown" (INFO pointing at `--i18n`).
- Pre-flight lint (Unit 110) runs after selection and BEFORE the first SSH, so a block costs nothing remote: no push, snapshot, checkpoint or rollback. It blocks only on `err` findings (manifest-listed files); `warn` never blocks. Archived modules are linted from the archive dir, everything else from the working tree (`lintScopes`). `--no-lint` is per run and logged, deliberately not a config key (a persisted opt-out ends up on the machine that needed the check most).

## Step order

`RunDeploy` order. Everything before step 4 is read-only on the server. From step 4 on, any failure goes through the [rollback decision](deploy-safety.md#rollback-decision).

0. Standalone modes return first, before selection: `--set-push`, `--test-*`, `--set-checkpoint*`, `--rollback`, `--restore-code`, `--lock`, `--set-git-branch`, `--set-code`.
1. Local planning: validate `--modules`, resolve `mod@ref` pins, resolve the target (no connection), load deploy history, detect dirty modules, select, resolve commits to modules, decide sources, extract commit trees to a scratch dir, **lint**.
2. First SSH: read the remote profile (stage, db, containers, server-side settings) and DB credentials; resolve push, checkpoint policy and tests; read the lock (when pushing); query module states; i18n decision; print the plan; resolve and validate actions; when pushing, the `code` lines and the [dependency check](deploy-safety.md#dependency-check) (two more SSH reads; `--no-dep-check` skips it). `--dry-run` ends here (after a dry push).
3. Gates: dependency findings on a non-`dev` stage ask (fail closed without a TTY; `--force` skips); `--test` on a prod target needs `--force`; prod confirmation (`confirmProd`, remote stage; `--force` skips).
4. `pre_push` actions (push only). Failure aborts; nothing to undo.
5. Push (`runPush`), in order: resolve explicit destination; **code snapshot** (first-write boundary, `codeWritten = true`); release pins; git: preflight, bootstrap, push objects, FF gate, advance; rsync worktree modules, then archived modules (`--delete`).
6. Lock write (`record(shipped)`, `verified=false`).
7. `post_push` actions.
8. Disk preflight (only when a checkpoint is planned). Runs after the push, so a doomed checkpoint still ends in a code restore.
9. `pre_deploy` actions.
10. Stop: only the Odoo app when checkpointing (the DB container must stay up for `psql`/`pg_dump`), otherwise `compose stop` of everything.
11. Checkpoint.
12. `up -d`.
13. Odoo run: `-i`/`-u`, `+ --i18n-overwrite`, `+` tests when enabled.
14. Verify: non-zero exit OR any `deployFailureRe` hit in the stream (`CRITICAL`, traceback, `Failed to load registry`, non-zero `N failed, M error(s)`, `FAILED (failures|errors=`) fails the deploy.
15. Lock `verified=true`; mark deploy history; record the checkpoint (links the code snapshot) and prune to `keep`, or delete the snapshot when no checkpoint exists.
16. `post_deploy` actions. A failure marks the run failed and never rolls back a verified-green deploy.

Failures before the first code write return plainly unless a checkpoint exists (`fail` in `RunDeploy`). Without push, `codeWritten` stays false and only a checkpoint can be restored.

## Per-run tests (Unit 100)

- `--test` / `--no-test` per run; `--test-toggle` flips the persisted `[deploy] test` and prints `test=on|off`. Pinned list `[deploy] test_modules` is edited with `--test-modules` (picker), `--test-modules=csv`, `--test-add`, `--test-rm`, `--test-clear`; empty means "test what this deploy deploys". Tests append `--test-enable --test-tags /<m>,… --no-http --http-port=8189 --log-level=test` to the same run (`odoo.WithTests`); the port isolation exists because the run shares the container with the live server on 8069.
- Tests do not force a checkpoint. On prod they need `--force`. A failing suite fails the deploy through verify and so triggers the normal rollback.

## Push and destinations

- `push` rsyncs modules from the working tree to the target host filesystem: `-az --checksum --itemize-changes`, excludes `__pycache__`, `*.pyc`, `.git`, trailing slash on both ends, `--delete` opt-in (`rsyncArgs`, `push.go:378`). `--checksum` is required because `git archive` stamps every file with the commit time, so size+mtime would re-sync whole modules each ship. Needs `rsync` locally and remotely (`requireRsync`). Output is parsed into a typed change tree, not printed raw.
- Standalone `push` takes **no code snapshot** and cannot roll back; it writes the lock with `via=push`, `verified=false`. It is always an overlay (`worktree`), also on git targets. Remote-host-FS only: a conf-mode remote whose addons live in the image fails closed unless an explicit destination exists.
- Destination (`resolvePushDest`, `resolvePushDestination`): `--dest` › `--pick-dest` › server `[push] path` › local `[push] path` › auto-detect. An explicit path bypasses the probe; modules land at `<dest>/<module>`; relative joins `remotePath`, absolute is used as-is; the compose root (`.`) is rejected; it must exist unless `--mkdir` or `[push] mkdir = true`. Auto-detect decides by the REMOTE layout (existing module location, else the first existing relative addons path, else `addons`/`custom`), never by the local cwd. A shared destination between targets is legal (habitta's `.cache/all_odoo`), which is why the lock does not live there.
- The picker is TTY-only and only for standalone `push`; `deploy --push` and `watch` never open one. In a TTY an auto-detect failure falls into the picker; headless it fails closed. A picked path is stored relative when under `remotePath`.
- `push --clean [mods|--all]` reverts a git target's overlay (`git checkout --` for tracked, `git clean -fd` for untracked), with `--dry-run`, prod gate and destructive confirm; it removes the entries from the lock. It needs a git-deploy target (Reverb: the overlay dir is emptied instead).

## Git-deploy topology

- Opt-in per target: `git_deploy`, `git_branch` (default `echo/deploy`), `git_path` on `[connect_targets.<t>]` or the project `[connect]`. Resolution (`resolveGitDeploy`): the named target is authoritative (its `git_deploy=false` disables), else a physical `ssh_host+remote_path` match against named targets, else `[connect]`. **Local config only, never server-first**; it describes how this machine ships.
- Transport keeps the real hashes: `git push --force <host>:<abs_git_dir> <sha>:refs/echo/incoming` (a scratch ref never checked out, so no `receive.denyCurrentBranch`), then on the server: FF gate (`merge-base --is-ancestor`), discard only overlay paths that collide with the move (WARNING lists them), `git reset --keep <sha>`, delete the scratch ref (`gitDeployCommitted`, `gitAdvance`).
- The dirty overlay survives commit deploys. Collisions = `status` paths that appear in `diff HEAD..sha`, plus untracked paths that exist in the sha tree (`gitCollisions`).
- Preflight fails closed (no git on host, not a work tree, foreign repo: the local root commit must exist remotely) and never falls back silently to rsync. `--no-git` forces rsync for one run.
- Provenance on the server checkout's own git config: `echo.deployed-ref/-sha/-at`, written by deploy, `--set-code` and cleared by `--restore-code` (a hash has no ref). Shown by `link --show` (`deploy code` line). Readable with plain `git config --get` without Echo.
- Out of scope: auto-clone, submodules, LFS, multi-repo layouts.

## Re-basing the deploy line

The deploy line (local `[promote]` branch plus the server's deploy branch) keeps accumulating after a merge to main; the FF gate refuses to move it off a diverged history. Re-basing is deliberate:

- `deploy --set-code <ref>`: force-moves the git target to ANY ref resolved locally (a branch that exists only on this machine works, objects travel over SSH). No FF gate, no DB, no checkpoint, no lint. Overlay is cleaned by default, scoped to module paths (`moduleScopedEntries`) so `odoo.conf`, override files and `filestore/` survive; `--keep-overlay` opts out. Confirms with direction (`ahead|behind|diverged`), restarts the Odoo container, re-seeds deploy history with the new tip (`ResetDeployedSHAs`), rebases the lock. `--with-local` first resets the local `[promote]` branch (aborts before any remote change on collision).
- `deploy --restore-code [sha]`: branch back to a hash already on the server (picker over the remote branch log, or explicit), restart, **deploy history untouched**, so `--auto` will still skip those commits.
- `deploy --set-git-branch <name>`: config-only; `--rename` also runs `git branch -m` on the server (tree, index and overlay untouched). It does NOT create a branch named after a ref: a real-looking branch name implies sync with `origin`, which Echo never pulls (Unit 113). Not to be confused with `promote --set-branch` (the local accumulation branch).
- Older checkpoints still restore (their `CodeSHA` objects remain) but return the server to the line before the move; `--set-code` warns when checkpoints exist.
- `promote --reset [base]` is the local half (see Promote); `deploy --set-code <base> --from <t>` the remote half.

## Watch

`watch [branch]` polls `git rev-parse refs/heads/<branch>` every `--interval` (default 10 s, floor 2 s); refs are shared across worktrees, so a commit from any worktree triggers a cycle; no fsnotify. The branch must exist locally; it need not be checked out (`watch.go`). Omitting it opens a branch picker (TTY only).

- Cycle: fast-forward check; a rewritten ref (rebase/amend/reset) logs `branch rewritten`, re-baselines, deploys nothing. Otherwise commits `old..new` resolve to modules (unresolved skipped like the picker) and watch runs `deploy --commits <shas> --force --push [--from] [--no-checkpoint] [--no-actions]` with `Via = "watch"` (`deployCommitsHeadless`). **Watch does not archive anything itself**; deploy ships each module's tree at its newest selected commit from `git archive` (Unit 125). It never passes `--no-push`.
- `--force` on the inner deploy means: no prod prompt, and a failed run rolls back automatically (`rollbackDecision`). Watch cannot be told otherwise; an unattended monitor must never leave a broken DB. Actions run each cycle; an action failure fails only that cycle.
- A cycle failure logs ERROR and the baseline STILL advances (those commits stay unmarked, so the next `deploy --auto` or picker finds them). Only setup errors end the loop. Starting on prod requires `--force`; Ctrl+C ends with `watch stopped cycles=N deployed=N rollbacks=N`, not an error.
- Each cycle that reaches deploy writes one local `watch-deploy` cmd-log record (SHAs in the command, full tip in `deployed_tip`, exit 0/1); cycles that deploy nothing write none. "Did my commit auto-deploy" is `git merge-base --is-ancestor <sha> <deployed_tip>` (watch batches commits). Reading it (`logview --json`) is in [scripting.md](scripting.md).
- Monitor mode: a side goroutine follows `compose logs --no-log-prefix -f` of the Odoo container while idle, as decoration. It is stopped SYNCHRONOUSLY before any cycle output (no interleaving), reopened with `--tail 0` after each cycle (deploy recreates containers), `--tail 20` at start; a stream that dies on its own warns and retries after one interval. `--no-logs` for tmux/CI. Not offered in `sequence` (does not terminate). Requires `rsync` locally even on git targets (`requireRsync` at start).

## Promote

`promote` is the LOCAL funnel from a feature worktree into the single deploy branch; no SSH, push, deploy or commit (`promote.go`). The invariant behind it: the instance is fed from ONE branch (history is per target SHA, watch follows one ref), and git forbids the same branch in two worktrees, which is why the destination is a separate worktree.

- Destination is a BRANCH; its worktree is discovered live (`git worktree list --porcelain`). Resolution `--to` › `[promote] branch` (project over global) › TTY picker › headless `ErrUsage`; **no hardcoded default** (a code comment in `resolveDest` still says `develop`; the code has none). `--create-dest <path>` creates the worktree. `--from` is never reused as a source flag (it means remote target everywhere).
- **Dirty mode** (`--dirty [folders]`): copies the modules' changed files into the destination, last-write-wins, destination left dirty on purpose; WARNING lists files that already had uncommitted changes there (`destDirtyPaths`). It is a file copy, not `git apply`: apply validates against the destination index and fails exactly in the promote cases (dirty or untracked destination). Atomic via `snapshotFiles`/`restoreFiles`. **Commits mode** (`<branch> [--commits]`): cherry-pick, deduped with `git cherry`; a conflict aborts leaving the destination intact (`ErrPromoteConflict`).
- `promote --reset [base]` moves the branch back onto `[promote] base` with `reset --keep` (refuses on collision; `--discard` = hard plus removal of module-scoped untracked files); confirm only when commits would leave the branch or discard is asked. Server code is untouched. `--set-base`/`--set-branch` write **global** `global.toml`.
- `promote --show-branch` (read-only, no cmd-log): `branch`, `source=project|global`, `worktree=<path>|none`, plus `base`, `base_source`, `ahead`, `behind`, `dirty` when a base is set (`base=none` otherwise). Exit 0 configured, exit 1 with a WARNING when not (documented deviation, scriptable: `if echo_cli promote --show-branch`). A large `behind` after a merge is the signal to re-base.

## Deploy history vs the lock

- **Deploy history** is LOCAL (`~/.config/echo/deploy-history/<projectKey>.toml`, 0600): per target (`sha256(sshHost \x1f remotePath)`) the set of commit SHAs shipped, capped at 1000 newest. It answers "which of my commits did I ship here?" for the picker and `--auto`. Written only at the end of a successful deploy, only for commits that resolved to a module; dry-run, declined gates, failures and rolled-back runs record nothing. Identity is the full SHA, so rebased/amended commits and a second machine read as new (hence `ctrl+d`). Loads are best-effort.
- **The lock** is SERVER-side code provenance, per module ([deploy-safety.md](deploy-safety.md#deploy-lock)). It does not replace the history and the history is not migrated into it.
- `deploy --rollback` un-marks the checkpoint's SHAs; `--set-code` re-seeds the history with the new tip.

## Config precedence

Each "layer" below is itself project profile over `global.toml` (the server's layers are its own files, see [remote-targets.md](remote-targets.md)). Stage under `auto` is the target's stage from the server profile.

| Setting (keys) | Resolution order | Shape |
|---|---|---|
| ship code (`[deploy] push`) | `--no-push` › `--push` › server › local › false (`resolveDeployPush`) | scalar `*bool`, first set wins |
| run tests (`[deploy] test`) | `--no-test`/`--test` › server › local › false | scalar `*bool` |
| test modules (`[deploy] test_modules`) | non-empty server list › non-empty local list › the deploy's own modules | **wholesale** |
| checkpoint (`[checkpoint] mode/method/keep`) | `--no-checkpoint` / `--checkpoint[=db\|dump]` › server › local › defaults `auto/db/2`; under `auto`, stage staging/prod = on, dev = off | **field by field** (server field overrides only if set; the server profile has no defaults) |
| actions (`[[deploy.actions]]`) | `--no-actions` › non-empty server list › non-empty local list › none | **wholesale** |
| push destination (`[push] path/mkdir`) | `--dest` › `--pick-dest` (push only) › server path › local path › auto-detect | path: first set wins; `mkdir` comes from the winning side OR `--mkdir` |
| rollback on failure | `--rollback-on-fail`/`--no-rollback-on-fail` › `--force` › TTY ask › headless: roll back | no config key |
| lint | `--no-lint` | no config key |
| dependency check | `--no-dep-check` | no config key |
| git topology (`git_deploy/_branch/_path`) | named target › physical match › `[connect]` | local only, no server layer |
| `[promote] branch/base` | flag/positional › project › global, each field independently | local only |

- Wholesale exists because merged lists give an unpredictable order across machines. Consequence: **an empty server list falls back to the local list**; `actions rm --from` warns with the local count. Field-by-field applies where partial declaration is natural (a server that only sets `keep`).
- **Config-only setter pattern**: `push --set-dest`, `deploy --set-push[=bool]`, `--set-checkpoint[=on|off|auto] --set-checkpoint-method --set-checkpoint-keep` (composable, keep ≥ 1), the `--test-*` family, `--set-git-branch` (without `--rename`), `promote --set-branch/--set-base`. Short-circuit at the top of `Run`, persist, log the resulting value, exit; no remote resolution, no prod gate. Deploy and push setters write ONLY the local project profile (`SaveProject`); `[checkpoint]` is re-emitted only when `CheckpointSource == "project"`, so a global-only policy is never copied into the project. Promote setters write `global.toml`. A machine-wide checkpoint default is a hand edit of `global.toml`. `init` does not prompt for `[checkpoint]`.
