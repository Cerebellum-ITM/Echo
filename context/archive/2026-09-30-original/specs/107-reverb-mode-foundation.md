# Unit 107: Reverb mode — foundation (`-E`, resolve, push to overlay)

## Goal

Let Echo target a **Reverb**-managed environment by name, with zero
per-environment local config: `-E <project>/<env>` resolves the target
over HTTP at call time instead of reading it from `global.toml`. The
diagnostic surface (`shell`, `shell-run`, `bash`, `psql`, `logs`, `view`,
`compare`, `ps`, `modules`, `update`, `test`, `db-pull`) comes for free,
and `push` gains the one behavioral change that matters: in Reverb mode it
rsyncs to the **overlay**, never to the addons directory Reverb replaces
wholesale on every deploy.

Source of truth: `dev_tools/reverb/docs/echo-reverb-mode.md` (client-side
contract) and Reverb's frozen `internal/api/resolve.go` payload.

This unit is the **foundation only**. Deploy delegation (§5),
checkpoint→snapshots (§6), docker lifecycle via the API (§7) and `modpath`
are deferred to Units 108–111; the commands they cover **refuse** a Reverb
target here with an explicit error rather than silently running a path
that would fight Reverb.

## Design

### Why this is a thin change

Every remote command funnels through **one** function,
`resolveRemoteShell()` (`internal/cmd/shell_remote.go`) — 22 call sites.
It does exactly the three things Reverb's resolve replaces in a single
HTTP call:

1. `resolveRemoteTarget` → `ssh_host` + `remote_path` (from TOML)
2. `fetchRemoteProfile` → SSH `cat ~/.config/echo/{global,projects/<key>}.toml`
   → containers, db name, stage, odoo version
3. `remotePullEnv` → SSH `cat <path>/.env` → PG user/password/port

Reverb mode is a branch at the top of that function. Nothing downstream
changes: it still returns a `remoteShellContext`, so the whole remote
surface keeps working unchanged.

### Target references: `env:<project>/<env>`

