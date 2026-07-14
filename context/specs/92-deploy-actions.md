# Unit 92: deploy actions — declared local/remote steps hooked into deploy

## Goal

New declarative `[[deploy.actions]]` config: named commands that run
**locally or on the remote host** at fixed points of the `deploy`
lifecycle (`pre_push` / `post_push` / `pre_deploy` / `post_deploy`),
resolved **server-first with local fallback** (Unit 90 pattern),
**fail-fast** (a non-zero exit aborts the deploy). Motivating case: an
image-built remote whose addons are baked into the Docker image — push
lands the modules in a build context (Unit 91 `[push] path`), a
`post_push` remote action runs the server's build script
(image rebuild), and the normal stop/up/`-u` flow follows. `watch`
inherits the same actions on every cycle.

## Design

**Declaration.** A TOML array of tables, valid in the server's
`global.toml` / `projects/<key>.toml` (project wins) and in the local
`echo.toml`/`global.toml` (project wins):

```toml
[[deploy.actions]]
name  = "build-image"                  # required, unique per list
phase = "post_push"                    # pre_push | post_push | pre_deploy | post_deploy
where = "remote"                       # local | remote
run   = "./scripts/build.sh --tag latest"   # required, sh -c string
```

Unknown `phase`/`where` or a missing `name`/`run` is a config error
reported before anything executes (validated at resolution time, not
mid-deploy).

**Server-first resolution, list-wise.** `ParseRemoteProfile` decodes
the server-side `[[deploy.actions]]` into
`RemoteProfile.DeployActions`. Unlike the field-by-field checkpoint
merge, actions are an ordered list: **if the server declares any
actions, the server list wins wholesale; otherwise the local list
applies**. Mixing the two lists would make execution order
unpredictable across machines — the environment that needs the actions
owns them. Precedence: `--no-actions` flag › server list › local list ›
none.

**Fixed phases, defined anchor points** (all relative to the existing
deploy sequence: plan → prod confirm → push → checkpoint → stop →
up/`-u` → verify):

| Phase | Runs | Typical use |
|---|---|---|
| `pre_push` | Before `pushModuleSet` (only when a push happens: `deploy --push`, `watch`) | prep the build context (clean dir) |
| `post_push` | After a successful push, before checkpoint/stop | **rebuild the image from the pushed code** |
| `pre_deploy` | Always, right before the checkpoint/stop step | maintenance page on, drain jobs |
| `post_deploy` | After the `-u` run and verify succeed | maintenance off, notify, cleanup |

On a deploy without push (`deploy` sin `--push`), `pre_push`/`post_push`
actions are skipped with a dim INFO note (`skipped — no push in this
run`), not an error: the same server profile must serve both flows.

**Execution.**

- `where = "remote"` → `runSSHStream` on the target, `cd <remotePath> &&
  sh -c '<run>'`; stdout/stderr lines flow through `opts.StreamOut`, so
  the REPL recolors them like every other remote stream.
- `where = "local"` → `exec.CommandContext` via `sh -c` with the project
  root as cwd, same line streaming.
- Both get context env vars so scripts need no interpolation syntax:
  `ECHO_STAGE`, `ECHO_DB`, `ECHO_REMOTE_PATH` (remote only meaningfully),
  `ECHO_MODULES` (space-separated resolved update+install set, empty when
  none), `ECHO_PHASE`.
- Ctrl+C cancels the running action via ctx (the deploy's existing
  SIGINT plumbing).

**Fail-fast semantics.**

- A failing `pre_push`/`post_push`/`pre_deploy` action aborts the deploy
  **before the stop** — containers untouched, clear ERROR naming the
  action and its exit code.
- A failing `post_deploy` action marks the run failed (exit ≠ 0, ERROR
  frame) but does **not** trigger the Unit 89 rollback: the deploy
  itself already verified green and the code is live; rolling back a
  healthy deploy because a notification hook died would be worse than
  the failure. The summary line says `deploy succeeded, post_deploy
  action failed action=<name>`.
- In `watch`, an action failure fails that cycle (counted like a deploy
  failure); the watcher keeps polling.

**Log surface (Odoo-style, greppable).** Each action is bracketed by a
frame under `echo.deploy.action`:
`running action=<name> phase=<phase> where=<where>` → streamed output →
`action done action=<name> took=<dur>` (or ERROR `action failed …
exit=<n>`). The deploy plan (including `--dry-run`) lists the resolved
actions per phase with their source (`server`/`local`); `--dry-run`
executes none.

