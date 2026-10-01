# Unit 112: re-basing the deploy line — `deploy --set-code`, `promote --reset`

## Goal

Make the deploy line **re-baselineable**: point a git-deploy target's code at
*any* local or fetched ref (`main`, `origin/feat/x`, a tag, a SHA) instead of
only at hashes it has already run, and reset the local accumulation branch onto
a declared base — so the recurring "I merged to `main`, started a new feature,
and now `develop` and the server are a line nobody can get back to" cycle is
two commands instead of hand-run git across a worktree and an SSH session.

## The hole it closes

The one-deploy-source invariant (Unit 97) plus git-mode deploys (Unit 102) give
a clean forward path and **no way back to a different line**:

- `gitAdvance` ([deploy_git.go:199](../../internal/cmd/deploy_git.go:199)) gates
  every deploy on `merge-base --is-ancestor HEAD <tip>` and, when it fails,
  says *"restore or reset it first"* — a reset that no command performs.
- The only non-fast-forward move is `deploy --restore-code`
  ([deploy.go:1578](../../internal/cmd/deploy.go:1578)), deliberately limited to
  hashes **already on the server** (`gitRemoteHasCommit` + a picker over the
  remote branch's own history). It is a rollback tool. It cannot take the
  server to `main`, to a branch a teammate pushed, or to a tag.
- Locally, `promote --set-branch` only renames the destination. Nothing re-bases
  `develop`, and `develop`'s worktree is dirty **by design** (promote leaves it
  unstaged), which is exactly why a by-hand `git reset --hard` is the operation
  everyone postpones.

So after a merge to `main` the deploy line keeps accumulating on top of history
that is no longer an ancestor of anything, and the cost of straightening it out
grows with every promote.

Everything needed to fix this already exists and is tested: `gitPushObjects`
ships the objects of **any** local commit (it does not care whether the commit
exists on `origin`), `gitBootstrap` creates the deploy branch when it is
missing, `gitAdvance(..., ffGate=false)` moves a branch backwards or sideways
while preserving the overlay, and `runPushClean` knows how to erase the overlay.
This unit wires them into two entry points and declares the missing concept:
**the deploy line has a base**.

## Decisions (locked with the user)

1. **Full scope**: the remote move *and* the local reset, plus the divergence
   readout that tells you when to run them.
2. **`--set-code` cleans the overlay by default.** "Take the server to this ref"
   means the checkout looks like the ref, not like the ref plus leftovers from
   modules the new base may not even contain. `--keep-overlay` opts back into
   the incremental-deploy semantics.
3. **The local reset uses `reset --keep`**: it preserves the dirty work that
   does not collide with the new base and **aborts without destroying anything**
   when it does. `--discard` is the explicit `--hard`.
4. **`promote` stays purely local** (the Unit 97 invariant): there is no
   `promote --reset --push`. The one-gesture form lives on the side that already
   owns SSH, target resolution and the prod gate — `deploy --set-code
   <ref> --with-local`.
5. The `odoo-probe` skill is part of this unit's scope (§ Skill), because a
   command that force-moves a server's code is exactly the kind an agent must
   not discover by guessing.

## Behavior

### A. `deploy --set-code <ref>` — move the server to any ref

```
deploy --set-code <ref> [--from <target>] [--fetch|--no-fetch] [--keep-overlay]
                        [--with-local] [--dry-run] [--force] [--json]
```

Standalone, like `--restore-code`: mutually exclusive with a deploy selection
(`--commits`/`--modules`/`--auto`), with `--rollback`, with `--restore-code` and
with `--no-git`. Requires a git-deploy target — otherwise
`ErrUsage: --set-code needs a git-deploy target (set git_deploy on it)`.

**1. Resolve the ref locally.** `git rev-parse --verify <ref>^{commit}` in the
local root. When `<ref>` is `<remote>/<branch>`, or `--fetch` is given, run
`git fetch <remote>` first (`--no-fetch` suppresses it), then resolve. An
unresolvable ref is `ErrUsage` naming what was tried — never a silent fallback
to a different commit. Resolution is **local only**: the server never needs
network access, and the ref never needs to exist on `origin`, which is the whole
point of Echo (a branch that lives only on your machine deploys fine).

**2. Preflight.** `gitPreflight` unchanged — git present, path is a checkout,
same repository (shared root commit).

**3. Bootstrap.** `gitBootstrap` — creates `<git_branch>` at the remote's
current `HEAD` and checks it out if needed. Its return value is the
**previous** code SHA, reported in the result and the closing line.

