# Unit 97: `promote` — worktree → single deploy branch funnel

## Goal

Add a **purely-local** `promote` command that moves work from the *current*
git worktree onto a **single accumulation branch** (the configured deploy
branch, `develop` used only as the example name below) —
so the developer can funnel changes from any feature worktree into the one
branch the instance is fed from, without hand-juggling `git` across
worktrees and without ever deploying from a divergent branch.

`promote` does **one thing: the local move**. It never talks to a remote
instance, never runs `git push`, never runs `deploy`, and never commits on
its own. Two source modes:

1. **Dirty** — take the *patch* (modified **+ untracked** files) of the
   current worktree, grouped/selected **by folder (module)**, and apply it
   into the `develop` worktree, **leaving it dirty** (no commit).
2. **Branch commits** — take selected commits from another local branch and
   `cherry-pick` them onto `develop`.

Once the change lands on `develop`, the existing flow (`deploy`/`watch` run
from the `develop` worktree) ships it — that stays a separate, manual step.

### Why this exists (the hole it closes)

- The instance is fed from **one branch** (`develop`). Echo's `deploy` keeps a
  per-target deployed-SHA history (`LoadDeployedSHAs`) and `watch` follows a
  single ref — so deploying from divergent branches corrupts the target's
  filesystem/history ("rompe la instancia"). The invariant is **one deploy
  source**.
- Work happens in **feature worktrees** (different absolute paths). Echo runs
  rooted at the checkout it's launched in and has **no worktree awareness**;
  git also forbids `git checkout develop` from a feature worktree while
  `develop` is checked out elsewhere. So landing changes on `develop` today is
  a manual cherry-pick/patch dance between worktrees.
- `push --dirty` / `deploy` *can* send dirty code, but they send it **straight
  to the instance, bypassing `develop`** → they violate the one-source
  invariant. They are not the tool for this flow.

`promote` is the missing **local funnel**: any worktree → `develop`, nothing
else.

## Design

`promote` is a **meta / git-local** command in the spirit of `link` — it is
**projectless-always** (no `docker-compose.yml` required) and rooted at the
**git worktree**, not the compose project. It emits Odoo-style log lines under
`echo.promote[.sub]` and, in the REPL, renders the change tree with the same
`BuildSyncTree` primitive `push` uses.

### Flag scheme (decided)

There is **no `--source` / `--from` flag** (`--from` is already the remote
target across Echo). The source is implied by the mode:

```
promote --dirty [<folder>...] [--to <branch>] [--create-dest <path>] [--dry-run]  # patch of the CURRENT worktree
promote <branch> [--commits <sha,sha>] [--to <branch>] [--create-dest <path>] [--dry-run]  # commits from another branch
promote                                                        # interactive: pick source → pick what → preview → apply
promote --set-branch <branch>                                  # config-only: persist [promote] branch, exit
```

- `--dirty` and a positional `<branch>` are **mutually exclusive** (two modes,
  each says where it reads from).
- `--to <branch>` overrides the destination; otherwise the saved `[promote]
  branch` is used. There is **no hardcoded default** — with neither, `promote`
  prompts for a destination (TTY) or fails closed (headless).
- `--set-branch <branch>` is **config-only** (mirrors `push --set-dest` /
  `deploy --set-push`): persist `[promote] branch` and exit — no move, no git
  mutation.
- `--create-dest <path>` creates the destination branch's worktree
  (`git worktree add [-b] <path> <branch>`) before applying — the headless
  escape hatch for "destination branch has no worktree yet".
- `--dry-run` previews (change tree + commit list) and writes nothing.
- `--force` — reserved for the conflict/edge guards below (not a prod gate;
  there is no remote here).

### Destination is dirty by design — no "clean destination" guard

`develop` **accumulates** several features' dirty changes; a dirty `develop`
worktree is the **normal, expected** state, not an error. `promote` therefore
does **not** block on "destination has uncommitted changes".

- **Dirty mode** moves by file copy (last-write-wins), so it never "conflicts":
  it overwrites the destination's version and **warns** when it overwrites a
  file that already had uncommitted work there.
- **Commit (cherry-pick) mode** is the only path with a real conflict: an
  overlapping cherry-pick aborts (`cherry-pick --abort`), leaves `develop`
  exactly as it was, and reports the failure (`ErrPromoteConflict`).

### Worktree awareness & destination resolution

