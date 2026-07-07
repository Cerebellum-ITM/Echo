# Unit 83: `push` — sync local modules to the remote addons dir

## Goal

New `push [<mod>...] [--from <t>|--remote] [--dirty] [--dry-run]
[--delete] [--force]` command: rsync the selected local modules to the
remote host's addons directory over SSH, replacing the external CLI
currently used to copy files server-side. Closes the documented gap in
`deploy` ("assumes code already pulled server-side"): with `push`, the
local checkout is the source of truth and Echo owns the whole
local → server → deploy cycle. Plus a `deploy --push` flag that syncs
the resolved modules right before the stop/up/-u run.

## Design

**Transport: rsync over the existing SSH plumbing.** The target
resolution is the shared `resolveRemoteShell` recipe (`--from <name>` /
`--from=<name>` / bare `--remote` → link binding). The copy itself is
one `rsync` per module:

```
rsync -az --itemize-changes \
  --exclude __pycache__ --exclude '*.pyc' --exclude .git \
  -e 'ssh -o BatchMode=yes' \
  <localAddons>/<mod>/  <host>:<remoteAddons>/<mod>/
```

- `--delete` is **opt-in** (removes remote files that no longer exist
  locally — correct for renames/deletions, but destructive enough to
  require the explicit flag).
- `--dry-run` adds rsync's `-n`: the itemized listing shows exactly
  which files would change, nothing is transferred.
- The exclude set mirrors `skipViewPath` (build/VCS noise never ships).
- rsync's stdout streams live through the session's dim-line printing
  (the itemize lines are the progress); a per-module summary line
  closes each sync (`echo.push.module: synced module=<m> changes=<n>`).

**Destination resolution: existing location first, mirror second.**
For each module:

1. **Probe where it already lives on the remote host filesystem** —
   Unit 79's `remoteModuleBase` host pass (relative profile addons
   paths joined under `remotePath`, `ssh test -f …/__manifest__.py`).
   An existing module is updated in place, wherever it is.
2. **New module** (not on the remote yet): mirror the local layout —
   the module's local addons subpath (from `resolveModuleDir`, made
   root-relative) joined under `remotePath`, provided that directory
   exists remotely (`ssh test -d`); else fall back to the first
   relative addons path in the remote profile; else fail with a clear
   error naming the paths tried.

**Host-filesystem remotes only (v1).** A conf-mode remote whose addons
live only inside the image has nothing rsync can write to — `push`
fails closed with `remote addons are container-internal — push needs a
host checkout` (same detection as the probe: no host-FS addons path
matches). Syncing into a container (`docker cp` chain over SSH) is out
of scope for this unit.

**Module selection = the established picker chain.** Positionals name
modules (validated against the local repo via `resolveModuleDir` —
unknown module is a usage error before touching the remote, the Unit 78
pattern). No positionals → multi-select fuzzy picker over the local
checkout's modules (stage-colored by the remote profile, the Unit 77
pattern). `--dirty` selects the git-dirty modules automatically
(`gitDirtyModules`, Unit 69) — the headless companion for scripts and
Unit 84's watcher; combined with positionals it unions.

**Prod gate.** Pushing code onto a prod host is a mutation:
`confirmRemoteProd(palette, "push", rsc, args)` — red confirm on a
prod-stage profile, `--force` bypasses, non-TTY fails closed. `--dry-run`
skips the gate (it changes nothing).

