# Unit 105: per-target action authoring (`actions … --from <target>`)

## Goal

Let one local project directory maintain **different deploy actions per
remote target**, which is the real-world shape whenever a single addons
repo feeds several environments (a dev instance and the client-facing
staging instance, both fed from the same folder).

Today `actions add/edit/rm` always mutate the **single local list**
(`cfg.DeployActions`) and then offer to upload that **whole list
wholesale** to whichever server is currently resolved. From one folder
serving N targets this cannot express "action A on dev, action B on
prod": every upload pushes the union to whatever server is linked at
that moment, so lists cross-contaminate and drift.

This is not a resolution bug — `resolveDeployActions` is already
server-first per target, and each target has its own server profile
(keyed by `ProjectKey(remote_path)`), so per-environment actions are
*representable*. The gap is purely authoring: there is no way to say
"edit **this target's** action set". This unit closes that gap by making
the **scope explicit** and removing the wholesale-upload footgun.

Observed in the wild (the motivating incident): `all_odoo` feeding
`habitta_dev` and `habitta_prod`; the local list held both build actions,
so dev's server ended up with **both** (`./build.sh dev` *and*
`./build.sh`) and prod's server with the **wrong one** (dev's). Both
server profiles had to be repaired by hand.

## Design

**The remote flag becomes the scope selector.** `--from <target>` /
`--remote` already mean "that target" across every command. Reuse it:

| Invocation | Scope operated on |
|---|---|
| `actions add\|edit\|rm --from <target>` | **that target's server profile** (`~/.config/echo/projects/<key>.toml` on the server) |
| `actions add\|edit\|rm --remote` | the **linked** target's server profile |
| `actions add\|edit\|rm` (no remote flag) | the **local** project list (the fallback) |

Server-scoped operations are a straight read-modify-write of that
server's `[[deploy.actions]]`: the current set comes from the already
fetched `rsc.prof.DeployActions`, the mutation is applied to *that* list,
and the result is written back with the existing
`uploadActionsToServer`. **The local list is never read or written in
this path**, so two targets can hold entirely different sets and neither
can clobber the other.

**Remove the wholesale upload offer.** `offerUploadActions` — "Upload
these actions to the server profile?", which pushed the entire local list
— is deleted. It is exactly the mechanism that caused the
cross-contamination: a local list is inherently *one* set, so pushing it
to a server is only correct when there is one target. With scope now
explicit, an upload is no longer an afterthought on a local edit; you
name the target you are editing. This is a deliberate behavior change
(documented under `### Changed`).

**Resolution is untouched.** Server-first wholesale
(`resolveDeployActions`: `--no-actions` › server › local › none) stays
exactly as-is. This unit only changes *who writes where*.

**The empty-server-list gotcha is surfaced.** Because resolution is
wholesale, a server list that becomes **empty** does not mean "no actions
run" — it means the target **falls back to the local list**. So
`actions rm --from <t>` that removes the last entry emits a WARNING
naming the local set that will now apply (or "none" when local is also
empty). Silently switching a target from its own actions to the shared
fallback is precisely the class of surprise this unit exists to kill.

**Guards.** A server-scoped write goes through the existing
`confirmRemoteProd` (skippable with `--force`) before touching the
profile, same as the old upload path. The wizard still needs a TTY
(`requireTTY`).

**Closing lines** (Odoo-cohesive, scope always explicit):

```
echo.actions: action added name="Create Docker Image" scope=server target=habitta_dev phase=post_push
echo.actions: action removed name="…" scope=server target=habitta_prod
echo.actions.warning: server action list is now empty — this target falls back to the local list count=2
echo.actions: action added name="…" scope=local
```

## Implementation

### `internal/cmd/actions.go`

- **Scope helper.** `func (p actionsArgs) remoteScoped() bool { return p.from != "" || p.remote }`
  — parallel to `isTestManage()`/`isCheckpointManage()`. No new flags:
  `--from`/`--remote` are already parsed by `remoteFlagsIn`.