Echo's target reference today is a **connect-target name** (`--from
muutrade`). This unit makes it a small namespace:

| Reference | Resolves to |
| --- | --- |
| `<name>` | a `[connect_targets.<name>]` in `global.toml` (unchanged) |
| `env:<project>/<env>` | a Reverb environment, resolved over HTTP |
| `env:<env>` | same, with the project inferred from `GET /api/v1/envs` |

`-E <spec>` is **sugar for `--from env:<spec>`** — it is not a separate
channel. That is what keeps the change small: `remoteFlagsIn()` is the
single flag parser behind 10 of the 12 command parsers, so teaching it
`-E` gives every one of them Reverb mode with no signature churn and no
new field threaded through 22 call sites.

`env:` is a **reserved prefix** for target references; a connect target
may not be named with it (documented, not enforced at load — a name that
collides simply resolves as Reverb and errors clearly if unconfigured).

### Config: point Echo at a Reverb

`global.toml` only (machine-wide credentials, never a project profile):

```toml
[reverb]
url         = "https://reverb.example.com"
token       = "rvb_…"          # scope: echo
compose_cmd = "docker compose" # optional; default "docker compose"
```

`compose_cmd` exists because the classic path reads the compose binary
from the **server's** Echo profile, which a Reverb host does not have.
The token is a **secret** (it grants DB passwords via resolve): it is
never logged, never echoed in an error, and never written to a project
profile.

### Resolution per invocation

1. If the spec has no `/`: `GET /api/v1/envs` (cheap, no secrets), match
   by env name. No match → error listing nothing; several matches across
   projects → error naming the candidates and asking for
   `<project>/<env>`.
2. `GET /api/v1/resolve/{project}/{env}` for the full payload.
3. Build an **in-memory** target. Never written to `global.toml` — that
   is the whole point.

Field mapping (Reverb's payload is frozen, so these names are stable):

| `remoteShellContext` / target field | Resolve field |
| --- | --- |
| `sshHost` | `ssh_host` |
| `remotePath` | `paths.compose_dir` |
| `target.dbName`, `conn.DB` | `db.name` |
| `conn.User` / `conn.Password` | `db.user` / `db.password` |
| `target.odooContainer` | `containers.odoo` |
| `target.dbContainer`, `conn.Host` | `containers.db` |
| `target.stage` (prod guard) | `stage` |
| `target.odooVersion` | `odoo_version` |
| `target.composeCmd` | `[reverb] compose_cmd` (default `docker compose`) |
| `fromName` | `env:<project>/<env>` (history/label key) |

`prof` (`config.RemoteProfile`) is filled from the same values so the
call sites that read `rsc.prof.DBName` / `.Stage` / `.ComposeCmd` behave
identically. `prof.AddonsPaths` stays empty — the existing fallbacks in
`remoteModuleBase` handle that.

### Errors, spelled out

- **`409 not_ready`** — the environment is still provisioning. Retry, do
  not fail: 3 attempts, 2s apart, with an INFO line each wait. Still not
  ready after that → a clear error saying so.
- **`401` / `403`** — configuration problems (missing / wrong-scope
  token). Say exactly that, name the `echo` scope, never print the token.
- **`404`** — unknown project/env, named.
- **missing `ssh_host`** — the daemon has no `public_host`
  (`REVERB_PUBLIC_HOST`). A clear configuration error naming that
  setting; **no fallback** to any other host, per the contract doc.
- **`[reverb]` absent** — using `-E` without it is a usage error naming
  the section and the `url`/`token` keys.

### `push` targets the overlay

The behavioral change. Reverb owns `paths.addons` and replaces it
wholesale on every deploy; `paths.overlay` is the directory it never
touches, and a module there shadows the git copy.

- Default destination in Reverb mode is **`paths.overlay`** (not the
  per-module auto-detect, not `[push] path`).
- An explicit `--dest` / `[push] path` that resolves **under
  `paths.addons`** is **refused** with an explanatory error: the code
  would be destroyed by the next deploy.
- An explicit destination elsewhere is honored (escape hatch).
- After resolving, each pushed module that also exists under
  `paths.addons/<module>` emits a **WARNING**: the running code is the
  overlay's copy, shadowing the deployed one.
- `push --clean` in Reverb mode **empties the overlay** (scoped to the
  named modules, or all of it with `--all`) instead of requiring a
  git-deploy target — the overlay is not a git checkout, so the existing
  `git checkout -- / git clean -fd` implementation does not apply. Same
  dry-run preview and destructive confirm.

### Guards kept

- The prod-stage refusal keeps working untouched: `confirmRemoteProd`
  keys off the resolved `stage`, which now comes from resolve. Reverb
  only manages `dev`/`staging`, so this should never fire — it stays as
  the backstop.
- The token never reaches a log line or an error message.

### Deferred commands refuse, loudly

`deploy`, `watch`, `checkpoint`, `up`, `stop`, `restart` return a usage
error naming the unit that will land them, e.g.:

```
deploy does not support Reverb targets yet — Reverb runs the deploy
itself (POST /environments/{id}/deploy); Echo's own deploy would be
overwritten by it. Use `push -E …` for uncommitted code meanwhile.
```

This is deliberate: running Echo's git/rsync deploy against a Reverb env
would write into a directory Reverb replaces, and Echo's checkpoint store
would shadow Reverb's snapshots.

## Implementation

### `internal/reverb/` (new package)

- `Client{BaseURL, Token, HTTP *http.Client}` + `New(url, token)`.
- `type Env` — the resolve payload, decoded with the frozen field names
  (`Paths{Addons,Overlay,ComposeDir}`, `DB{Name,User,Password *string}`,
  `Containers{Odoo,DB}`, `Stage`, `OdooVersion`, `Git{Branch,DeployedRev}`,
  `State`, `URL`).
- `type EnvRef` — one `GET /envs` row (`Project`, `Env`, `Stage`, `State`,
  `URL`, `Branch`).
- `Resolve(ctx, project, env) (Env, error)` — Bearer auth, decodes
  Reverb's `{"error":{"code","message"}}` envelope into typed sentinels
  `ErrNotReady` / `ErrUnauthorized` / `ErrForbidden` / `ErrNotFound`.
- `ListEnvs(ctx) ([]EnvRef, error)`.
- `ResolveWithRetry(ctx, project, env, attempts, delay)` — the 409 loop,
  with an `onWait(attempt)` callback so the caller logs it.
- No token in any returned error string.

### `internal/config/config.go`

- `reverbConfig{URL, Token, ComposeCmd}` → `[reverb]` in `globalFile`.
- `Config.ReverbURL / ReverbToken / ReverbComposeCmd`, populated in
  `Load` from the global file only. Not emitted by `SaveProject`.

### `internal/cmd/reverb.go` (new)

- `reverbRefPrefix = "env:"`, `reverbRefIn(from) (spec string, ok bool)`,
  `parseReverbSpec(spec) (project, env string, err error)`.
- `reverbClient(cfg)` — builds the client or the "`[reverb]` not
  configured" usage error.
- `resolveReverbShell(ctx, cfg, root, spec, log) (remoteShellContext, error)`
  — the resolution above, returning a context whose new `reverb *reverbEnv`
  field carries `project`, `env`, `paths`.
- `requireNoReverb(cmdName, from) error` — the deferred-command guard.

### `internal/cmd/shell_remote.go`

- `remoteShellContext` gains `reverb *reverbEnv` (nil for classic
  targets — every existing path keeps its exact behavior).
- `remoteFlagsIn` recognizes `-E <spec>` / `-E=<spec>` / `--env <spec>` /
  `--env=<spec>` and returns it as `from = "env:" + spec`.
- `resolveRemoteShell` branches to `resolveReverbShell` when
  `reverbRefIn(from)` matches, before `resolveRemoteTarget`.

### `internal/cmd/push_dest.go` / `push.go` / `push_clean.go`

- `resolvePushDestination`: Reverb branch — refuse an explicit dest under
  `paths.addons`, else honor it, else default to `paths.overlay`.
- `warnOverlayShadow(ctx, rsc, opts, modules)` called before syncing.
- `runPushClean`: Reverb branch emptying the overlay
  (`rm -rf <overlay>/<mod>` per module, or the overlay's contents with
  `--all`), reusing the dry-run/confirm frame.

### Deferred guards

`RunDeploy`, `RunWatch`, `RunCheckpoint` and the remote `up`/`stop`/
`restart` paths call `requireNoReverb(...)` right after parsing.

### Registration / docs

- `commandFlags` += `-E` / `--env` for the supported commands.
- Help rows; README section "Reverb mode"; CHANGELOG `[Unreleased]`
  `### Added`.

