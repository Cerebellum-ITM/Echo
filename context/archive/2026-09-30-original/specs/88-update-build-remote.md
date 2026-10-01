# Unit 88: `update --build` — remote/source-aware module picker

## Goal

`update --build` gets a dedicated builder (like `i18n-pull --build` /
`deploy --build`) that resolves **where** the update runs and **which
source** the module list comes from *before* the picker — so the picker
offers the right modules (a remote target's installed modules, say)
instead of always the local project addons. Fixes the current inversion:
today the picker lists local addons and `--remote`/`--installed` are
toggled after, unable to influence it.

## Design

The generic `RunBuild` flow is positional-first, flags-second, so
`--remote`/`--installed` can't govern the picker. `update` therefore
joins `i18n-pull`/`deploy` as a **bespoke builder** special-cased in
`RunBuild`, resolving its inputs up front.

**Flow (`runUpdateBuild`):**

1. **Where** — if any remote option exists (a named connect target, or
   this directory's `link`), a `huh.Select` "Where to update?": `local`
   (default, first), each named target, and — when `cfg.ConnectSSHHost`
   is set — "this directory's link (remote)". With **no** remote options
   the step is skipped (mode = local), so a local-only project keeps a
   one-extra-prompt flow. A sequence's pre-selected `opts.From` skips the
   picker (targets that target).
   - `local` → no bake.
   - named `<t>` → bake `--from=<t>` (reproducible).
   - link → bake `--remote`.
2. **Source** — always a `huh.Select` "Module source": `project addons`
   (default) or `installed in the database (--installed)`. This governs
   **only** the picker; it is never baked (explicit module names make
   `--installed` a runtime no-op, mirroring i18n-pull's reasoning about
   `--all`).
3. **Populate the picker** from the 2×2 matrix, tinted by the resolved
   stage:
   | where × source | provider | stage |
   |---|---|---|
   | local · addons | `resolveModules` | `cfg.Stage` |
   | local · installed | `installedModules` | `cfg.Stage` |
   | remote · addons | `listRemoteConfModules` | `prof.Stage` |
   | remote · installed | `listRemoteModules` | `prof.Stage` |
   Remote resolution reuses `resolveRemoteShell` (SSH round-trips
   surfaced through `info`/`warn` like the i18n-pull builder, so the wait
   isn't silent). Empty list → `ErrNoModulesAvailable`.
4. **Pick modules** — the shared multi-select (`runFuzzyPickerCore`,
   stage-tinted). Cancel / empty → `ErrCancelled`.
5. **Extra flags** — the shared flag step (extracted `gatherFlags`)
   offered over a **reduced** set `["--i18n", "--level"]` only. `--all`,
   `--last`, `--installed`, `--remote`, `--from` are excluded here: the
   first two are meaningless once modules are hand-picked, the last three
   are already decided in steps 1–2.
6. **Compose + decide** — `composeArgs(picked, [remoteFlag?] + extras)`
   → `decideAction` (Run / Copy / Cancel), exactly like the generic
   path. Result line examples:
   - `update base web --from=prod --i18n`
   - `update sale --remote --level=debug`
   - `update account` (local, addons source)

## Implementation

### `internal/cmd/build.go`

- In `RunBuild`, add `if opts.Command == "update" { return
  runUpdateBuild(ctx, opts) }` alongside the i18n-pull / deploy cases,
  and drop `update` from `buildPositionals` (its picker is now bespoke).
- Extract the Step-2/3 flag gathering into
  `gatherFlags(ctx, opts, flags []string, stage string) ([]chosenFlag,
  error)` and call it from both the generic path and `runUpdateBuild`.
  Behavior identical (help-order preserved, `buildFlagValues[cmd]` value
  prompts, skip-on-no-value warning).

### `internal/cmd/build_update.go` — new file

- `runUpdateBuild(ctx, opts) (BuildResult, error)`:
  target resolve → source select → picker → `gatherFlags(…, {"--i18n",
  "--level"})` → compose → decide.
- `resolveUpdateBuildTarget(opts) (mode, fromName string, rsc
  remoteShellContext, err error)` — builds the "where" select (or
  skips it when no remote option / uses `opts.From`), resolving the
  remote via `resolveRemoteShell` when needed.
- `updateBuildModules(ctx, opts, mode, src, rsc) (mods []string, stage
  string, err error)` — the 2×2 provider dispatch.
- A `BuildOpts`→log adapter (like `i18nPullBuildOpts`) so remote waits
  surface through `info`/`warn`.

### Tests (`internal/cmd/build_update_test.go`)

- `gatherFlags` (pure-ish, TTY-guarded pieces mocked or skipped): the
  reduced flag set composes `--i18n` as bool and `--level` with `=`.
- Provider dispatch table (`updateBuildModules`) with stubbed list
  helpers via existing seams: each (mode, src) calls the right provider
  and returns its stage. (Where a helper needs SSH, use the same seam
  the sibling tests use, or assert the selection logic on a fake.)
- The composed-line shape for each target mode (`--from=<t>` vs
  `--remote` vs none) given a fixed pick — asserting bake correctness
  without a live remote.
- `build_test.go` cross-checks stay green (update no longer in
  `buildPositionals`; still in `commandFlags`).

## Dependencies

None new — reuses Unit 79 (`resolveRemoteShell`), the remote module
listers (`listRemoteConfModules`/`listRemoteModules`), the local
`resolveModules`/`installedModules`, and the Unit 51 picker. Builds on
the `update --remote` of the prior work.

## Verify when done

- [ ] `update --build` in a project with a `link`/targets first asks
      Where (local / target / linked) and Source (addons / installed).
- [ ] Choosing a remote target + "installed" lists the **remote DB's**
      installed modules (e.g. `base`), not the local addons; the picker
      is tinted by the remote stage.
- [ ] The composed line bakes `--from=<t>` (named) or `--remote`
      (linked) and never bakes `--installed`; local picks bake neither.
- [ ] "Run" executes it through the normal `update` frame (remote or
      local as chosen); "Copy" yields a recipe-ready line.
- [ ] A project with no remotes skips the Where step (local only) and
      still offers the Source choice.
- [ ] Non-TTY `update --build` fails closed with `ErrNonInteractive`.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/cmd/...
      ./internal/repl/...` and the build/registry cross-checks pass.
