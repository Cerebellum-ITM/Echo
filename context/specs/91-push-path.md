# Unit 91: `push` — explicit destination path

## Goal

Let `push` (and everything that reuses its core: `deploy --push`, `watch`)
send modules to an **explicit remote directory** instead of the
auto-detected addons dir: a new `--dest <path>` flag plus a `[push]
path = "<dir>"` config resolved **server-first with local fallback**
(the Unit 90 checkpoint-policy pattern). This unblocks image-built
remotes where modules must land in a build context (e.g.
`build/addons/`) rather than a mounted addons directory — the layout
Unit 92 (deploy actions) builds on.

## Design

**One destination for the whole run.** When a destination is resolved
(flag or config), every module lands at `<dest>/<module>` — the
two-step probe of Unit 83 (`remoteModuleBase` in-place update →
addons-candidates fallback) is **bypassed entirely**. The explicit path
is authoritative: it expresses "this remote's modules live HERE", which
is exactly the case the probe cannot learn (a build dir is not an
addons dir Odoo knows about).

**Path semantics.** A relative `path` is joined under the target's
`remotePath` (`path.Join(remotePath, dest)` — same rule as the profile's
relative addons paths); an absolute path is used as-is. The docker root
(`"."`/empty after cleaning) is rejected with a usage error — a module
must never be written at the compose-project root (Unit 83 invariant).

**Resolution precedence** (mirrors Unit 90's checkpoint policy):

1. `--dest <path>` / `--dest=<path>` flag — per-invocation override.
2. `--pick-dest` flag — open the interactive remote directory picker
   (below) and use its selection.
3. Server-side `[push] path` — declared in the remote host's
   `global.toml` and/or `projects/<key>.toml` (project wins), decoded by
   `ParseRemoteProfile` into a new `RemoteProfile.PushPath` field. No
   defaults applied remotely, so absence falls through.
4. Local `[push] path` in the project's `echo.toml` (and `global.toml`,
   project wins — the standard config merge).
5. Nothing set → current behavior, unchanged: probe + addons-candidates
   (`pushDest` as it exists today) — **except** when that auto-detect
   fails (no addons dir found, or container-internal remote) in a TTY:
   instead of failing closed, fall into the picker so the user can point
   Echo at the right directory once. Headless keeps the fail-closed
   behavior byte-identical.

**Interactive remote directory picker (one-time setup, not per-run).**
A level-by-level browser over the remote host FS built on the existing
`fuzzyPicker` (stage-tinted like every remote picker):

- Each level lists the directories under the current path via one SSH
  call (`find <dir> -maxdepth 1 -mindepth 1 -type d`, sorted), plus two
  synthetic rows: `.. (up)` and `· use this directory`. `enter` on a dir
  descends, on `..` ascends (allowed **above** `remotePath` up to `/`,
  so a build context fully outside the compose tree is reachable), on
  `· use this directory` selects the current path. `esc` cancels (falls
  back to auto-detect / usage error per the entry point), `ctrl+x` quits
  Echo (`cmd.ErrQuit`).
- Start point: `remotePath` (the compose-project root).
- Selecting the compose root itself is rejected in-place with a WARNING
  (the Unit 83 invariant) — the picker stays open.
- After a selection, offer to persist it (`huh` confirm, the
  `db-neutralize` pattern): "Save as this project's push destination?"
  → writes local `[push] path` (absolute if picked outside
  `remotePath`, relative otherwise), so the next push is non-interactive.
  Declining uses it for this run only.
- The picker is TTY-only (`requireTTY` inside the picker core, as
  everywhere); `deploy --push` and `watch` never open it — they resolve
  from flag/config alone and keep today's failure modes.

**Existence check, not creation by default.** The resolved dest dir is
probed once per run (`remoteDirExists`); if missing, fail closed naming
the path — unless the new `--mkdir` flag is passed, which creates it
(`mkdir -p` over SSH) before syncing. Config may declare `mkdir = true`
in the same `[push]` section (same server-first merge) for
build-context dirs that are wiped between builds.

**Conf-mode remotes become pushable.** Unit 83 fails closed on
container-internal remotes ("push needs a host checkout"). With an
explicit dest that guard no longer applies: the user is asserting a
host-FS location exists (the build context). The container-internal
error remains only for the auto-detect path (no dest resolved).

**Log surface.** The per-module `syncing` frame already carries
`dest=<dir>`, so no new lines are needed; when an explicit dest is in
effect, the run opens with one INFO line
`echo.push: using explicit destination dest=<dir> source=<flag|server|local>`
so a misconfigured server profile is diagnosable at a glance
(Odoo-style, greppable — the Unit 54 spirit).

## Implementation

### `internal/config/config.go` — profile + local section