**4. Clean the overlay** (default; skipped by `--keep-overlay`). Reuses the
`push --clean --all` machinery: `remoteDirtyEntries` → keep the entries whose
path maps to a module (`moduleOfPath` non-empty) → `runRemoteClean`
(`git checkout --` for tracked, `git clean -fd` for untracked). **Scope is
module paths, never the repo root**: a server's `odoo.conf`, its
`docker-compose.override.yml` or an untracked `filestore/` are not part of any
deploy line and must survive a re-baseline. Cleaning runs **before** the
advance, so the advance has no collisions left to warn about.

**5. Transfer and move.** `gitPushObjects(tip)` then
`gitAdvance(..., ffGate=false)`. The absent FF gate *is* the feature: forward,
backward and sideways are all legal here, which is the difference between this
command and a deploy.

**6. Restart** the Odoo container, as `--restore-code` does. No DB is touched
and no checkpoint is created — this command moves code and nothing else. Any
module the new base needs installed or upgraded is a separate, explicit
`update`.

**7. Re-baseline the deployed-SHA history.** `config.LoadDeployedSHAs` for this
target is a claim about a line that no longer exists; after a real run it is
truncated and re-seeded with the new tip (new
`config.ResetDeployedSHAs(projectKey, targetKey, seed []string)` in
[deploy_history.go](../../internal/config/deploy_history.go)). Leaving it stale
would make the next `deploy --auto` skip commits it has never actually shipped.

**Confirmation.** `confirmRemoteProd` plus a destructive confirm showing the
target, `<branch>`, `<prev-short> → <tip-short>`, the direction (`ahead` /
`behind` / `diverged`, from `merge-base`), and the number of overlay files to be
cleaned. `--force` bypasses the second one; headless without `--force` fails
closed.

**`--dry-run`** performs the read-only remote calls (preflight, status,
merge-base) and renders the would-be-cleaned files as the usual change tree plus
the intended advance, writing nothing — local or remote.

**`--with-local`** additionally resets the local `[promote] branch` worktree to
the same ref, using the § B core, and runs it **first**: a local reset that
cannot proceed (a colliding dirty file) must abort before anything remote moves.
Without a configured `[promote] branch` it is `ErrUsage`.

**Closing lines and `--json`:**

```
echo.deploy.git: code set branch=echo/deploy ref=origin/main sha=a1b2c3d prev=9f8e7d6 cleaned=12
echo.deploy: code set target=develop db=erp_dev took=6.2s
```

`--json` gains `ref`, `previous_sha`, `cleaned` alongside the existing
`code_sha`. The run is recorded in the cmd-log by the REPL/one-shot wrapper that
records every deploy — `"command": "deploy"` with `--set-code <ref>` in its
`cmd` — so `logview --json` confirms what a server was moved to and when. It
deliberately does **not** mint a second `"set-code"` record: `watch` needed its
own kind because its deploys are nobody's typed command, which is not the case
here.

### B. `promote --set-base` / `promote --reset` — re-base the local line

```
promote --set-base <ref>                          # config-only, persists [promote] base
promote --reset [<base>] [--discard] [--no-fetch] [--dry-run] [--force]
```

`--set-base` mirrors `--set-branch` exactly: config write, no git mutation, exit.

`--reset` operates on the **destination worktree** — the worktree holding
`[promote] branch`, resolved with the existing cascade (`worktreeForBranch`,
`--create-dest`, the TTY picker) — not on the cwd. Running it from a feature
worktree is the normal case.

Base resolution: positional `<base>` › `[promote] base` › (TTY) a picker over
`origin/main`, `main` and the branches of the other worktrees › (headless)
`ErrUsage` asking for `<base>` or `promote --set-base`. A `<remote>/<branch>`
base is fetched first unless `--no-fetch`.

**Mechanics**, the local mirror of the remote advance:

- Default: `git -C <dest> reset --keep <base-sha>`. Uncommitted work that does
  not collide with the move survives; work that does collide makes git refuse,
  and the refusal is surfaced as `ErrPromoteConflict` listing the files and
  hinting `--discard`. **Nothing is destroyed on the default path.**
- `--discard`: `git reset --hard <base-sha>`, plus removal of the untracked
  overlay **scoped to module directories** (same `moduleOfPath` rule as the
  remote clean — a stray `.env` at the worktree root is not this command's
  business).
- Either way the branch itself moves: `develop` now points at the base. The old
  tip is printed (`prev=<sha>`) and remains reachable through the reflog; the
  work it carried is, by construction of this workflow, already in the base via
  the merge. That is stated in the confirm, not assumed.

**Preview** (`--dry-run` and the confirm): the change tree from
`git diff --name-status HEAD <base>` plus the untracked files a `--discard`
would remove, rendered with `BuildSyncTree` like every other promote output.

