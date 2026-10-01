# Unit 124: the deploy lock — the target records what code it runs

## Goal

Echo has no explicit record of which version of each module is on a target.
The answer is implicit and depends on the topology:

- **rsync targets** (`git_deploy = false`): whatever the working tree held the
  last time someone ran `deploy` or `push`. Nothing on the server or in the
  local history says which commit, or whether uncommitted edits went along.
- **git-deploy targets**: the deploy branch says what the *committed* line is
  (Unit 113 provenance), but the dirty overlay on top of it is anonymous.
- The local deploy history (`~/.config/echo/deploy-history/<project>.toml`)
  only lists commit SHAs per target. A module shipped from disk leaves no trace.

That gap is what made the 2026-09-30 incident hard to read and impossible to
fix with Echo: nobody could ask the target "which `ccima_flow_mail` are you
running, and where did it come from?".

This unit adds a **lock file on every target** that every code mover writes,
and makes the **sync of committed content exact**. It changes no command line
and no deploy behavior beyond those two points. It is the base for Unit 125
(per-module source) and Unit 126 (code rollback).

## Why this shape

- **On the server, not in the local config.** The lock describes the target,
  and anyone who reaches the target (another machine, another agent, a person
  over SSH) must be able to read it. The local deploy history stays as it is:
  it answers a different question ("which of my commits did I already ship
  here?") for the picker.
- **One file per target, under `remote_path`.** `remote_path` is the one
  directory every target owns, whatever its topology. A push destination can be
  shared between targets (habitta's `.cache/all_odoo` is), so the lock must not
  live there.
- **JSON.** It is machine-written and machine-read over SSH, and `--json`
  consumers get it verbatim.
- **No content hashes in this unit.** A per-module content hash would only pay
  off with a drift check (comparing the destination against the lock before a
  build), and that was explicitly left out. Committed content already has an
  identity for free: the git tree id of the module at its commit.
- **Metadata never fails a deploy.** Like the Unit 113 provenance, a lock read
  or write failure is a WARNING. A deploy that worked must not be reported as
  failed because a JSON file did not land.

## Behavior

### A. The lock file

Path: `<remote_path>/.echo/lock.json` (directory created on first write).

**The directory ignores itself.** `.echo/` is CLI state, not project content, so
it must never show up in a git repository that happens to contain it (the
project clone at `remote_path`, or a git-deploy checkout whose `git_path` is
empty). On every write Echo ensures `<remote_path>/.echo/.gitignore` exists
with a single `*` line, the same self-ignoring pattern tool caches use
(`.pytest_cache`, `.ruff_cache`):

- nothing tracked by the repo changes: no edit to its `.gitignore` and no
  `.git/info/exclude` entry, so it works even if the repo is created later or
  re-cloned;
- `git status` never lists the lock, so `push --clean` (which reads the
  porcelain status to find the overlay) never treats it as overlay, and
  `git clean` without `-x` never deletes it.

Creating the `.gitignore` is part of the lock write and follows the same
warn-on-failure rule.

The one case it cannot cover is a repository that already **tracks** files
under `.echo/` (git never ignores tracked paths). On write, when `remote_path`
is inside a git work tree, Echo runs `git ls-files -- .echo` there; any output
is a WARNING naming the fix (`git rm --cached -r .echo`). Echo does not run it:
that would modify the repository.

```json
{
  "schema": 1,
  "target": "habitta_prod",
  "base": {
    "branch": "echo/deploy",
    "sha": "0b6fc41…",
    "ref": "feature/rev-upload-capture",
    "at": "2026-09-30T09:20:00Z"
  },
  "modules": {
    "ccima_crm_reassign": {
      "source": "worktree",
      "sha": "0b6fc41…",
      "dirty": false,
      "version": "18.0.1.12.0",
      "dest": "/home/…/deployment/.cache/all_odoo/ccima_crm_reassign",
      "via": "deploy",
      "at": "2026-09-30T09:20:00Z",
      "by": "pascual.chavez@diverza.com",
      "verified": true
    }
  }
}
```

| Field | Meaning |
|---|---|
| `base` | git-deploy targets only: the deploy branch, its SHA, the ref it came from (same values as the Unit 113 `echo.deployed-*` keys). Absent on rsync targets. |
| `modules.<name>.source` | Where the content came from. This unit writes `worktree` (rsync from the local working tree), `commit` (rsync from a `git archive`, today only `watch-deploy`) and `branch` (the module rode the git deploy branch). Unit 125 adds `ref`. |
| `sha` | The commit the content belongs to. `worktree`: local `HEAD` at push time. `branch`: the branch tip. |
| `tree` | Git tree id of the module directory at `sha` (`git rev-parse <sha>:<path>`). Set for `commit`/`branch`; omitted for `worktree`. |
| `dirty` | `worktree` only: the module had uncommitted changes when it shipped. |
| `version` | The manifest `version` as shipped (read from the source that shipped, not from `main`). |
| `dest` | The absolute remote directory written. Omitted for `branch`. |
| `via` | `deploy`, `push` or `watch`. |
| `at` / `by` | RFC3339 time; local `git config user.email` (empty if unset). |
| `verified` | `false` when the code landed but Odoo has not run `-u` on it successfully yet; `true` after a green deploy verify. |

Entries are per module and only change for the modules a run touches. A module
that no mover has ever written has no entry: the lock describes what Echo
shipped, not the whole addons tree.

### B. Who writes it

Every command that moves code on a target updates the lock in the same run:

| Command | Lock change |
|---|---|
| `deploy` (push on) | After the push lands: each pushed module gets its entry with `verified: false`. After a green verify: flipped to `true`. A git branch advance rewrites `base` and sets `source: branch` for the modules resolved from the selected commits. |
| `watch-deploy` | Same as `deploy`, `via: watch`, `source: commit` (it already ships from `git archive`). |
| `push` | Each pushed module, `via: push`, `verified: false`. |
| `push --clean` | Removes the entries of the cleaned modules (they are back to the branch content). |
| `deploy --set-code` | Rewrites `base`; drops every module entry whose `source` is not `branch` unless `--keep-overlay`. |
| `deploy --restore-code` / rollback code restore | Rewrites `base` with an empty `ref` (Unit 113 semantics). |

Write procedure: read the current file over SSH (missing or unparsable →
start from an empty lock, with a WARNING for the unparsable case), apply the
change, write to `lock.json.tmp` and `mv` it into place. One read-modify-write
per lock update; the deploy does at most two (after push, after verify).

A dry-run never writes.

### C. Exact sync for committed content

Today `deploy` rsyncs without `--delete` (`deploy.go`, the `pushModuleSet` call
inside `runPush`). When the shipped source is a commit (`PushSrcRoot` set, i.e.
a `git archive` dir), a file deleted at that commit stays in the destination
and ends up in the running code or the built image.

Rule: **content from an archive is synced with `--delete`**, scoped to each
module directory (rsync already runs per module with trailing slashes, so the
deletion cannot reach a sibling). `--exclude` entries (`__pycache__`, `*.pyc`,
`.git`) stay protected on the receiver, as rsync does not delete excluded files
without `--delete-excluded`.

Content from the working tree keeps today's behavior (`push --delete` remains
the opt-in). The working tree is a scratch overlay, and deleting server files
based on whatever is on disk is a separate decision this unit does not take.

The dry-run itemization already lists deletions (`parseItemize`), so the plan
shows them with no extra work.

### D. Reading it

- **`deploy --lock [--from <target>] [--json]`**: read-only, prints the lock.
  Text mode: one line per module,
  `echo.deploy.lock: module name=ccima_flow_mail source=worktree sha=0b6fc41 version=18.0.1.31.0 at=… verified=true`,
  plus a `base` line on git targets. `--json` prints the file as is. Standalone
  like `--rollback` (no selection, no remote change).
- **`link --show`**: one summary line after the existing `deploy code` line:
  `echo.link: deploy lock modules=14 unverified=0 last=2026-09-30T09:20:00Z`.
  On git targets it also names the modules whose `source` is not `branch`
  (`overlay=ccima_flow_mail`). A target without a lock prints nothing.
- **`deploy --dry-run` plan**: for each module about to ship, one plan line
  with what will ship and what the lock says is there now:
  `echo.deploy.plan: code module=ccima_flow_mail ship=worktree@3c1d2e0 version=18.0.1.33.1 locked=worktree@0b6fc41 locked_version=18.0.1.31.0`.
  `locked=none` when there is no entry.
- **`--json` result**: `DeployModule` gains `source`, `sha`, `version`, so a
  `logview --json` record carries what shipped per module.

## Implementation

| File | Change |
|---|---|
| `internal/cmd/deploy_lock.go` (new) | `deployLock` / `lockModule` types; `readDeployLock(ctx, rsc)`, `writeDeployLock(ctx, rsc, lock)` (tmp + `mv`, warn on failure); `lockPath(remotePath)`; `lockEntryFor(...)` builders per source; `runDeployLockShow` for `deploy --lock` |
| `internal/cmd/deploy.go` | Parse `--lock`; exclusivity with any selection; build the per-module ship descriptors (source, sha, tree, version, dest) once the modules resolve; plan lines; lock update after push and after verify; `del` = `PushSrcRoot != ""` in `runPush`; `DeployModule` fields |
| `internal/cmd/push.go` | `pushModuleSet` returns the resolved dest per module (the lock needs it); `RunPush` writes the lock |
| `internal/cmd/push_clean.go` | Drop the cleaned modules' entries |
| `internal/cmd/deploy_setcode.go`, `deploy_git.go` | `base` rewrite next to `recordDeployedRef`; overlay entries dropped on `--set-code` without `--keep-overlay` |
| `internal/cmd/watch.go` | Nothing beyond what it inherits from `RunDeploy` (`via: watch` through a `DeployOpts` field) |
| `internal/cmd/link.go` | The `deploy lock` summary line |
| `internal/cmd/modinfo.go` | Reuse `manifestVersion` on bytes read from the shipped source |
| `internal/repl/…` | Help row and completion for `deploy --lock` |
| README.md, CHANGELOG.md | Deploy section: the lock, `--lock`, exact sync; `[Unreleased]` → Added + Changed (archive pushes now delete) |

**Tests** (transport seams only, no real server): lock round trip (missing,
empty, unparsable → warning + fresh lock); every write also ensures
`.echo/.gitignore` with `*`, idempotently; entry builders per source; deploy
writes `verified:false` after push and `true` after verify, and never on
dry-run; a failed run leaves `verified:false`; `push --clean` removes entries;
`--set-code` drops overlay entries unless `--keep-overlay`; `rsyncArgs` gets
`--delete` exactly when the source is an archive; `deploy --lock` text and JSON;
plan line with and without an existing entry.

## Out of scope

- Content hashes and drift detection (declined: no pre-build check).
- Shipping a module from a ref (Unit 125) and code rollback (Unit 126).
- Migrating the local deploy history into the lock.

## Verify when done

- [ ] A `deploy --push` to an rsync target writes `<remote_path>/.echo/lock.json`
      with one entry per pushed module, `source: worktree`, `verified: true`.
- [ ] `<remote_path>/.echo/.gitignore` contains `*`, and `git status` in a repo
      at `remote_path` does not list `.echo/`.
- [ ] A failed `-u` leaves those entries at `verified: false`.
- [ ] `deploy --dry-run` prints a `code` plan line per module and writes nothing.
- [ ] A `watch-deploy` cycle records `source: commit` and removes from the
      destination a file the new commit deleted.
- [ ] `push` without `--delete` still never deletes on the server.
- [ ] `deploy --lock --json` prints the file; `link --show` prints the summary.
- [ ] A lock write failure is a WARNING and the deploy still reports success.
- [ ] `go build ./...` and `go test ./...` pass; README + CHANGELOG updated.