## Dependencies

- Reverb units 12 + 20, shipped and verified on the dev host. No new Go
  modules (`net/http` + `encoding/json` from the stdlib).

## Verify when done

- [ ] `[reverb] url/token` in `global.toml` + `-E <project>/<env>` builds
      a target with no entry in `[connect_targets]`.
- [ ] `-E <env>` (bare) resolves the project through `GET /envs`; an env
      name present in two projects errors naming both candidates.
- [ ] `shell-run -E …`, `logs -E …`, `psql`/`db-pull -E …` work against a
      live Reverb (the highest-value surface).
- [ ] `409 not_ready` retries 3× before failing; `401`/`403` say
      "configuration"; a payload with no `ssh_host` names
      `REVERB_PUBLIC_HOST`; `-E` with no `[reverb]` is a usage error.
- [ ] The token never appears in any log line or error message.
- [ ] `push -E …` lands in `paths.overlay`; `--dest` under `paths.addons`
      is refused; a module also present in `paths.addons` warns.
- [ ] `push --clean -E …` empties the overlay (dry-run previews, confirm
      gates) without needing `git_deploy`.
- [ ] `deploy`/`watch`/`checkpoint`/`up`/`stop`/`restart` with `-E` fail
      with the deferred-unit error (exit 2), not a half-run.
- [ ] Classic `--from`/`--remote` targets are byte-identical to today
      (no Reverb code path reached, `rsc.reverb == nil`).
- [ ] Tests: `internal/reverb` against an `httptest` server (resolve
      decode, envs listing, each error code, the 409 retry loop);
      `remoteFlagsIn` with `-E` forms; `parseReverbSpec`;
      `reverbPushDest` (overlay default, addons refusal, explicit dest).
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass;
      registry/help cross-check tests stay green.
