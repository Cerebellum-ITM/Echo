# Unit 69: Offer dirty (uncommitted) modules in the `deploy` picker

## Goal

`deploy` today only offers committed work: a multi-select picker over
recent commits, each resolved to a module. But often there are modules
with **uncommitted local changes** (working tree dirty) that the user also
wants to update on the server (the code itself is synced by another tool).
Add those dirty modules as **selectable entries in the same picker**, so a
deploy can include them alongside the picked commits. The final module set
is the **union** of commit-resolved modules and selected dirty modules.

## Design

Before the picker, detect the addon modules touched by the working tree
(`git status --porcelain`): map each changed/untracked path to its
top-level addon dir (same `isAddonDir` rule as the commit path resolver),
grouped and sorted, keeping each module's dirty paths (for i18n
detection).

The picker now lists dirty modules first (your current work), then the
recent commits. Dirty entries carry a distinct label
(`~ <module>  ·  uncommitted (N files)`) so they never collide with commit
labels (`<sha7>  <subject>`) and are obviously different on screen. The
selection is split back into picked commits and picked dirty modules by
label.

Processing:

- **Picked dirty modules** resolve directly to their module name
  (`via=dirty`), and their dirty paths feed the existing
  `pathsTouchI18n` check (so a dirty `i18n/` change still drives
  `--i18n-overwrite`).
- **Picked commits** resolve as before.
- The two sets merge (deduped) into `modules`; the install/update split,
  i18n decision, plan, prod gate and remote run are unchanged.

Because deploy's precondition is "the new code is already on the server"
and dirty changes are *not* committed (let alone pushed), selecting dirty
modules emits one `WARNING`: deploy updates them on the server but does
**not** push the code — the user's other tool is responsible for getting
it there. Honest, non-fatal.

Detection is best-effort: a `git status` failure (or no dirty modules)
just means the picker shows commits only, exactly as today. No new flag —
it's always on; nothing to select if the tree is clean.

## Implementation

### `internal/cmd/deploy.go`

- `type dirtyModule struct { name string; paths []string }`.
- `parsePorcelainPaths(out string) []string`: parse `git status
  --porcelain` lines (`XY <path>`, taking the post-`->` side of renames,
  trimming porcelain quoting).
- `dirtyModulesFromPaths(root string, paths []string) []dirtyModule`:
  group paths by top-level addon dir (`isAddonDir`), sorted; pure +
  testable.
- `gitDirtyModules(ctx, root) ([]dirtyModule, error)`: `gitOutput(... ,
  "status", "--porcelain")` → the two helpers above.
- Replace `pickDeployCommits` with `pickDeployItems(commits, dirty,
  deployedSet, palette) ([]deployCommit, []dirtyModule, error)`: builds
  both label sets (dirty first), maps the picked labels back, keeps the
  empty/cancel → `ErrCancelled` behavior.
- `RunDeploy`: detect dirty (best-effort), call `pickDeployItems`, log
  `items selected commits=.. dirty=..`, process picked dirty into
  `modules`/`i18nTouched` (`via=dirty`) before the commit loop, and emit
  the one uncommitted-changes `WARNING` when any dirty module is selected.

### `internal/repl/repl.go`

- Update the `deploy` help line to mention dirty modules.

### Tests (`internal/cmd/deploy_test.go`)

- `TestParsePorcelainPaths`: modified/untracked/renamed/quoted lines →
  expected paths.
- `TestDirtyModulesFromPaths` (reuses `addonsRepo`): groups by addon,
  drops non-addon paths, sorted, paths preserved per module.

## Verify when done

- [ ] With uncommitted changes under an addon, `deploy` shows that module
      as a `~ <module> · uncommitted` entry above the commits.
- [ ] Selecting it includes the module in the install/update plan
      (`via=dirty`), deduped against any commit that also touched it.
- [ ] A dirty `i18n/` change drives the i18n-overwrite decision.
- [ ] Selecting a dirty module logs the uncommitted-changes WARNING.
- [ ] A clean tree shows commits only (unchanged behavior).
- [ ] `go build/vet/test` pass.