**Flags.** `deploy --no-actions` (and `watch --no-actions`) skips all
actions for the run — the escape hatch when a server-declared action is
broken. No per-action selection in v1.

## Implementation

### `internal/config/config.go` — types + parsing

- New `DeployAction{Name, Phase, Where, Run string}` +
  `ValidateDeployActions([]DeployAction) error` (unique names, phase and
  where enums, non-empty run).
- Local `Config` gains `DeployActions []DeployAction` from
  `[[deploy.actions]]` (standard global+project merge: project list
  replaces global list, consistent with wholesale semantics).
- `RemoteProfile` gains `DeployActions []DeployAction`;
  `ParseRemoteProfile` decodes the same table from the server bytes
  (project wins over global, no defaults).

### `internal/cmd/deploy_actions.go` — new file

- `resolveDeployActions(prof config.RemoteProfile, cfg *config.Config,
  noActions bool) ([]config.DeployAction, string, error)` — pure:
  applies wholesale precedence, validates, returns the source label.
- `actionsForPhase(actions, phase)` — filter preserving declared order.
- `runDeployActions(ctx, rsc, opts, actions, phase, envCtx) error` —
  the executor: per action emit the running frame, dispatch
  local/remote, stream lines, stop at the first failure returning a
  typed error carrying action name + exit code. `envCtx` is a small
  struct (stage, db, remotePath, modules) rendered into the env slice.
- Seams for tests: `actionRunLocal` / `actionRunRemote` package vars
  (the `ckptRunSSH` pattern) so ordering and fail-fast are unit-testable
  without SSH/exec.

### `internal/cmd/deploy.go` — hook points

- `deployArgs` gains `noActions bool` (`--no-actions`).
- After target resolution: `resolveDeployActions`; plan output gains an
  `actions` block (phase, name, where, source) when any resolve.
- Call sites: `pre_push` immediately before `pushModuleSet`;
  `post_push` right after it succeeds; `pre_deploy` before the
  checkpoint/stop step; `post_deploy` after verify passes. Push-less
  runs log the skip note for the two push phases.
- Failure routing per the design (abort pre-stop; post_deploy = failed
  run without rollback).

### `internal/cmd/watch.go`

- `--no-actions` parsed and threaded into each cycle's deploy opts;
  cycle summary counts action failures as deploy failures.

### Registration

- `commandFlags["deploy"]` += `--no-actions`; `commandFlags["watch"]`
  += `--no-actions`; help rows for both.
- README: new "Deploy actions" section (schema, phases table,
  server-first rule, env vars, image-built-remote walkthrough combining
  Unit 91 `[push] path` + a `post_push` build action).
- CHANGELOG `[Unreleased]` → `### Added`.

## Dependencies

- Unit 91 (`push` explicit destination) — the image-built flow needs
  push landing in the build context. The actions machinery itself has
  no hard dependency on it.
- No new packages (SSH/exec/stream plumbing already present).

## Verify when done

- [ ] A server profile with a `post_push` remote action: `deploy --push`
      pushes, runs the script on the host (streamed, recolored), then
      proceeds to stop/up/`-u`; the plan and `--dry-run` list the action
      with `source=server`.
- [ ] Server list wins wholesale over a local list; with no server
      actions the local list runs; `--no-actions` skips all with a note.
- [ ] A failing `pre_deploy` action aborts before `stop` (containers
      untouched, ERROR names action + exit code); a failing
      `post_deploy` action reports failure without rolling back.
- [ ] `deploy` without `--push` skips `pre_push`/`post_push` with the
      dim note; `watch` runs the actions every cycle and a failure
      fails only that cycle.
- [ ] Action env vars (`ECHO_STAGE`, `ECHO_DB`, `ECHO_MODULES`,
      `ECHO_PHASE`, `ECHO_REMOTE_PATH`) reach the script on both
      `where` values.
- [ ] Invalid config (bad phase/where, duplicate name, empty run) errors
      at resolution time, before any deploy step.
- [ ] Tests: `ValidateDeployActions`, `resolveDeployActions` precedence
      (server/local/none/`--no-actions`), `actionsForPhase` ordering,
      `runDeployActions` fail-fast + env rendering via seams,
      `ParseRemoteProfile` decoding `[[deploy.actions]]`.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass;
      registry/help cross-check tests stay green.