`promote` learns the layout from `git worktree list --porcelain`:

- Resolve the **git root** of the current invocation via
  `git rev-parse --show-toplevel` (this is the *source* worktree).
- The destination is a **branch** (never a persisted path). Its worktree is
  discovered live from `git worktree list` each run — so moving the worktree
  on disk never breaks `promote`.

Destination resolution runs as a cascade; only the **worktree missing** and
**no branch configured** cases need interaction:

1. Target branch = `--to <branch>` › `[promote] branch` (config) › none.
2. Target branch known **and** it has a worktree → use it. (Normal case: `develop`
   is the main checkout or a `../develop` worktree — zero friction.)
3. Target branch known but **no worktree**:
   - **interactive**: prompt "no worktree for `<branch>`" → choose **create one**
     (ask for a path, run `git worktree add [-b] <path> <branch>`) / **use a
     different existing worktree** (picker of worktrees; the chosen worktree's
     branch becomes the destination and is offered to persist) / cancel.
   - **headless**: error, unless `--create-dest <path>` was given (then create
     and continue).
4. **No target branch configured at all**:
   - **interactive**: picker over existing worktrees (`path — branch`) plus a
     "create a new worktree…" row; the chosen branch is offered to persist as
     `[promote] branch` (a `huh` confirm) so the next run skips this.
   - **headless**: error asking for `--to <branch>` or `promote --set-branch`.

Why a real worktree (not a temp one): the dirty flow leaves uncommitted files
in the destination working tree so they **accumulate** across promotes; a
throwaway temp worktree would drop them and your `deploy` runs from the real
`develop` worktree anyway.

- Refuse when source worktree == destination worktree (nothing to funnel).

### Interactive flow (REPL / no-arg one-shot with TTY)

1. **Source** (`PickOne`): a fixed top row *"this worktree — dirty changes"*
   plus local branches by recency (`gitLocalBranches`), each annotated with the
   worktree it is checked out in (or `—` if none). Dirty is only offered from
   the current worktree.
2. **What to move** (multi-select):
   - dirty source → the current worktree's **dirty modules by folder**
     (`gitDirtyModules`), one row per module; selecting a module includes only
     its changed/added files (the patch), never the whole folder.
   - branch source → the commits in `<dest>..<branch>` not already on `<dest>`
     (deduped, see below), newest first.
3. **Preview**: render the change tree (`BuildSyncTree`) for dirty, or the
   commit list for cherry-pick; confirm.
4. **Apply** into the destination worktree; report a summary line.

Non-TTY without enough flags → `ErrNonInteractive` (invariant #9: fail closed,
no picker without a terminal).

## Implementation

### `internal/cmd/promote.go` (new)

**Args**

- `promoteArgs{ dirty bool; branch string; commits []string; to string; setBranch string; createDest string; dryRun bool; force bool }`.
- `parsePromoteArgs(args)` — first non-flag positional is the source branch;
  `--commits a,b` splits on commas; `--to`/`--set-branch`/`--create-dest` take a
  value; enforce `--dirty` XOR `branch`; `--commits` requires a branch;
  `--set-branch` is standalone (rejects mode/source flags); unknown `-`flags
  error with `ErrUsage`.

**Root & worktrees**

- `gitToplevel(ctx, cwd) (string, error)` — `git -C <cwd> rev-parse --show-toplevel`.
- `type gitWorktree struct{ path, branch, head string }`.
- `gitWorktrees(ctx, root) ([]gitWorktree, error)` — parse
  `git worktree list --porcelain` (`worktree`/`HEAD`/`branch` records;
  `detached` has no branch).
- `worktreeForBranch(wts, branch) (gitWorktree, bool)` — match
  `refs/heads/<branch>`.
- `addWorktree(ctx, root, path, branch) error` — `git worktree add <path>
  <branch>`, falling back to `-b <branch>` when the branch doesn't exist yet.

**Destination resolution** (`resolveDest`)

- Target branch = `--to` › `cfg.PromoteBranch` (`promoteDestBranch`). **No
  hardcoded default** — an empty result means "not configured" and triggers the
  picker (step 4 of the cascade) in a TTY, or a fail-closed `ErrUsage` headless.
- Find its worktree; if present → done. If missing:
  - `--create-dest <path>` set → `addWorktree`, re-list, use it.
  - interactive → `promptMissingDest` (create / pick-existing / cancel).
  - headless → `ErrUsage` with `--create-dest`/`--set-branch` guidance.
