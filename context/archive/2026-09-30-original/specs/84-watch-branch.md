# Unit 84: `watch` — auto push+deploy on new commits to a branch

## Goal

New `watch <branch> [--from <t>|--remote] [--interval <sec>] [--force]`
command: a foreground loop that polls the local repo's ref for
`<branch>` and, every time it advances, pushes the affected modules'
**committed content** to the remote (Unit 83 core) and runs a headless
`deploy --commits <shas>` (Unit 78). Built for the multi-worktree
workflow: refs are shared across worktrees, so a commit made on that
branch from *any* worktree triggers the cycle — no file watching, the
commit is the deploy unit (same semantics `deploy` already has).

## Design

**Poll the ref, not the filesystem.** Every `--interval` seconds
(default 10, min 2) the watcher reads
`git rev-parse refs/heads/<branch>`. Same SHA → sleep. Advanced SHA →
one cycle. This is worktree-proof (one shared ref store), cheap, and
needs no fsnotify dependency. The branch must exist at startup
(usage error otherwise); it does **not** need to be checked out
anywhere.

**A cycle = range → modules → push@SHA → deploy.**

1. Fast-forward check: `git merge-base --is-ancestor <old> <new>`.
   Not an ancestor (rebase/amend/reset — common across worktrees) →
   WARNING `branch rewritten — re-baselining, nothing deployed` and the
   baseline moves to `<new>` with no action. A false negative beats
   auto-redeploying half a branch.
2. List `<old>..<new>` commits (the `gitAheadCommits` parsing, pointed
   at an explicit range) and resolve each to its module with the
   existing `resolveCommitModule` (Unit 61's subject scheme +
   single-module-diff fallback). Unresolved commits are skipped and
   named in a WARNING, exactly like the deploy picker does.
3. **Push the committed content, not the working tree.** The watcher's
   own worktree may sit on a different branch, so syncing the cwd would
   ship the wrong files. Instead: `git archive <new> -- <module paths>`
   extracted into a scratch dir (`os.MkdirTemp`), and Unit 83's
   `pushModuleSet` runs with that dir as source (new `srcRoot` knob on
   the push core). The scratch dir is removed after the cycle.
4. Deploy headless: the Unit 78 non-interactive path with
   `--commits <shas>` against the same resolved target. Deploy's own
   history marks the SHAs on success, so a restarted watcher (or a
   manual deploy) never re-deploys them — dedup comes free.
5. Frame the cycle in Odoo-style logs:
   `echo.watch.cycle: commits=N modules=m1,m2 → push → deploy` and an
   outcome line (`cycle ok took=…` / `cycle failed err=…`).

**Failures don't kill the loop.** A failed push or deploy logs ERROR,
the baseline still advances to `<new>` (the commits stay undeployed in
history, so the *next* cycle's deploy — or a manual one — picks them
up via `--auto`/picker), and polling continues. Only unrecoverable
setup errors (target unresolvable, branch deleted) stop the watcher.

**Baseline at startup = current tip.** The watcher only reacts to
commits made *after* it starts; a startup INFO names the branch, tip
SHA, target and interval. Anything already pending is one manual
`deploy --auto` away — mixing "catch up the backlog" into the watcher
would duplicate that command.

**Guards.** A prod-stage target refuses to start without `--force`
(one-time red confirm at startup is not enough for an unattended
auto-deployer; the flag must be explicit). Non-prod stages run freely.
`Ctrl+C` (context cancel) closes cleanly with a summary frame
(`watch stopped cycles=N deployed=M commits`). The watcher is
non-interactive by nature — it runs fine headless (tmux/CI), so no TTY
guard; `--force` is simply required for prod either way.

## Implementation

### `internal/cmd/watch.go` — new file

- `WatchOpts{Cfg, Root, Args, Palette, StreamOut, Log}`.
- `parseWatchArgs(args)` → `(branch string, interval time.Duration,
  from string, remote, force bool, err error)`; branch is the single
  required positional; remote flags via `remoteFlagsIn`.