```
echo.promote: reset complete branch=develop base=origin/main sha=4d5e6f7 prev=1a2b3c4 kept=3
echo.promote: hint next=deploy --set-code origin/main --from <target>
```

### C. `promote --show-branch` — say when a reset is due

The Unit 103 query gains the line's state, because "should I re-base?" must be
answerable without running git by hand:

```
echo.promote: promote branch branch=develop source=project worktree=/Users/…/develop \
              base=origin/main ahead=3 behind=41 dirty=6
```

`ahead`/`behind` come from `git rev-list --left-right --count <base>...<branch>`;
`base=none` when unconfigured (and the `ahead`/`behind` fields are omitted, not
faked). Exit codes are unchanged: `ErrNotConfigured` still means *no branch*
configured — a missing base is a WARNING-free `base=none`, since plenty of
setups never re-base.

### Interaction with the rest of the system

- **`watch`** needs nothing. Its cycle already detects a rewritten ref and
  re-baselines ([watch.go:294](../../internal/cmd/watch.go:294)); after a reset
  the first cycle logs `branch rewritten — re-baselining, nothing deployed` and
  carries on from the new tip.
- **Checkpoints** keep working: the commits their `CodeSHA` points at are still
  in the server's object store, so `deploy --rollback` and `--restore-code`
  still resolve. The confirm warns that restoring one of them returns the server
  to the **pre-reset line**.
- **`push --clean`** is unchanged; `--set-code` calls the same core, so the two
  cannot drift.

## Config

```toml
[promote]
branch = "develop"      # existing (Unit 97)
base   = "origin/main"  # new — what the line is re-based onto
```

Valid in `global.toml` and in a project file, project wins — the same
precedence and the same `PromoteBranchSource` treatment.
`config.Config` gains `PromoteBase` / `PromoteBaseSource`;
`config.SavePromoteBase(ref string) error` mirrors `SavePromoteBranch`
([config.go:841](../../internal/config/config.go:841)).

## Implementation

| File | Change |
|---|---|
| [internal/cmd/deploy.go](../../internal/cmd/deploy.go) | Parse `--set-code[=<ref>]`, `--fetch`/`--no-fetch`, `--keep-overlay`, `--with-local`; extend the exclusivity block; dispatch `runDeploySetCode` next to `runDeployRestoreCode` (before the selection, so the lint pre-flight of Unit 110 never runs for it) |
| [internal/cmd/deploy_git.go](../../internal/cmd/deploy_git.go) | `resolveLocalRef(ctx, root, ref, fetch)` (fetch-then-`rev-parse`, returns SHA + the resolved remote), `gitSetCode(...)` orchestrating bootstrap → clean → push → `gitAdvance(ffGate=false)`, `gitAheadBehind` for the confirm/readout |
| [internal/cmd/push_clean.go](../../internal/cmd/push_clean.go) | Extract the scope+revert core out of `runPushClean` into `cleanRemoteOverlay(ctx, rsc, absDir, modules []string) ([]remoteDirtyEntry, error)` so `--set-code` and `push --clean` share one implementation |
| [internal/cmd/promote.go](../../internal/cmd/promote.go) | `--set-base`, `--reset`, `--discard`, `--no-fetch` in `promoteArgs` + exclusivity; dispatch before the source/mode resolution (a reset has no source); `runShowBranch` emits the base/ahead/behind/dirty fields |
| [internal/cmd/promote_git.go](../../internal/cmd/promote_git.go) | `resetBranchToBase(ctx, destPath, baseSHA, discard bool) (kept []string, err error)` — the core `--with-local` also calls |
| [internal/config/config.go](../../internal/config/config.go) | `PromoteBase`, `PromoteBaseSource`, `[promote] base` decode (global + project), `SavePromoteBase` |
| [internal/config/deploy_history.go](../../internal/config/deploy_history.go) | `ResetDeployedSHAs(projectKey, targetKey string, seed []string) error` |
| [internal/repl/repl.go](../../internal/repl/repl.go), [commands.go](../../internal/repl/commands.go) | Help rows for the new flags; completion entries for `deploy` and `promote` |
| README.md, CHANGELOG.md | Command docs; `[Unreleased]` entry in the same commit |

**Tests** (table-driven, in the existing files): flag parsing and every
exclusivity rule; `resolveLocalRef` fetch-vs-no-fetch branching against a
scripted `gitOutput`; a `--set-code` run against the `gitRunSSH`/`gitPushCommand`
seams asserting the **order** bootstrap → clean → push → advance and that the
advance carries `ffGate=false`; `--dry-run` asserting **zero** mutating remote
calls; `resetBranchToBase` on a temp repo for both the collision-abort and the
`--discard` path; `ResetDeployedSHAs` truncate+seed.

## Skill: `odoo-probe`