- No branch configured, interactive → `pickDestWorktree` over `gitWorktrees`
  (+ "create new…"); offer to persist the chosen branch via `SaveGlobal`/
  `SaveProject` (`config.PromoteBranch`). Headless → `ErrUsage`.
- Returns the resolved `gitWorktree` (dest) + its branch; refuses when it
  equals the source worktree.

**Config-only** (`runSetBranch`): when `p.setBranch != ""`, persist
`[promote] branch` (global by default, project when a project config exists)
and return — no git mutation, headless, short-circuit at the top of
`RunPromote` (the `deploy --set-push` pattern).

**Dirty mode** (`runPromoteDirty`)

- Modules: `gitDirtyModules(ctx, srcRoot)`; interactive multi-select by module,
  or filter to the `<folder>` positionals in headless.
- Collect the change set for the selected modules' paths (`dirtyChangeSet`):
  - **tracked**: `git -C src diff HEAD --name-status -- <paths>` → op
    new/changed/deleted.
  - **untracked**: `git -C src ls-files --others --exclude-standard -- <paths>`
    → op new.
- **Apply by file copy, not `git apply`** (`syncFiles`). The move is a plain
  file-level sync of the source's *current* content into the destination: new/
  changed → copy (overwrite), deleted → remove. It deliberately **does not go
  through git's index** — a `git apply` (even `--3way`) validates against the
  destination index and fails on exactly the promote cases (`does not exist in
  index` for a module the destination doesn't track yet; `does not match index`
  for a file the destination already has dirty from a prior promote). File copy
  works regardless of the destination's git state. The destination is left
  dirty (uncommitted), as intended.
- **Last-write-wins + clobber warning**: copying overwrites the destination's
  version wholesale. Before applying, `destDirtyPaths` (a `git status
  --porcelain` of the destination under the selected dirs) records which files
  already carry uncommitted work there; after applying, `promoteDirty` emits a
  WARNING listing any of those the copy overwrote — the accumulation trade-off
  made visible.
- **Atomicity**: `snapshotFiles` captures each touched destination path
  (content+mode, or absent) before the sync; a mid-sync copy/remove failure
  triggers `restoreFiles` (rollback) so the destination is left exactly as it
  was. (Dirty mode has no "patch conflict" — file copy always applies;
  `ErrPromoteConflict` remains for the cherry-pick path.)
- Build a `[]FileChange` (op = `new` for untracked, `changed`/`deleted` for
  tracked) for the caller's `OnSync`/preview.

**Branch mode** (`runPromoteCommits`)

- Dedup: `git -C src cherry <dest> <branch>` → keep only `+`-prefixed SHAs
  (not yet on `<dest>`); the picker/`--commits` selection intersects this set.
- Apply: `git -C dest cherry-pick <sha>...` (preserves authorship). On
  conflict: `git -C dest cherry-pick --abort`, report, return
  `ErrPromoteConflict`.

**`RunPromote(ctx, PromoteOpts)`**

- `PromoteOpts{ Cfg, Args, Palette, Log, StreamOut, OnSync }` (mirrors
  `PushOpts`; no `Root` — resolved from cwd via `gitToplevel`).
- Order: parse → (`--set-branch` short-circuit) → `gitToplevel` →
  `gitWorktrees` → `resolveDest` (cascade above) → mode dispatch (interactive
  source picker when neither `--dirty` nor branch given) → `--dry-run` returns
  after preview → apply → summary log (`promote complete`, fields: `source`,
  `dest`, `modules`/`commits`, `files`).
- On success, record one **cmd-log** entry via `config.SaveCmdLog` (root =
  source worktree): `Command: "promote"`, `Cmd` = the effective invocation,
  fields carrying source/dest/counts — same guards as `saveWatchDeployRecord`
  (skip when `CmdLogsDisabled`, best-effort, one `PruneCmdLogs` pass). Gives
  `logview` a trace of what was funneled.
- New sentinel error `ErrPromoteConflict` (wraps a clear message).

### `internal/repl/promote.go` (new)

- Wrapper mirroring `internal/repl/push.go`: builds `PromoteOpts` with the
  session logger + `StreamOut` + an `OnSync` that renders the change tree
  (`BuildSyncTree`) with themed colors; runs the source/what pickers via the
  shared `PickOne` / multi-select core (`runFuzzyPickerCore`) tinted by no
  stage (local); `finalize`/`handleQuit` as usual.