- `RemoteProfile` gains `PushPath string` and `PushMkdir *bool`
  (pointer to distinguish "unset" from explicit `false`, the
  `cmdLogsFile` pattern). `ParseRemoteProfile` decodes a `[push]`
  table (`path`, `mkdir`) from global + project bytes, project wins,
  **no defaults** (so the client can fall back).
- Local `Config` gains the same `[push]` section (`PushPath string`,
  `PushMkdir *bool`) with the standard global+project merge;
  `SaveGlobal` preserves a non-default section (the `[cmd_logs]`
  precedent).

### `internal/cmd/push.go` — resolution + dest override

- `pushArgs` gains `dest string`, `pickDest bool` and `mkdir bool`;
  `parsePushArgs` accepts `--dest <path>` / `--dest=<path>` (value token
  skipped like `--from`), `--pick-dest` and `--mkdir`. Empty `--dest`
  value is `ErrUsage`; `--dest` + `--pick-dest` together is `ErrUsage`.
- New `pickRemoteDir(ctx, rsc, opts, start string) (string, error)` —
  the level-by-level browser: one seam `listRemoteDirs` (SSH `find`,
  overridable in tests) + a pure `dirPickerEntries(cur string, dirs
  []string)` building the row set (`..`, dirs, `· use this directory`).
  Loops the single-select picker until a selection/cancel; rejects the
  compose root in-place. Persistence offer via the shared `huh` confirm;
  on accept, write local `[push] path` through the standard config save.
- New `resolvePushDest(p pushArgs, prof config.RemoteProfile,
  cfg *config.Config) (dest, source string, mkdir bool)` — pure helper
  applying the precedence (flag › server › local › ""), returning the
  winning source label for the log line and the merged mkdir policy
  (flag `--mkdir` › server `mkdir` › local `mkdir` › false). Unit-
  testable without SSH, exactly like `resolveCheckpointPolicy`.
- `RunPush`: after `resolveRemoteShell`, call `resolvePushDest`; when
  dest ≠ "", emit the `using explicit destination` line, validate/clean
  the path (reject `.`/root), probe `remoteDirExists` (or `mkdir -p`
  via `runSSH` when mkdir is on), and thread the resolved base into the
  module loop.
- `pushModuleSet` gains the explicit base: signature grows a
  `destBase string` param (empty = legacy auto-detect per module via
  `pushDest`). `deploy --push` and `watch` pass the resolved base too
  (they share the target's profile, so resolution happens once at their
  call sites via the same helper).
- `pushDest` untouched — it remains the auto-detect fallback.

### `internal/cmd/deploy.go` / `watch.go` — thread-through

- Where `pushModuleSet` is called, resolve the dest first with
  `resolvePushDest` (flags empty — deploy/watch expose no `--dest` of
  their own in this unit; config/server only) and pass the base.

### Registration

- `commandFlags["push"]` += `--dest`, `--pick-dest`, `--mkdir`; help
  rows for the three under the `push` entry.
- README: `push` section gains a "Explicit destination" paragraph +
  `[push]` config reference (both sides, server-first note).
- CHANGELOG `[Unreleased]` → `### Added`.

## Dependencies

- None new (rsync/SSH plumbing already present).

## Verify when done

- [ ] `push my_module --from staging --dest build/addons` lands the
      module at `<remotePath>/build/addons/my_module/`, skipping the
      probe, with the `using explicit destination … source=flag` line.
- [ ] A server profile declaring `[push] path` wins over the local
      `[push] path`; with neither, behavior is byte-identical to
      Unit 83 (auto-detect).
- [ ] Missing dest dir fails closed naming the path; `--mkdir` (or
      `mkdir = true` merged server-first) creates it and syncs.
- [ ] Absolute dest paths are used as-is; relative joined under
      `remotePath`; `--dest .` / empty is a usage error (exit 2).
- [ ] A conf-mode (container-internal) remote pushes fine with an
      explicit dest; without one, in a TTY it opens the directory
      picker instead of failing; headless it still fails closed with
      the host-checkout hint.
- [ ] `push --pick-dest` browses the remote FS level by level (up
      navigation past `remotePath` works, compose root rejected
      in-place), and accepting the persistence prompt writes
      `[push] path` so the next push needs no picker.
- [ ] `deploy --push` and `watch` honor the configured dest.
- [ ] Tests: `resolvePushDest` precedence matrix (flag/pick/server/
      local/none + mkdir tri-state), `parsePushArgs` with
      `--dest`/`--pick-dest`/`--mkdir` (+ the mutual-exclusion error),
      `dirPickerEntries` row set + compose-root rejection,
      `ParseRemoteProfile` decoding `[push]`, dest path
      validation/join rules.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass;
      registry/help cross-check tests stay green.