Implementing this unit includes editing
`~/Documents/Projects/odoo-probe/SKILL.md` and `references/recipes.md`. The
skill currently predates `promote` entirely — `SPEC-promote-redesign.md` in that
repo is the companion change and should land in the same pass, since this
section assumes its "Promote — local funnel onto the deploy branch" section
exists.

**Required content:**

1. **Two commands, two permission classes.** The skill must not blur them:
   - `promote --reset` / `--set-base` — **local git only**, no SSH, no stage
     gate; same class as the rest of `promote` (per-operation permission,
     `--dry-run` first, full explicit flags because pickers cannot be driven
     headless).
   - `deploy --set-code` — **a remote, destructive, force move of a server's
     code**. Full write-mode gate: explicit user request, stage gate (never
     `prod` without an unambiguous yes), `--dry-run` shown first, and the
     literal command displayed before running it. It is *not* a deploy and
     must never be substituted for one.
2. **When to reach for them.** The trigger is a divergence readout, not a hunch:
   run `promote --show-branch`, read `ahead`/`behind`; a large `behind` after a
   merge to the base is the signal. The agent proposes; the user decides.
3. **Canonical headless forms**, with the ordering that matters:

   ```sh
   echo_cli promote --show-branch                                   # read the line's state
   echo_cli promote --reset origin/main --dry-run                   # preview the local reset
   echo_cli promote --reset origin/main                             # apply (keeps non-colliding dirty work)
   echo_cli deploy --set-code origin/main --from <target> --dry-run # preview the remote move
   echo_cli deploy --set-code origin/main --from <target>           # apply (cleans the overlay)
   ```

   Plus the one-gesture `deploy --set-code <ref> --from <t> --with-local`, and
   an explicit note that `--force` exists for headless confirmation and must be
   proposed to the user, never added silently.
4. **Reading outcomes.** `reset complete` / `code set` mean applied; a
   `promote conflict` on `--reset` means **nothing changed** — report the listed
   files and offer `--discard` as the user's call, never take it. Verify a
   remote move from the `code set` line's `sha`/`prev`/`cleaned` fields, or with
   `deploy --set-code … --json`; in `logview --json` it is the newest
   `"command": "deploy"` record whose `cmd` carries `--set-code`.
5. **What a re-baseline does not do**: it does not install or upgrade modules
   (propose an explicit `update` when the base changes module code), does not
   touch the DB, and does not create a checkpoint — while older checkpoints now
   restore to the pre-reset line.
6. **Command table + read-only contract**: add `promote --show-branch` (query,
   local), `promote --reset` (mutating, local — no SSH), `deploy --set-code`
   (mutating, remote, destructive) to the table, and add `deploy --set-code` to
   the list of commands a read-only session proposes but never runs.

**`references/recipes.md`** gains a recipe *"Re-base the deploy line after a
merge to main"*: `--show-branch` → confirm with the user → local `--dry-run` →
local apply → remote `--dry-run` → remote apply → verify with `logview --json` →
propose the `update` for whatever module code changed.

## Verify when done

- [ ] `deploy --set-code main` moves a git-deploy target to a commit it has
      never run, and `deploy --set-code origin/feat/x` works for a branch that
      exists only on `origin` (fetched) and for one that exists **only locally**
      (never pushed).
- [ ] The advance runs with `ffGate=false`: a target whose deploy branch has
      diverged is recoverable with one command, and the previously blocking
      message is no longer the end of the road.
- [ ] By default the remote checkout's module paths match the ref exactly (no
      leftover untracked files from the previous line); `--keep-overlay`
      preserves the non-colliding overlay; a root-level `odoo.conf` survives
      both.
- [ ] `--dry-run` (both commands) performs zero mutating calls — asserted in
      tests against the SSH/push seams, not only by eye.
- [ ] `promote --reset` with a colliding dirty file aborts with the file list
      and changes nothing; with `--discard` it completes; the non-colliding
      dirty work survives the default path.
- [ ] `--with-local` aborts the whole run on a local conflict **before** any
      remote call.
- [ ] `promote --show-branch` reports `base`/`ahead`/`behind`/`dirty`, and
      `base=none` when unconfigured, with unchanged exit codes.
- [ ] After a `--set-code`, `deploy --auto` does not skip commits (the deployed
      SHA set was re-baselined), and a subsequent `watch` cycle logs the
      re-baseline instead of failing.
- [ ] `SKILL.md` and `references/recipes.md` updated per § Skill; the skill
      states the two permission classes distinctly and never tells the agent to
      add `--force` on its own.
- [ ] `go build ./...` and `go test ./...` pass; README and `CHANGELOG.md`
      `[Unreleased]` updated in the implementing commit.
