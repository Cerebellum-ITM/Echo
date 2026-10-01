# Unit 95: `[deploy] push` default + `deploy --set-push` CLI setup

## Goal

Make `deploy --push` the default via config, so an image-built remote
(where a deploy always ships code) doesn't need `--push` typed every
time. A `[deploy] push` boolean, resolved **server-first with local
fallback** (the Unit 92/93 pattern), turns push on by default; a
`--no-push` flag overrides it per run. A config-only `deploy --set-push`
sets the flag from the CLI so nobody hand-edits TOML.

## Design

**Config: `[deploy] push`.** A bool in the existing `[deploy]` table
(alongside `[[deploy.actions]]`), valid in the server's global/project
profile and the local config.

```toml
[deploy]
push = true
```

**Precedence for the effective push default** (highest first):

1. `--no-push` → off for this run.
2. `--push` → on for this run.
3. Server `[deploy] push` (from the resolved profile).
4. Local `[deploy] push`.
5. Default `false` (today's behavior when nothing is set).

Resolved in `RunDeploy` once the remote profile is read: when neither
`--push` nor `--no-push` is passed, the effective push comes from
`prof.DeployPush ?? cfg.DeployPush ?? false`. `watch` inherits it (it
drives `deploy --push`/plain deploy through the same headless path — a
configured default flows through unchanged).

**CLI setup: `deploy --set-push[=true|false]`.** A config-only mode
(the `push --set-dest` parallel): it persists `[deploy] push` to the
**local** project profile and exits — no remote resolution, no deploy,
fully headless. `--set-push` alone means `= true`; `--set-push=false`
turns the default back off. Closes with
`echo.deploy: deploy push default set push=<bool>`.

**Interaction.** `--set-push` short-circuits before everything else
(it's local config). `--push` and `--no-push` are mutually exclusive
(usage error). The pointer types (`*bool`) distinguish "unset" (fall
through the precedence) from an explicit `false`.

## Implementation

### `internal/config/config.go`

- `deployFile` gains `Push *bool \`toml:"push"\``.
- `Config` gains `DeployPush *bool`; `RemoteProfile` gains
  `DeployPush *bool`.
- `Load`: after the actions merge, set `cfg.DeployPush` from
  `g.Deploy.Push` then `p.Deploy.Push` (project wins; nil stays nil).
- `ParseRemoteProfile`: set `DeployPush` from server global then project
  `[deploy] push` (project wins, no default).
- `SaveProject`: emit `[deploy]` when actions **or** push is set — the
  existing `if len(cfg.DeployActions) > 0` guard widens to also include
  `cfg.DeployPush != nil`, and the `deployFile` carries `Push`.

### `internal/cmd/deploy.go`

- `deployArgs` gains `noPush bool` and `setPush *bool`.
- `parseDeployArgs`: `--no-push` → `noPush`; `--set-push` → `setPush =
  &true`; `--set-push=true|false` → parsed bool. Validate `--push` +
  `--no-push` mutually exclusive.
- `RunDeploy`: right after `parseDeployArgs` (before the rollback
  branch), if `p.setPush != nil`, persist local `[deploy] push` via a
  `*opts.Cfg` copy + `config.SaveProject`, log the set line, and return.
- After the remote profile is read (`prof`), compute the effective push:
  when `!p.push && !p.noPush`, set `p.push = prof.DeployPush ??
  cfg.DeployPush ?? false`; `--no-push` forces it off. The existing
  `runPush`/action-phase checks read `p.push` unchanged.

### Registration

- `commandFlags["deploy"]` += `--no-push`, `--set-push`; help rows
  (`--no-push` — "Skip the code push even when it's the configured
  default"; `--set-push[=bool]` — "Set deploy to push by default and
  exit").
- README: extend the "Deploy actions"/push prose with the `[deploy]
  push` default + `deploy --set-push` one-liner.
- CHANGELOG `[Unreleased]` `### Added`.

## Dependencies

- Unit 91 (`[push] path`) and Unit 92 (`[deploy]` table / server-first
  resolution) — landed. No new packages.

## Verify when done

- [ ] With `[deploy] push = true` (local or server), `deploy --from <t>`
      pushes without `--push`; `deploy --no-push` skips the push.
- [ ] A server `[deploy] push` wins over the local one; with neither,
      behavior is byte-identical to today (push only with `--push`).
- [ ] `deploy --set-push` writes `[deploy] push = true` to the local
      profile headlessly (no SSH, no deploy) and exits;
      `--set-push=false` turns it off.
- [ ] `--push` + `--no-push` together is a usage error (exit 2).
- [ ] `watch` honors the configured default.
- [ ] Tests: `parseDeployArgs` (`--no-push`, `--set-push[=bool]`, the
      mutual-exclusion error), the effective-push precedence resolver,
      `ParseRemoteProfile`/`Load` decoding `[deploy] push`, `SaveProject`
      round-trip with push set.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass;
      registry/help cross-check tests stay green.
