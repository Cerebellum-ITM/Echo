# Unit 125: per-module source — ship a module as it is at any ref

Depends on Unit 124 (the lock and the exact sync of archived content).

## Goal

`deploy` decides *what* to ship and *how* to ship it in one tangled step, and
the "what" is mostly the working tree:

- On an **rsync target**, a module resolved from selected commits ships the
  **working tree** of that module. Selecting commit A ships whatever is on disk:
  later commits, other branches' state, uncommitted edits.
- On a **git-deploy target**, committed content rides the deploy branch and
  only dirty modules go through rsync, again from disk.
- There is no way to say "this module, as it is at commit X". The 2026-09-30
  fix (`ccima_flow_mail` at `99f2109` onto staging, with `main` checked out
  locally at 1.33.1) needed exactly that, and every existing path either
  shipped the disk or moved the code of the whole target.

This unit separates the two decisions. Each module first resolves a
**source**, and the target's transport then ships it:

| Source | rsync target | git-deploy target |
|---|---|---|
| `worktree`: dirty module, or `push` | rsync from disk (unchanged) | overlay rsync (unchanged) |
| `commit`: resolved from selected commits | **rsync from `git archive` at the module's newest selected commit** (changed) | rides the branch (unchanged) |
| `ref`: new, `mod@ref` / `--at` | rsync from `git archive` at the ref | overlay rsync from `git archive`, branch untouched |

Nothing in it is specific to one project or one topology.

## Behavior

### A. Syntax

```
deploy --modules ccima_flow_mail@99f2109[,other_mod[@ref]…] [--at <ref>] [--fetch|--no-fetch] …
```

- `mod@ref` pins one module to a ref. `ref` is anything `git rev-parse`
  resolves locally: SHA, short SHA, branch, `origin/<branch>`, tag.
- `--at <ref>` applies to every `--modules` entry without its own `@ref`.
  `--at` needs `--modules`; with `--commits`, `--auto` or the picker it is
  `ErrUsage`.
- A module both in the commit selection and pinned with `@ref` is `ErrUsage`
  (two sources for one module).
- Refs resolve through `resolveLocalRef` (Unit 112): a `<remote>/<branch>` ref
  fetches its remote first, `--fetch` forces it for any ref, `--no-fetch`
  suppresses it, a fetch failure is a WARNING. Each distinct ref resolves once.
- A ref that does not resolve: `ErrUsage` naming it. A module that does not
  exist at the ref: `ErrUsage`, `module ccima_flow_mail does not exist at 99f2109`.

For the incident:

```sh
echo_cli deploy --modules ccima_flow_mail@99f2109 --from habitta_prod --dry-run
echo_cli deploy --modules ccima_flow_mail@99f2109 --from habitta_prod
```

### B. Locating a module at a ref

`--modules` validation today reads the disk (`resolveAddon`), and
`archiveModules` locates paths with `localAddonsSubpath` (also disk). A module
pinned to a ref is located **in the ref's tree**:

1. If the module exists on disk, try the same relative path at the ref
   (`git cat-file -e <sha>:<sub>/<mod>/__manifest__.py`).
2. Otherwise search the ref's tree for `<mod>/__manifest__.py` at depth one or
   two (`git ls-tree -r --name-only <sha>`, filtered). Exactly one match is
   used; several are `ErrUsage` listing them.

`archiveModules` takes the resolved paths instead of re-deriving them from disk.

### C. The working tree never leaks into a committed source

- A module whose source is `commit` or `ref` ships from `git archive`. Local
  uncommitted changes in that module are ignored, with one WARNING:
  `module has uncommitted changes — ignored, shipping 99f2109`.