- **`runActionsAdd`** splits on scope:
  - *server-scoped*: `rsc := resolveRemote()`; `base := rsc.prof.DeployActions`;
    run `actionWizard` (unchanged — its remote presets already use
    `resolveRemote`); `next := append(base, a)`;
    `config.ValidateDeployActions(next)`; `confirmRemoteProd`;
    `uploadActionsToServer(ctx, rsc, next, opts)`. **No `saveActions`.**
  - *local*: today's `saveActions(opts, next)` path, minus the upload offer.
- **`runActionsEdit` / `runActionsRm`** take the same fork. The list the
  name/picker resolves against is the **scoped** list — so
  `pickActionIndex` gains an explicit `actions []config.DeployAction`
  parameter instead of reading `opts.Cfg.DeployActions` implicitly.
- **`runActionsRm`** (server-scoped) checks `len(next) == 0` after the
  removal and emits the fallback WARNING described above, reporting the
  local list length it will defer to.
- **`runActionsList`** is already remote-aware (it resolves the server
  profile and reports `source: server|local`); only its log/table gains
  the explicit `scope` field for symmetry. No behavior change.
- **Delete `offerUploadActions`** and its three call sites
  (`runActionsAdd`/`runActionsEdit`/`runActionsRm`).

### `internal/cmd/actions_wizard.go`

- Remove `offerUploadActions` (moved/deleted per above). `actionWizard`
  and `pickExecPath` are unchanged — the addons preset and the remote
  directory picker already resolve through the lazy `resolveRemote`, so
  they work identically under a server scope.

### Registration / docs

- `commandFlags["actions"]` unchanged (`--from`/`--remote`/`--json`/`--force`
  already registered).
- Help: note on the `actions` rows that `--from <target>` / `--remote`
  make `add`/`edit`/`rm` operate on **that target's server profile**,
  and that without them they edit the local fallback list.
- README: rewrite the actions prose — the per-target model, the
  server-first wholesale rule, the empty-server-list fallback warning,
  and the removal of the upload prompt.
- CHANGELOG `[Unreleased]`: `### Changed` (scope selector + wholesale
  upload removed), `### Fixed` is not claimed — this is a design change,
  not a defect in resolution.

## Dependencies

- Unit 92 (deploy actions, server-first wholesale resolution) and
  Unit 93 (interactive `actions` CRUD + upload) — landed; this unit
  reshapes 93's write path. Reuses `uploadActionsToServer`,
  `config.WithDeployActions`, `confirmRemoteProd`, `resolveRemoteShell`.
  No new packages, no config-schema change.

## Verify when done

- [ ] `actions add --from <t>` writes **only** to that target's server
      profile; the local `[deploy.actions]` is byte-identical afterwards.
- [ ] Two targets fed from the same directory can hold **different**
      action sets, and editing one never mutates the other (the
      `habitta_dev` / `habitta_prod` regression case).
- [ ] `actions add` with no remote flag edits the local list only and
      **never** contacts a server (no SSH, no upload prompt).
- [ ] `actions edit --from <t>` / `rm --from <t>` resolve the name and
      the picker against the **server's** list, not the local one.
- [ ] `actions rm --from <t>` that empties the server list emits the
      fallback WARNING naming the local count that now applies.
- [ ] A server-scoped write against a `prod`-stage target is gated by
      `confirmRemoteProd` and bypassed by `--force`.
- [ ] A server profile that does not exist yet is created by the first
      server-scoped write (absent file → composed from empty).
- [ ] `actions list` / `actions list --from <t>` behavior is unchanged
      apart from the added `scope` field.
- [ ] Deploy-time resolution is byte-identical to today (no change to
      `resolveDeployActions`).
- [ ] Tests: `remoteScoped()` parsing (`--from`, `--from=`, `--remote`,
      none); server-scoped add/edit/rm against a stubbed SSH asserting the
      composed TOML and that no local write occurred; local-path add
      asserting no SSH; empty-server-list warning; `pickActionIndex` over
      an explicitly passed list.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass;
      registry/help cross-check tests stay green.