### `internal/config/config.go`

- New `[promote]` section (global **and** project; realistically stored in
  `global.toml` since feature worktrees have no project config): `promoteFile{
  Branch string \`toml:"branch"\` }`.
- `Config.PromoteBranch string`; read in `Load` from global then project
  (project overrides global, like `applyPush`). Persisted by `promote
  --set-branch` and by the interactive "remember this branch" confirm — global
  by default (feature worktrees have no project config), project when one
  exists. Serialize a `[promote]` block in `SaveGlobal`/`SaveProject` when
  `PromoteBranch != ""`.

### Registration

- `internal/repl/repl.go`: add `promote` to the `Registry`, `dispatchNames`,
  and a `case "promote"` dispatch (projectless meta command; `startLog`/
  `finalize` like `connect`).
- `internal/repl/commands.go`: `commandFlags["promote"] = {"--dirty",
  "--commits", "--to", "--set-branch", "--create-dest", "--dry-run", "--force"}`.
- `internal/repl/script.go`: `IsScriptCommand` includes `promote`.
- `main.go`: add `promote` to the `projectlessOneShot` unconditional group
  (alongside `link`) so `echo_cli promote …` runs with no compose project;
  ensure it uses cwd (git root resolved inside `RunPromote`).
- Help: a `promote` row under a "Scripting"/"Git" grouping with the two usage
  forms.
- README: a **"Promote (worktree funnel)"** section — the one-branch rule, the
  two modes, dirty-stays-dirty, no push/deploy, and the conflict behavior.
- CHANGELOG `[Unreleased]` `### Added`.

## Dependencies

- Reuses `gitDirtyModules`/`dirtyModule`/`gitOutput` (deploy.go),
  `gitLocalBranches`/`gitRevParse` (watch.go), `FileChange`/`BuildSyncTree`/
  `PickOne`/`runFuzzyPickerCore` (push.go/picker.go), `SaveCmdLog`/`PruneCmdLogs`
  (config). No new Go packages. Requires the `git` binary (already assumed).

## Verify when done

- [ ] `promote --dirty <mod>` applies only the changed+untracked files of
      `<mod>` from the current worktree into the `develop` worktree, leaving
      `develop` **dirty and uncommitted**; no commit is created anywhere.
- [ ] `promote <branch> --commits <sha>` cherry-picks the commit onto
      `develop`, skipping SHAs already present (`git cherry` dedup).
- [ ] A dirty `develop` worktree does **not** block a promote. Dirty mode
      overwrites (last-write-wins) and warns when it clobbers uncommitted
      destination work; only a cherry-pick conflict aborts cleanly (`develop`
      unchanged) reporting the failure.
- [ ] `promote --dirty` works when the module is **new to the destination**
      (untracked) or the destination file is **already dirty** — the cases that
      broke the earlier `git apply` mechanism.
- [ ] `promote` runs **projectless**: from a feature worktree with no
      `docker-compose.yml` it works, rooted at the git worktree; source ==
      destination worktree is refused.
- [ ] Destination cascade: configured branch with a worktree is used silently;
      a missing worktree offers create / pick-existing interactively and
      `--create-dest <path>` handles it headless; no configured branch opens the
      worktree picker (TTY) or errors (headless).
- [ ] `promote --set-branch <b>` persists `[promote] branch` and exits without
      any git mutation; a later `promote` uses it as the default destination.
- [ ] `promote` (no args, TTY) walks source → what → preview → apply;
      non-TTY without `--dirty`/branch fails with `ErrNonInteractive`.
- [ ] `--dry-run` prints the change tree / commit list and writes nothing.
- [ ] `promote` never runs `git push`, never contacts a remote instance, never
      deploys (no SSH in the path).
- [ ] A successful promote writes a local `promote` cmd-log record visible in
      `logview`.
- [ ] Tests (`internal/cmd/promote_test.go`): `parsePromoteArgs` (modes, XOR,
      `--commits` parsing, unknown flag); `git worktree list --porcelain`
      parsing; `worktreeForBranch`; dest-branch precedence (`--to` › config ›
      empty, no default); dirty change set → file copy (overwrite/new/delete);
      cherry dedup selection. Config round-trip of `[promote] branch` in
      `config_test.go`.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass;
      registry/help/commandFlags cross-check tests stay green.