- A dirty module still ships from disk only when it is selected **as dirty**
  (the picker's dirty entry, `--auto`, or `--modules` without `@`, as today).
- If a module is selected both as dirty and through commits, the dirty source
  wins, as today, and the plan says so.

### D. `commit` sources on rsync targets ship the commit, not the disk

For each module resolved from selected commits, the shipped tree is the module
at the **newest selected commit that touches it** (the one every other selected
commit for that module is an ancestor of; a non-linear set for one module is
`ErrUsage`, same rule as `resolveGitTip`). This is a behavior change: selecting
commits no longer ships later commits or uncommitted edits that happen to be
on disk. It goes in the CHANGELOG under Changed.

### E. Git-deploy targets: a ref ships as overlay

- `@ref` modules rsync from their archive into the checkout's module directory
  with `--delete` (Unit 124 rule). The deploy branch does not move for them.
- When the same run also advances the branch, the branch moves first and the
  overlay is applied after, so the pinned module ends as the ref.
- They are overlay in every existing sense: `push --clean` reverts them,
  `deploy --set-code` cleans them unless `--keep-overlay`.

### F. What replaces a pinned module

The rule is **the last ship wins**, and it has to be clean:

- **rsync target**: the next ship of that module (any source) replaces the
  directory; archived sources do it with `--delete`.
- **git-deploy target**: when a module whose lock entry is `source: ref` is
  deployed again through the branch, Echo first reverts that module's overlay
  (the `push --clean` internals, scoped to it) and then advances, so the result
  is exactly the branch content and not a mix. Logged as
  `INFO pin released module=… was=ref@99f2109`.
- `deploy --set-code` replaces every pin unless `--keep-overlay`.

### G. Plan, i18n, lint, history

- **Plan** (Unit 124 `code` line) shows the source and the manifest version at
  that source next to the version installed in the database:
  `code module=ccima_flow_mail ship=ref@99f2109 version=18.0.1.31.1 installed=18.0.1.31.0 locked=worktree@0b6fc41`.
  The installed version comes from `ir_module_module.latest_version`, read in
  the same query that already decides install vs update.
- **i18n detection** for a `ref` source: the `i18n/` subtree id at the ref is
  compared with the one at the lock entry's `sha`. Different → detected. No
  lock entry or no `sha` (worktree) → not detected, with an INFO line pointing
  to `--i18n`.
- **Pre-flight lint** runs over the tree that ships (the archive dir), not the
  disk. This also fixes `watch-deploy`, which today lints the disk while
  shipping an archive.
- **Local deploy history**: `ref` deploys mark no commits (they are not a
  deploy of the line). The lock records them.
- **`--json`**: `DeployModule.source` is `ref`, with `sha` and `version`.

### H. Unchanged

Stage gate and prod confirm (`--force`), automatic checkpoint per stage,
`--test`/`--no-test`, `--i18n`/`--no-i18n`, `--no-actions`, `--no-git`, and the
deploy actions run in the same order. A `ref` source with `--no-push` is
`ErrUsage`: an update without shipping would run `-u` on whatever is there,
which is not the ref.

## Implementation

| File | Change |
|---|---|
| `internal/cmd/deploy.go` | Parse `mod@ref` in `--modules` and `--at`; exclusivity rules; a `moduleSource` per resolved module (kind, sha, path); per-module tips for `commit` sources on rsync targets; archive dir built for every non-worktree source and passed as push source; plan lines; `ref` modules excluded from `MarkDeployed` |
| `internal/cmd/deploy_source.go` (new) | `parseModuleRefs`, `locateModuleAt(ctx, root, sha, mod)`, `moduleTipFor(commits, mod)`, `archiveVersion` (manifest version at a sha), i18n subtree comparison |
| `internal/cmd/watch.go` | Stops archiving on its own: `RunDeploy` now ships each commit-resolved module from its tree, so `PushSrcRoot` and `archiveModules` go away |
| `internal/cmd/deploy_lint.go` | Lint root is the shipped source dir |
| `internal/cmd/deploy_git.go`, `push_clean.go` | Scoped overlay revert before a branch advance for modules locked as `ref` |
| `internal/cmd/deploy.go` (`remoteModuleStates`) | Also return `latest_version` |
| `internal/repl/…` | Help text and completion for `--at`, the `mod@ref` form in the `--modules` help |
| README.md, CHANGELOG.md | Deploy section: sources, `mod@ref`, `--at`, the "last ship wins" rule, the incident command; `[Unreleased]` → Added (`--at`, `mod@ref`) + Changed (commit deploys on rsync targets ship the commit) |

**Tests** (transport seams only): `mod@ref` / `--at` parsing and every
exclusivity error; ref not found; module not at the ref; module located at a
different path than on disk; dirty local module ignored with the warning; the
shipped dir equals `git archive <sha> -- <path>` (temp repo fixture) and other
modules are not in the push set; the `-u` argv names only the pinned module;
newest-selected-commit per module, and the non-linear error; git target: branch
untouched and overlay applied, then a later branch deploy reverting the pin
first; `push --clean` removing the pin's lock entry; plan line; i18n detection
against the lock.

## Out of scope

- `push mod@ref` (the standalone push keeps shipping the disk).
- `deploy --patch <commit>`: applying one commit on top of what the target runs.
- Dependency check for partial deploys (future unit, see the progress tracker).

## Verify when done

- [ ] With `main` checked out locally, `deploy --modules ccima_flow_mail@<sha>
      --dry-run` prints `ship=ref@<sha>` with the version at that sha and the
      installed one, and changes nothing.
- [ ] A real run (seams) leaves the module's destination identical to
      `git archive <sha> -- <path>`, touches no other module, and runs
      `-u ccima_flow_mail`.
- [ ] Local uncommitted edits in the module do not reach the server.
- [ ] Selecting an older commit on an rsync target ships that commit's tree,
      not the disk.
- [ ] On a git target, a later branch deploy of the module replaces the pin
      cleanly; `push --clean` and `--set-code` also remove it.
- [ ] `go build ./...` and `go test ./...` pass; README, CHANGELOG and
      `echo_cli help` updated.