- `RunWatch(ctx, opts) error`:
  - validate branch (`git rev-parse --verify refs/heads/<b>`), resolve
    the target once (`resolveRemoteShell`), prod+`--force` check;
  - loop on a `time.Ticker`, honoring `ctx.Done()`;
  - per advance: `watchCycle(ctx, opts, rsc, old, new)` implementing
    steps 1–5; pure-ish pieces split for tests:
    `isFastForward(ctx, root, old, new)`, `rangeCommits(ctx, root,
    old, new) []deployCommit`, `archiveModules(ctx, root, sha, modules)
    (srcRoot string, cleanup func(), err error)`.
- Deploy is invoked through the exported `RunDeploy` with synthesized
  args (`--commits <csv> --from <name> --force`) — the Unit 78
  headless path; `--force` here is deploy's confirm bypass, gated by
  the watcher's own startup rule.

### `internal/cmd/push.go` — `srcRoot` knob

`pushModuleSet` gains a source-root parameter (defaults to the project
root for manual `push`); Unit 83 lands it, this unit passes the archive
scratch dir.

### `internal/repl/watch.go` — new file

`runWatch` wrapper: start frame, `RunWatch`, finalize; `ErrUsage` →
usage exit, context cancel → the summary is the normal close (not
`ErrCancelled` — stopping a watcher is its natural end).

### Registration

- `Registry` / `dispatchNames` / dispatch case / help (Docker section,
  after `deploy`):
  `{"watch <branch>", "Auto push+deploy when new commits land on a branch"}` +
  rows for `--from <t>`/`--remote`, `--interval <sec>`, `--force`.
- `commandFlags["watch"] = {"--from", "--remote", "--interval",
  "--force"}`.
- `main.go` `projectlessOneShot`: `watch` returns true (always remote,
  local repo only for git).
- **Not** offered inside `sequence` (a non-terminating step would hang
  the sequence) — excluded from the sequence command set like the
  interactive shells are.

### Tests (`internal/cmd/watch_test.go`)

- `parseWatchArgs`: positional branch required; interval parse +
  minimum clamp; remote flags stripped.
- `isFastForward` / `rangeCommits` against a scratch git repo (the
  `gitAheadCommits` test pattern): linear advance yields the range,
  amend/rebase yields not-ancestor.
- `archiveModules`: extracted tree contains exactly the named modules'
  files at the SHA (commit a change, archive the previous SHA, assert
  the old content).
- Cycle wiring with a stubbed pusher/deployer: failure advances the
  baseline and keeps looping; rewritten branch re-baselines without
  calling either.

## Dependencies

None new — `git` CLI (already assumed by deploy) and Unit 83's push
core. Requires Units 78 (`deploy --commits` headless) and 83.

## Verify when done

- [ ] `watch dev --from staging` starts, names branch/tip/target/
      interval, and sits idle while the ref is unchanged.
- [ ] A commit to `dev` made **from another worktree** triggers one
      cycle: modules resolved from the commit subjects, pushed at the
      commit's content (not the watcher worktree's files), then
      deployed headless; deploy history marks the SHAs.
- [ ] Two quick commits before the next poll produce **one** cycle
      covering both.
- [ ] An amend/rebase of the branch logs `branch rewritten` and deploys
      nothing; the next normal commit cycles cleanly.
- [ ] A failing deploy logs ERROR and the watcher keeps polling; the
      missed commits appear in the next `deploy --auto`.
- [ ] A prod-stage target refuses to start without `--force`.
- [ ] `Ctrl+C` closes with the `cycles=N` summary frame; exit 0.
- [ ] Commits whose module can't be resolved are skipped with the same
      WARNING wording as the deploy picker.
- [ ] `help`/registry/dispatch consistency tests stay green; `watch` is
      not offered in `sequence`.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/cmd/...
      ./internal/repl/...` pass.
