# Unit 94: `push --set-dest` — configure the push destination, no transfer

## Goal

A config-only way to set the project's `[push] path` (Unit 91) without
naming a module or transferring anything: `push --set-dest` resolves the
remote target, opens the directory picker (or takes `--dest <path>`),
persists the choice to the local profile, and returns. Removes the
friction of having to pick a module just to configure where addons land
on an image-built remote.

## Design

**`push --set-dest`** short-circuits the whole push flow:

- Resolve the remote target (`--from <t>` / `--remote` / link) — the
  destination is remote, so a target is required (a clear error names
  what's missing when none resolves).
- Determine the destination:
  - **`--dest <path>` given** → validate/join/probe it
    (`prepareExplicitDest`), so `push --set-dest --dest build/addons`
    works headlessly (scripted config).
  - **no `--dest`** → open the Unit 91 remote directory picker
    (`pickRemoteDir`, TTY-only) starting at the compose root.
- Persist to the local `[push] path` (`config.SaveProject`), storing the
  path **relative** when it falls under `remotePath` (`underPath`) and
  absolute otherwise — the same normalization as the Unit 91 persistence
  offer. `--mkdir` composes: the flag is saved as `[push] mkdir = true`
  so the dir is created on the next real push.
- **No modules, no rsync, no prod gate** (it changes nothing on the
  server): the module positionals / `--dirty` / `--delete` / `--dry-run`
  are irrelevant in this mode. Close with
  `echo.push: push destination set path=<stored> source=set-dest`.

The rest of `push` is untouched: `--pick-dest` (pick + push + offer to
save) and `--dest`/`[push] path` (push to an explicit dir) keep their
Unit 91 behavior. `--set-dest` is the "just configure it" entry point.

**Interaction with the auto-detect fallback.** Unrelated — `--set-dest`
never pushes, so the auto-detect-failure→picker fallback doesn't apply.

## Implementation

### `internal/cmd/push.go`

- `pushArgs` gains `setDest bool`; `parsePushArgs` accepts `--set-dest`.
  `--set-dest` with `--dest` is allowed (saves that path); otherwise it
  opens the picker.
- `RunPush`: right after `parsePushArgs`, if `p.setDest`, delegate to
  `runSetDest(ctx, opts, p)` and return — before `requireRsync` (no
  transfer needs rsync) and before any module handling.

### `internal/cmd/push_dest.go`

- New `runSetDest(ctx, opts, p) error`:
  1. `resolveRemoteShell` (target required).
  2. destination = `prepareExplicitDest` (when `--dest` given) or
     `pickRemoteDir`.
  3. normalize via `underPath` → relative/absolute; save through a
     `*opts.Cfg` copy with `PushPath` (and `PushMkdir` when `--mkdir`)
     set, `config.SaveProject`; mirror onto `opts.Cfg`.
  4. log the `push destination set` line.

### Registration

- `commandFlags["push"]` += `--set-dest`; help row under `push`
  (`--set-dest` — "Set the remote push destination and exit (no push)").
- README: extend the "Explicit destination" paragraph with the
  `push --set-dest` one-liner (config-only, interactive or `--dest`).
- CHANGELOG `[Unreleased]` `### Added`.

## Dependencies

- Unit 91 (`pickRemoteDir`, `prepareExplicitDest`, `underPath`,
  `[push]` config) — landed.

## Verify when done

- [ ] `push --set-dest --from <t>` opens the remote picker, saves the
      chosen dir to `[push] path`, transfers nothing, and needs no
      module.
- [ ] `push --set-dest --dest build/addons --from <t>` saves the path
      headlessly (no picker, no TTY); a path under `remotePath` is
      stored relative, outside it absolute.
- [ ] `--mkdir` composes: the destination is created and
      `[push] mkdir = true` persisted.
- [ ] The next `push` / `deploy --push` uses the saved destination with
      no further prompting.
- [ ] `--set-dest` ignores module positionals / `--dirty` and never runs
      rsync or the prod confirm.
- [ ] Tests: `parsePushArgs` with `--set-dest` (+ `--dest` combo),
      `runSetDest` persistence via the config seam (relative/absolute
      normalization, mkdir flag), no-target error.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass;
      registry/help cross-check tests stay green.
