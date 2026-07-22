# Unit 104: `deploy --set-checkpoint` CLI setup (project-scoped)

## Goal

Let the operator turn the DB checkpoint policy on/off (and set its
method / retention) from the CLI, headlessly, without hand-editing
TOML — mirroring `deploy --set-push` (Unit 95) and the `--test-*`
management flags (Unit 100). The command persists the `[checkpoint]`
table to the **local project profile only**
(`~/.config/echo/projects/<key>.toml`); the global `global.toml` and the
server-side policy are explicitly out of scope. As a side effect it
plugs a latent data-loss bug: `SaveProject` today never re-emits an
existing `[checkpoint]` table, so any other `--set-*` write silently
drops a hand-written project-level policy.

## Design

**Scope: project only, never global.** `SaveProject` writes exactly one
file — `projects/<key>.toml` — so routing the setter through it keeps the
change scoped to this instance/project by construction. If the user wants
a machine-wide default they still hand-edit `global.toml`; that is a
deliberate non-goal here (stated so nobody "helpfully" widens it).

**The policy already exists (Unit 90).** `[checkpoint]` carries three
fields, resolved server-first over local, with built-in defaults:

```toml
[checkpoint]
mode   = "on"    # auto | on | off   (default: auto → staging/prod only)
method = "db"    # db | dump         (default: db)
keep   = 3       # int ≥ 1           (default: 2)
```

This unit does **not** touch resolution or the per-run
`--checkpoint`/`--no-checkpoint` overrides — only adds a persister.

**CLI setup — three composable, config-only flags:**

- `--set-checkpoint[=on|off|auto]` — set `mode`. Bare `--set-checkpoint`
  means `= on` (the always-on value, matching `--set-push` bare = true).
- `--set-checkpoint-method=db|dump` — set `method`.
- `--set-checkpoint-keep=N` — set `keep` (N ≥ 1).

Any subset may appear in one invocation (e.g.
`deploy --set-checkpoint=on --set-checkpoint-keep=3`). Presence of **any**
of them makes the run a config-only "checkpoint-manage" op: it persists
the requested fields to the local project `[checkpoint]`, reports the
resulting policy, and exits — no remote resolution, no SSH, no deploy.
Fields not named in the invocation keep their currently-resolved value
(so `--set-checkpoint=off` alone leaves method/keep untouched).

**Precedence is unchanged.** The setter only writes config. At deploy
time the existing chain still wins (highest first): `--no-checkpoint` /
`--checkpoint[=m]` per-run flags → server `[checkpoint]` → local
`[checkpoint]` → defaults (`auto`/`db`/2), via `resolveCheckpointPolicy`
+ `resolveCheckpointMode`.

**Interactions / guards.**

- The checkpoint-manage op short-circuits **before** remote resolution
  and before the `--rollback` / `--restore-code` branches (it is pure
  local config, like `--set-push`).
- It is mutually exclusive with the per-run overrides
  `--checkpoint` / `--no-checkpoint` (mixing "persist the policy" with
  "override just this run" is nonsensical) → `ErrUsage`.
- It is mutually exclusive with a deploy selection
  (`--commits`/`--modules`/etc.) the same way the other `--set-*` ops
  are — it persists and exits.
- Invalid values → `ErrUsage`: `mode ∉ {on,off,auto}`,
  `method ∉ {db,dump}`, `keep < 1`, or a non-numeric keep.

**Closing line** (Odoo-cohesive, mirrors `deploy push default set`):

```
echo.deploy: checkpoint policy set mode=on method=db keep=3
```

## Implementation

### `internal/config/config.go`

- **Source tracking.** `Config` gains `CheckpointSource string`
  (`"" | "global" | "project"`), set during `Load` exactly like
  `PromoteBranchSource`: `applyCheckpoint` sets `"global"` when the
  global `[checkpoint]` table is present; the project-override block sets
  `"project"` when `p.Checkpoint != nil`. Pure-default configs leave it
  `""`. The resolved `CheckpointMode/Method/Keep` fields keep their
  current behavior (defaults always populated).
- **`SaveProject` re-emits `[checkpoint]`.** Add, alongside the existing
  `[push]`/`[deploy]` guards:

  ```go
  if cfg.CheckpointSource == "project" {
      p.Checkpoint = &checkpointConfig{
          Mode:   cfg.CheckpointMode,
          Method: cfg.CheckpointMethod,
          Keep:   cfg.CheckpointKeep,
      }
  }
  ```

  This both (a) lets the new setter persist, and (b) fixes the latent
  bug where a `--set-push` (or any other `SaveProject`) dropped a
  project-declared `[checkpoint]`. Gate on `"project"` so a policy that
  only lives in `global.toml` is **not** copied down into the project
  file by an unrelated write.

