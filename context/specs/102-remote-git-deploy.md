# Unit 102: remote git deploy — echo branch, identical hashes, dirty overlay cleanup

## Goal

Give a remote target a **git-tracked deploy state**: commit deploys land on a
dedicated echo branch in the server's checkout **with the exact same SHAs as
local** (real object transfer, like a `git pull` — never a cherry-pick), so the
deployed code can be restored to any previously deployed hash. Dirty pushes keep
working exactly as today (an rsync overlay on top), and a new `push --clean`
removes that overlay once its content has been promoted to commits and deployed.

## The hole it closes

Every transport today is rsync ([push.go](../../internal/cmd/push.go)
`pushModuleSet`): `push`, `deploy --push`, and `watch` write plain files into
the remote addons dir. When `remote_path` is a git checkout (the linked-env
case), the server's repo sees **everything as uncommitted working-tree noise**:

- No history: you cannot tell *which* commit a server is running.
- No restore point for code: `deploy --rollback` (Unit 89) restores the **DB
  only**; the broken code stays.
- `watch` already knows the exact SHA it ships (it archives committed content
  via `git archive`, [watch.go](../../internal/cmd/watch.go) `archiveModules`)
  — but that identity is destroyed on arrival.
- After `promote` funnels dirty work into commits, the stale dirty overlay on
  the server lingers forever with no removal tool.

Identical hashes are only possible by transferring the actual commit objects:
`git push` over the same SSH host Echo already uses. Cherry-picks or
re-commits on the server would mint new SHAs and are explicitly not wanted.

## Decisions (locked with the user)

1. **Opt-in per target** via config; unconfigured targets behave 100% as today.
2. **Commit deploys preserve the dirty overlay**: only files the incoming
   commits touch are overwritten (with a WARNING listing them — the `promote`
   clobber semantics); non-colliding dirty files survive.
3. **Cleanup is a `push` flag**: `push --clean [modules]` reverts the remote
   overlay, with dry-run and confirmation.
4. **Rollback is integrated**: the deploy checkpoint records the pre-deploy
   code SHA and `deploy --rollback` restores DB + code together; a standalone
   `deploy --restore-code <sha>` moves only code.

## Config

New per-target fields in `[targets.<name>]` (and the `[connect]` single-target
form), decoded into `config.RemoteTarget` / the resolved profile:

```toml
[targets.develop]
ssh_host    = "muutrade"
remote_path = "/opt/odoo/erp"
git_deploy  = true            # opt-in switch (default false)
git_branch  = "echo/deploy"   # default when omitted
git_path    = "."             # git worktree dir, relative to remote_path
```

`git_deploy = false`/absent → every code path in this unit is inert.

## Behavior

### Preflight (once per git-mode run, before any transfer)

Fail closed, with a specific message per gap:

1. `git --version` on the remote host (git not installed).
2. `git -C <dir> rev-parse --is-inside-work-tree` (`<dir>` =
   `remote_path`/`git_path`) — not a git checkout.
3. **Same-repo check**: the local repo's root commit
   (`git rev-list --max-parents=0 HEAD`, first line) must exist on the remote
   (`git -C <dir> cat-file -e <root>`). Identical hashes require the server to
   be a clone of the same repository; a foreign repo fails closed with a
   message saying so (never silently falls back to rsync).

### Branch bootstrap (first git-mode deploy)

- `refs/heads/<git_branch>` missing → create it at the remote's current
  `HEAD` (`git branch <git_branch> HEAD`) — no worktree change, dirty intact.
- Checked-out branch ≠ `<git_branch>` → `git checkout <git_branch>` (same
  commit at bootstrap, so the working tree and its dirty overlay are untouched).

### Commit deploys advance the echo branch (replaces rsync for commits)

In `RunDeploy`, when the target is git-mode and the run includes commits, the
push phase for the **committed** content becomes:

1. **Target SHA**: the newest selected commit; every other selected commit must
   be its ancestor (always true for `watch` ranges and `--commits` taken from
   one branch's history). A non-linear selection → `ErrUsage` telling the user
   to deploy the containing tip (escape hatch: `--no-git` forces the legacy
   rsync path for one run).
2. **Object transfer** (local side):
   `git push --force <ssh_host>:<abs_git_dir> <sha>:refs/echo/incoming`.
   A holding ref — never checked out, so `receive.denyCurrentBranch` cannot
   reject it, and `--force` is safe (scratch ref). Object identity is
   guaranteed by the transport: after this, `<sha>` exists remotely verbatim.
3. **Advance** (remote side, the overlay-preserving algorithm):
   a. FF gate: `git merge-base --is-ancestor HEAD <sha>` — a diverged echo
      branch errors ("echo branch has diverged — restore or reset it first").
   b. Colliding paths = (`git status --porcelain` paths) ∩
      (`git diff --name-only HEAD <sha>` ∪ untracked paths present in
      `git ls-tree -r --name-only <sha>`).
   c. Discard only those: tracked → `git checkout -- <paths>`; untracked →
      `rm`. WARNING frame listing them (mirrors `promote`'s clobber warning).
   d. `git reset --keep <sha>` — moves branch + worktree, preserves the
      remaining dirty overlay by design; a residual abort surfaces as an error.
   e. `git update-ref -d refs/echo/incoming`.
4. Dirty modules selected in the same run still rsync afterwards (the overlay).
   `pre_push`/`post_push` actions bracket the whole sync phase as today.
5. Log frame `code synced` with `sha=<short>` `branch=<git_branch>`; the
   deploy `--json` result gains `code_sha`.

`watch` inherits all of this for free (it drives `deploy --commits --push`).
Plain `push` (modules / `--dirty`) is unchanged — it *is* the overlay.

### `push --clean` — remove the dirty overlay

`push --clean [<mod>...] [--from <t>/--remote] [--dry-run] [--force]`

- Requires a git-mode target (`ErrUsage` otherwise). Mutually exclusive with
  `--dirty`, `--dest`, `--pick-dest`, `--set-dest`, `--mkdir`, `--delete`.
- Scope: the named modules' paths inside the remote repo. No positionals →
  list the remote's dirty paths (`git status --porcelain`), map them to
  modules, and open the multi-select picker (non-TTY fails closed asking for
  names); `--all` cleans every module path.
- `--dry-run`: render the would-be-reverted files as the usual change tree
  (`deleted`/`changed` glyphs from the porcelain codes), touch nothing.
- Real run: prod gate (`confirmRemoteProd`) plus a destructive confirm showing
  the file count, then `git checkout -- <paths>` + `git clean -fd -- <paths>`,
  and a summary frame (`clean complete files=N`).

### Rollback and restore

- `config.CheckpointEntry` gains `CodeSHA` — the remote branch's `HEAD`
  captured **before** the advance (git-mode runs only).
- On-failure rollback and `deploy --rollback`: after the DB restore, when the
  entry carries a `CodeSHA`, run the same advance algorithm **without the FF
  gate** (collision discard + `git reset --keep <CodeSHA>`), so code and DB
  return together. Entries without `CodeSHA` (pre-unit, non-git targets)
  restore the DB only, as today.
- `deploy --restore-code <sha>`: code-only restore. Validates the SHA exists
  remotely (`cat-file -e`; only previously deployed hashes can be targets),
  applies the reset flow, restarts the Odoo service, prod-gated. No DB, no
  checkpoint consumed.

## Design notes

- New file `internal/cmd/deploy_git.go`: preflight, bootstrap, advance,
  restore-code, clean — all remote git plumbing behind `ckptRunSSH`-style
  package seams (`gitRunSSH = runSSH`) so every step is scriptable in tests.
- Collision computation is a pure function
  (`gitCollisions(status, diff, tree []string) []string`) — unit-tested
  without SSH.
- Flag parsing: `deployArgs` gains `noGit`, `restoreCode string`; `pushArgs`
  gains `clean`, `all`. Registered in `commandFlags` + REPL help.
- The scp-like push URL (`host:/abs/path`) rides the user's `~/.ssh/config`
  alias exactly like every other SSH call; no new auth surface.
- Server-first policy reads (the Unit 90 pattern) are **not** extended here:
  git config is deploy-topology, known locally. Revisit only if a real
  multi-client need appears.

## Out of scope

- Repos the server doesn't already have (no auto-clone/`git init`), foreign
  repos without shared history, submodules, git-lfs.
- Multi-repo addons layouts (one `git_path` per target).
- Changing `watch`, `promote`, or plain `push`/`--dirty` behavior.
- Filestore/code coupling beyond the checkpoint `CodeSHA` field.
- A `--no-git` config default (flag only).

## Verify when done

- [ ] `parseDeployArgs`: `--no-git`, `--restore-code <sha>` (value required),
      invalid combos → `ErrUsage`; `parsePushArgs`: `--clean` + exclusivity
      matrix → `ErrUsage`.
- [ ] `gitCollisions`: dirty∩diff plus untracked-in-tree cases, empty sets.
- [ ] Scripted-seam tests: preflight failures (no git / not a repo / foreign
      repo), branch bootstrap (missing branch, wrong checked-out branch),
      advance (FF gate rejection, collision discard order, holding-ref
      cleanup), rollback with and without `CodeSHA`.
- [ ] Non-linear `--commits` selection on a git-mode target → `ErrUsage`;
      same selection with `--no-git` → legacy rsync path.
- [ ] A non-git target runs byte-for-byte the same code path as today
      (regression: existing deploy/push/watch tests untouched and green).
- [ ] `go build ./...` + `go test ./...` pass.