**`deploy --push`.** New deploy flag: after the plan is resolved (and
after deploy's own prod confirm), sync the update+install modules with
the push core before the `stop` step, reusing the already-resolved
`rsc`-equivalent target. A push failure aborts the deploy before
anything restarts. `--dry-run` composes: the plan prints, the push
itemization prints, nothing executes.

## Implementation

### `internal/cmd/push.go` — new file

- `PushOpts{Cfg, Root, Args, Palette, StreamOut, Log}` (ModulesOpts
  shape).
- `parsePushArgs(args)` → `(modules []string, dirty, dryRun, del bool,
  from string, remote bool, err error)`; remote flags consumed via
  `remoteFlagsIn` + skip loop (Units 75/79 pattern); unknown flags
  error.
- `RunPush(ctx, opts) error`:
  1. parse; `--dirty` → merge `gitDirtyModules(ctx, root)` names;
     no modules → multi-select picker.
  2. validate each module locally (`resolveModuleDir`) — usage error
     (`ErrUsage`) on a miss.
  3. `resolveRemoteShell`; `confirmRemoteProd` unless `--dry-run`.
  4. per module: `pushDest(ctx, rv, opts, mod)` (the two-step
     destination resolution) → `rsyncModule(ctx, srcDir, sshHost,
     destDir, ruleset)` streaming itemize lines; count changes.
  5. close with a totals line
     (`echo.push: push complete modules=N files=M`).
- `rsyncModule` builds the argv (exec.CommandContext, `rsync` from
  PATH — missing binary is a clear error), pipes stdout line-wise to
  `opts.StreamOut`, folds stderr into the error.
- `pushModuleSet(ctx, rsc, opts, modules, dryRun, del)` — the loop of
  steps 4–5 extracted so `deploy --push` and Unit 84's watcher can call
  it with an already-resolved target and module list (no picker, no
  re-resolution).

### `internal/cmd/deploy.go` — `--push`

- `deployArgs` gains `push bool` (`--push`); after the plan/prod-confirm
  and before the `stop` step, call `pushModuleSet` with
  `update+install`; abort on error. Dry-run path prints the itemization
  after the plan.

### `internal/repl/push.go` — new file

- `runPush` wrapper: start frame, `RunPush`, finalize (`ErrUsage`→
  usage exit, cancel path, standard error frame) — the `deploy` wrapper
  shape.

### Registration

- `Registry` / `dispatchNames` / dispatch case / help (Docker section,
  right before `deploy`):
  `{"push [<mod>...]", "Rsync local modules to the remote addons dir"}` +
  rows for `--from <t>`/`--remote`, `--dirty`, `--dry-run`, `--delete`,
  `--force`.
- `commandFlags["push"] = {"--from", "--remote", "--dirty", "--dry-run",
  "--delete", "--force"}`; `commandFlags["deploy"]` += `--push`.
- `main.go` `projectlessOneShot`: `push` returns true unconditionally
  (always remote, needs only the local checkout — the `deploy` rule).
- `sequence`: `push` joins the remote-capable command set so a
  `push → deploy → i18n-pull` sequence composes with `--from`.

### Tests (`internal/cmd/push_test.go`)

- `parsePushArgs`: remote flags stripped from positionals; `--dirty`/
  `--dry-run`/`--delete` parsed; unknown flag errors.
- `rsyncModule` argv golden (seam the runner): excludes present, `-n`
  only under dry-run, `--delete` only when asked, trailing slashes on
  both src and dest.
- `pushDest` fallback order with a stubbed prober: existing-remote wins;
  new module mirrors the local subpath; profile-first-path fallback;
  all-misses error names the tried paths.

## Dependencies

- `rsync` on PATH locally and on the remote (runtime check, clear
  error) — no Go dependency.

## Verify when done

- [ ] `push my_module --from staging` rsyncs
      `<local>/<sub>/my_module/` onto the target's existing module dir,
      streaming itemized changes, excluding `__pycache__`/`.pyc`/`.git`.
- [ ] `push` with no modules opens the multi-select picker;
      `push --dirty` selects exactly the git-dirty modules.
- [ ] A module absent on the remote lands mirrored at the local subpath
      (when that dir exists remotely) or the profile's first relative
      addons path.
- [ ] `--dry-run` lists the would-be changes and transfers nothing (and
      skips the prod confirm); `--delete` is required for remote
      deletions to happen.
- [ ] A prod-stage target prompts the red confirm; `--force` bypasses;
      non-TTY fails closed.
- [ ] A conf-mode remote (no host-FS addons) fails closed with the
      container-internal hint.
- [ ] An unknown local module is a usage error (exit 2) before any SSH.
- [ ] `deploy --push --dry-run` prints plan + itemization; a push
      failure in `deploy --push` aborts before `stop`.
- [ ] `help`/registry/dispatch consistency tests stay green.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/cmd/...
      ./internal/repl/...` pass.