### `internal/cmd/deploy.go`

- **`deployArgs`** gains a `setCheckpoint *checkpointManage` (nil unless
  a `--set-checkpoint*` flag was seen), where

  ```go
  type checkpointManage struct {
      mode         *string // on|off|auto
      method       *string // db|dump
      keep         *int
  }
  ```

  (pointer fields distinguish "named in this invocation" from "leave as
  resolved").
- **`parseDeployArgs`**:
  - `--set-checkpoint` → `mode = "on"`; `--set-checkpoint=<v>` → validate
    `v ∈ {on,off,auto}`.
  - `--set-checkpoint-method=<v>` → validate `v ∈ {db,dump}`.
  - `--set-checkpoint-keep=<n>` → parse int, require `n ≥ 1`.
  - Lazily allocate `out.setCheckpoint` on first such flag.
  - After the loop: if `setCheckpoint != nil` and
    (`checkpointSet || noCheckpoint`) → `ErrUsage` (mutually exclusive
    with per-run overrides).
- **`isCheckpointManage()`** helper on `deployArgs`
  (`return p.setCheckpoint != nil`), parallel to `isTestManage()`.
- **`runDeployCheckpointManage(opts, p)`** (parallel to
  `runDeployTestManage`): copy `*opts.Cfg`, overlay the named fields onto
  `CheckpointMode/Method/Keep`, set `CheckpointSource = "project"`,
  `config.SaveProject(&cfgCopy)`, mirror the values back onto `opts.Cfg`,
  log the closing line, return.
- **`RunDeploy`**: right after `parseDeployArgs`, alongside the existing
  `p.setPush != nil` / `p.isTestManage()` short-circuits, add
  `if p.isCheckpointManage() { return DeployResult{}, runDeployCheckpointManage(opts, p) }`.

### Registration / docs

- `commandFlags["deploy"]` += `--set-checkpoint`, `--set-checkpoint-method`,
  `--set-checkpoint-keep`.
- Help rows: `--set-checkpoint[=on|off|auto]` — "Set the checkpoint
  policy for this project and exit"; `--set-checkpoint-method=db|dump`
  and `--set-checkpoint-keep=N` — companion setters.
- README: extend the checkpoint prose with the `deploy --set-checkpoint`
  one-liner and note it writes the **local project** `[checkpoint]` only.
- CHANGELOG `[Unreleased]` `### Added`.

## Dependencies

- Unit 89 (checkpoint on deploy) and Unit 90 (`[checkpoint]` policy,
  server-first resolution) — landed. Pattern source: Unit 95
  (`--set-push`) and Unit 100 (`--test-*` management). No new packages.

## Verify when done

- [ ] `deploy --set-checkpoint` writes `[checkpoint] mode = "on"` to
      `projects/<key>.toml` headlessly (no SSH, no deploy) and exits;
      `=off`/`=auto` set the respective mode.
- [ ] `--set-checkpoint-method=dump` and `--set-checkpoint-keep=3`
      persist those fields; a combined
      `deploy --set-checkpoint=on --set-checkpoint-keep=3` writes both in
      one call; fields not named keep their resolved value.
- [ ] The write lands **only** in the project file — `global.toml` is
      never touched.
- [ ] A subsequent unrelated `deploy --set-push` (or any `SaveProject`)
      **preserves** the project `[checkpoint]` instead of dropping it
      (latent-bug regression test).
- [ ] A `[checkpoint]` that lives only in `global.toml`
      (`CheckpointSource == "global"`) is **not** copied into the project
      file by an unrelated `SaveProject`.
- [ ] `--set-checkpoint` together with `--checkpoint` or
      `--no-checkpoint` is a usage error (exit 2); invalid mode/method
      and `keep < 1` / non-numeric are usage errors.
- [ ] Deploy-time behavior is byte-identical to today when no
      `--set-checkpoint*` flag is present (resolution untouched).
- [ ] Tests: `parseDeployArgs` (each `--set-checkpoint*` form, bare = on,
      invalid values, the per-run-override mutual-exclusion),
      `runDeployCheckpointManage` persistence, `Load` setting
      `CheckpointSource` for global-only vs project vs none,
      `SaveProject` round-trip with checkpoint set and the preserve /
      don't-copy-global cases.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass;
      registry/help cross-check tests stay green.
