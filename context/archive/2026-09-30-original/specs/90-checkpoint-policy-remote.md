# Unit 90: checkpoint policy reads from the server profile (remote-first)

## Goal

The deploy checkpoint policy (`mode` / `method` / `keep`) becomes **part of
the server's Echo profile**, read over SSH like `stage`, `db_name` and the
containers already are — so the same target checkpoints the same way no matter
which laptop runs the deploy. The local `[checkpoint]` config stays valid as a
**fallback** when the server doesn't declare one, and the per-run flags
(`--checkpoint` / `--no-checkpoint` / `--checkpoint=<m>`) still override
everything. Fixes the Unit 89 split-brain where the gating *stage* came from
the server but the *policy* came from the client.

## Design

**Why this change.** Unit 89 resolved the checkpoint policy from `opts.Cfg`
(the local config) while the `stage` that gates `auto` came from the remote
profile (`fetchRemoteProfile`). That contradicts Echo's invariant that the
server profile is the source of truth for a target's mapping — and it means
deploying to one server from two machines could behave differently. The policy
protects the *server's* database, so it belongs next to the stage/db/containers
it acts on.

**Precedence (strongest first).** For each of `mode` / `method` / `keep`:

1. **Per-run flags** — `--checkpoint`, `--checkpoint=db|dump`,
   `--no-checkpoint` (mode + method only; there is no `keep` flag). Unchanged
   from Unit 89.
2. **Remote profile `[checkpoint]`** — the server's `global.toml` plus its
   `projects/<key>.toml` (project wins over global), read via
   `ParseRemoteProfile`.
3. **Local `[checkpoint]`** — the client's `global.toml` / project profile
   (the Unit 89 location), now a fallback.
4. **Stage default** — `auto` → on for staging/prod, off for dev.

Because the local config always carries defaults (`applyDefaults` fills
`auto`/`db`/`2`), "local" is never truly empty; the merge is therefore
"remote field if the server declared it, else the local value (user-set or
default)". A field the server omits transparently falls back to local, so a
partial server `[checkpoint]` (e.g. only `mode = "on"`) still inherits
`method`/`keep` from local/defaults.

**Configurable on both sides.** The exact same `[checkpoint]` table
(`mode`/`method`/`keep`) is honored in **both** the local and the remote
config — nothing new to author, just a second place it's read. Set it on the
server (in that host's `~/.config/echo/global.toml` or the project profile) to
make the policy travel with the target; set it locally to override a server
that doesn't declare one, or for a purely local workflow. The precedence above
resolves any overlap.

**Scope boundaries.** The checkpoint *objects* (copy DBs, dumps) already live
on the server; the *metadata index* (`~/.config/echo/checkpoints/…`) stays
local — that is Echo's own client-side bookkeeping (like `deploy-history`) and
does not move. Only the *policy* relocates to be server-first. `ec init` is
**not** extended to prompt for `[checkpoint]` in this unit — the server's
`global.toml`/profile is authored directly, as today (possible follow-up).

## Implementation

### `internal/config/config.go`

- `RemoteProfile` gains three fields: `CheckpointMode`, `CheckpointMethod`
  (strings), `CheckpointKeep` (int).
- `ParseRemoteProfile(globalTOML, projectTOML)` decodes the remote
  `[checkpoint]` from the already-parsed `globalFile` / `projectFile` (both
  already have the `*checkpointConfig` field from Unit 89): start from the
  global table, then let the project table override field-by-field (non-blank /
  non-zero wins), and copy the result onto the profile. **No defaults are
  applied here** — an absent server `[checkpoint]` must leave the fields
  empty/zero so the client merge can fall back to local.

### `internal/cmd/checkpoint_remote.go` (or deploy.go)

- New pure helper:
  ```go
  type checkpointPolicy struct { mode, method string; keep int }
  func resolveCheckpointPolicy(prof config.RemoteProfile, cfg *config.Config) checkpointPolicy
  ```
  Starts from the local `cfg` values (already defaulted), then overrides each
  field the remote profile declares (`prof.CheckpointMode != ""`, etc.).
  Unit-tested in isolation.

### `internal/cmd/deploy.go`

- `resolveCheckpointMode` changes its middle argument from `*config.Config` to
  the merged `checkpointPolicy` (it already only read `CheckpointMode` /
  `CheckpointMethod`): `resolveCheckpointMode(p deployArgs, pol
  checkpointPolicy, stage string) (enabled bool, method string)`. The flag
  precedence and stage default are unchanged; it just reads `pol.mode` /
  `pol.method` instead of `cfg.CheckpointMode` / `cfg.CheckpointMethod`.
- `RunDeploy`: build `pol := resolveCheckpointPolicy(prof, opts.Cfg)` right
  after the profile is fetched, pass it to `resolveCheckpointMode`, and use
  `pol.keep` for the retention prune (`pruneCheckpoints(..., pol.keep, ...)`)
  instead of `opts.Cfg.CheckpointKeep`.
- `runDeployRollback` is unaffected (it doesn't resolve a policy).

### `internal/cmd/checkpoint.go`

- `runCheckpointCreate`: resolve the same policy from `rsc.prof` + `opts.Cfg`;
  the manual `--method` still wins, else `policy.method` (not
  `opts.Cfg.CheckpointMethod`); retention uses `policy.keep`.

### Tests

- `resolveCheckpointPolicy`: remote overrides local per field; an empty remote
  field falls back to local; all-empty remote yields the local values.
- Update `TestResolveCheckpointMode` to pass a `checkpointPolicy` instead of a
  `*config.Config` (same cases).
- `ParseRemoteProfile`: a server `[checkpoint]` (global + project override) is
  decoded onto `RemoteProfile`; an absent section leaves the fields
  empty/zero (no defaults applied remotely).

### Docs

- README: in the checkpoint section, state that the policy is read
  **server-first** (from the target's profile) with the local config as
  fallback and flags on top; show the `[checkpoint]` table living in the
  server's `global.toml`.
- CHANGELOG `[Unreleased]` → `### Changed` (Spanish, house style), same commit.
- `context/specs/00-build-plan.md`: add the Unit 90 row.

## Verify when done

- [ ] With a server-side `[checkpoint] mode = "on"` on the `develop` (dev)
      target and **no** local `[checkpoint]`, `deploy --dry-run` shows
      `checkpoint enabled method=db` — i.e. the server policy activated it on a
      dev stage.
- [ ] Removing the server section and setting the **local** `[checkpoint]
      mode = "on"` produces the same result (fallback works).
- [ ] With the server declaring `mode = "on"` and the client `mode = "off"`,
      the server wins (checkpoint runs); `--no-checkpoint` on the command still
      overrides both.
- [ ] A partial server section (`mode = "on"` only) inherits `method`/`keep`
      from local/defaults.
- [ ] `keep` from the server profile drives retention (a third checkpoint
      prunes to the server's keep count).
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass;
      README + CHANGELOG updated in the same commit.
