# Unit 113: the deploy branch — naming it, and knowing what is on it

## Goal

Two gaps left open by Units 102 and 112, both about the server side of a
git-deploy target:

1. **Changing the deploy branch is a config-file edit.** `git_branch` can only
   be set by hand-editing `global.toml`, and the next deploy silently leaves the
   previous branch behind as an orphan.
2. **The server cannot say where its code came from.** After a deploy the
   checkout sits on `<git_branch>` at some SHA. The ref that produced it —
   `origin/main`, `deploy/dev`, a tag — exists only in the local log and
   `--json`. Anyone who reaches the server over SSH has no way to ask.

## Why this shape and not "the server checks out the ref"

The obvious-looking alternative — make `deploy --set-code origin/deploy/dev`
create and check out `deploy/dev` on the server — was considered and rejected:

- A branch on the server carrying a **real** branch name creates an expectation
  Echo cannot honor. Echo never pulls; it ships objects over SSH and moves the
  pointer with `reset --keep`. If that branch also exists on `origin`, every
  deploy diverges it — which is exactly the failure this repo hit with a target
  whose `git_branch` was `staging`.
- `--set-code` also accepts tags and SHAs, where there is no branch to create.
  The command would need one behavior for branch-shaped refs and another for
  the rest.
- It would make a *content* command write persistent *configuration* as a side
  effect, breaking the split every other `--set-*` in Echo respects (config-only,
  no remote effect).

So the deploy branch stays an Echo-owned line with a name that does not collide
(`echo/deploy` by default), the ref name never travels, and the two real needs —
renaming the line, and reading its provenance — get their own explicit surfaces.

## Behavior

### A. `deploy --set-git-branch <name>` — name the line

```
deploy --set-git-branch <name> [--from <target>] [--rename] [--force]
```

Named after the config key it writes (`git_branch`), which also keeps it
distinct from `promote --set-branch` (the *local* accumulation branch — a
different concept on a different machine).

**Config-only by default**, like `--set-push` / `--set-checkpoint`: it resolves
the target locally (no SSH), persists the new branch, prints old → new, and
exits. The remote side happens on the next deploy, where `gitBootstrap` already
creates the branch at the checkout's current HEAD and switches to it — the
working tree and the dirty overlay are untouched because both branches are at
the same commit.

Where it writes:

1. `--from <name>` (or a project binding that resolves to a named target) →
   that `[targets.<name>]` entry, via `config.SaveConnectTarget`.
2. Otherwise the project's own `[connect]` → `SaveProject`.
3. Neither → `ErrUsage` naming the target to pass.

A target with `git_deploy = false` is `ErrUsage`: the branch would be inert.

**`--rename` does the remote half now.** Without it the old branch is left
behind pointing at the last deployed SHA — harmless but permanent litter, and
the reason this flag exists:

- `git -C <dir> branch -m <old> <new>` on the server (git renames the current
  branch in place; the working tree, the index and the overlay are untouched —
  no reset, no checkout).
- Guards: git mode on, `<old>` exists remotely, `<new>` does not. Prod gate
  (`confirmRemoteProd`) plus a confirm bypassed by `--force`.
- When `<old>` does not exist remotely (never deployed), it is not an error:
  the config is saved and the run says the branch will be created on the next
  deploy.

### B. Provenance — the server records what it is running

On every git-mode move, Echo writes into the server checkout's own git config:

| key | value |
|---|---|
| `echo.deployed-ref` | the ref this code came from (`origin/main`, `develop`, …) |
| `echo.deployed-sha` | the SHA the branch was moved to |
| `echo.deployed-at`  | RFC3339 timestamp of the move |

`git config` is the right home: it lives with the checkout, survives every
reset (it is not tracked content), needs no new file, and answers the question
with a command anyone already knows:

```sh
git config --get echo.deployed-ref
```

Written by all three movers, so it can never describe a state the server left:

- `gitDeployCommitted` (a normal commit deploy) — ref = the local branch the
  deploy ran from, or unset when detached.
- `applyGitSetCode` — ref = exactly what the user asked for.
- `gitRestoreCode` (`--restore-code`, rollback) — the code moved backwards to a
  hash, so there is no meaningful ref: `echo.deployed-ref` is **cleared** rather
  than left describing a line the checkout is no longer on.

Failures are warnings, never failures: provenance is metadata, and a deploy that
worked must not be reported as failed because a `git config` write did not land.

### C. Reading it back — `link --show`

When the linked target is git-mode, `link --show` adds one line after the
binding, read over the SSH round trip it already pays for:

```
echo.link: deploy code branch=echo/deploy sha=a1b2c3d ref=origin/deploy/dev at=2026-08-13T21:40:00Z
```

Unknown fields are omitted rather than shown empty — a checkout that predates
this unit reports `branch` and `sha` only. A non-git target prints nothing new.

## Implementation

| File | Change |
|---|---|
| [internal/cmd/deploy.go](../../internal/cmd/deploy.go) | Parse `--set-git-branch <name>` + `--rename`; exclusivity (standalone, like the other config-only setters); dispatch `runDeploySetGitBranch` before target resolution |
| [internal/cmd/deploy_git.go](../../internal/cmd/deploy_git.go) | `recordDeployedRef(ctx, rsc, absDir, ref, sha, log)` (three `git config` writes, warn-on-failure); `clearDeployedRef`; `readDeployedRef` for the `link --show` line; `gitRenameBranch` |
| [internal/cmd/deploy_setcode.go](../../internal/cmd/deploy_setcode.go) | Record provenance after the advance |
| [internal/cmd/deploy_setbranch.go](../../internal/cmd/deploy_setbranch.go) | New: `runDeploySetGitBranch` — resolve where to write, persist, optional remote rename |
| [internal/cmd/link.go](../../internal/cmd/link.go) | The `deploy code` line in `runLinkShow` |
| [internal/repl/repl.go](../../internal/repl/repl.go), [commands.go](../../internal/repl/commands.go) | Help rows, completions |
| README.md, CHANGELOG.md | Docs; `[Unreleased]` in the same commit |

**Tests**: flag parsing and exclusivity; the config write landing on the named
target vs the project binding; `--rename` asserted against the SSH seam
(renames, and never resets or checks out); provenance written after a set-code
and **cleared** after a restore; `link --show` rendering with and without the
git fields.

## Verify when done

- [ ] `deploy --set-git-branch echo/deploy --from develop` rewrites that
      target's `git_branch` and touches nothing else in `global.toml`.
- [ ] Without `--rename`, the next deploy creates the new branch at the
      checkout's HEAD and leaves the working tree and overlay intact.
- [ ] With `--rename`, the server's branch is renamed in place: same SHA, same
      dirty overlay, no checkout and no reset in the SSH transcript.
- [ ] After `deploy --set-code origin/deploy/dev`, `git config --get
      echo.deployed-ref` on the server returns `origin/deploy/dev`, and after a
      `--restore-code` the key is gone.
- [ ] `link --show` prints the `deploy code` line for a git target and nothing
      new for a non-git one.
- [ ] `go build ./...` and `go test ./...` pass; README + CHANGELOG updated.
