# Unit 106: `link` as a target switcher (`link`, `--next`, `--list`)

## Goal

Make switching the active environment a one-gesture operation. A single
directory routinely feeds several targets (dev instance + the
client-facing staging instance), and the per-directory `link` binding is
what every command falls back to when `--from` is absent — so the binding
*is* the "current system". Today changing it means retyping
`link <name>` from memory, and there is no way to see which target is
current alongside the alternatives.

Bare `link` already opens a picker, but it is a *bind* picker, not a
*switcher*: it does not mark or preselect the current binding, and its
rows show only what `pickConnectTarget` renders, so you cannot tell where
you are or what you are switching away from.

## Design

**Three additions, no new concepts.** The binding model is unchanged
(one `[connect]` per project, targets live in `global.toml`); this unit
only improves how you move between them.

**1. Bare `link` becomes an explicit switcher.** The picker gains
current-awareness:

- The currently-linked target is **marked** and **preselected**, so the
  switcher opens on "where you are".
- Rows carry identifying context from the target record:
  `name · db_name · ssh_host:remote_path` (matching the `connect` picker's
  shape).
- Picking the **current** target is a no-op: an INFO line
  (`already linked`) and **no** rewrite and **no** probe — switching to
  where you already are should not cost an SSH round trip.

**2. `link --next` — non-interactive cycle.** Rebind to the next target
in the sorted registry, wrapping at the end. For the common two-target
setup this is a straight toggle (`dev ⇄ prod`) with no picker, which is
the whole point: fast alternation from a script or a keystroke. With
fewer than two targets it is an `ErrUsage` (nothing to cycle to).

**3. `link --list` — see without changing.** Print every target with the
current one marked, and exit. Distinct from `--show`, which reports the
*current binding only* and pays for a remote probe + `compose ps`;
`--list` is the cheap inventory view. Honors `--json` for scripting.

**No SSH in the switcher.** The picker, `--next` and `--list` all render
purely from the local target records (`name`, `db_name`, `ssh_host`,
`remote_path`). Deliberately **not** fetching each target's `stage` or
container health: that would need one SSH round trip per target and turn
an instant toggle into a multi-second stall. `--show` remains the command
that actually probes.

**Probe on switch stays.** An actual rebind keeps today's
`probeLink` behavior (save first, probe after, unreachable remote is a
WARNING and never a failure) — it is the confirmation that you landed
where you meant to.

**Flag exclusivity.** `--next`, `--list`, `--show`, `--rm` and a target
positional are all mutually exclusive → `ErrUsage`.

**Closing lines** (Odoo-cohesive, mirroring the current `saved` line):

```
echo.link: saved target=habitta_prod host=Ionos-personal-pascual path=/home/…/odoo
echo.link: already linked target=habitta_dev
echo.link.error: --next needs at least two connect targets count=1
```

## Implementation

### `internal/cmd/link.go`

- **`linkArgs`** gains `next bool` and `list bool`.
- **`parseLinkArgs`**: parse `--next` / `--list`; extend the existing
  mutual-exclusion block so at most one of
  `{target, --show, --rm, --next, --list}` is present.
- **`RunLink`** dispatch order: `--rm` › `--show` › `--list` › `--next` ›
  bind (today's default).
- **`runLinkList(opts)`** — render every `cfg.ConnectTargets` row with a
  current marker resolved via the existing `linkTargetName(cfg)`; `--json`
  emits `[{name, ssh_host, remote_path, db_name, current}]` (`[]` when
  empty, per the `finishActionsJSON` convention). No SSH, no write.
- **`runLinkNext(ctx, opts)`** — resolve the current name with
  `linkTargetName`; find its index in the sorted target list; pick
  `(i+1) % len`; if the current binding matches no target (unlinked or
  hand-edited), start at index 0. `len(targets) < 2` → `ErrUsage`. Then
  reuse `runLinkBind`'s persistence path so save/probe/logging stay
  identical.
- **`resolveLinkTarget`** (bare-`link` path) passes the current name into
  the picker so it can mark and preselect it.
- **`pickConnectTarget`** gains a `current string` parameter: rows render
  as `name · db_name · host:path` with a marker on the current one, and
  the picker opens with that row selected. Callers that have no notion of
  "current" (the `connect` flow) pass `""` and are visually unchanged.
- **`runLinkBind`** short-circuits when the resolved target equals the
  current binding: INFO `already linked`, no `SaveProject`, no probe.

### Registration / docs

- `commandFlags["link"]` += `--next`, `--list` (currently
  `{"--show", "--rm"}`).
- Help rows: `--next` — "Switch to the next connect target (cycles)";
  `--list` — "List connect targets, marking the current one".
- README: extend the link/targets prose with the switcher, `--next` as
  the two-target toggle, and the `--list` vs `--show` distinction (cheap
  inventory vs probing the current binding).
- CHANGELOG `[Unreleased]` `### Added`.

## Dependencies

- Unit 18 (`connect` targets registry in `global.toml`) and the existing
  `link` bind/show/rm — landed. Reuses `linkTargetName`,
  `pickConnectTarget`, `connectTargetNames`, `probeLink`,
  `config.SaveProject`. No config-schema change, no new packages.

## Verify when done

- [ ] Bare `link` marks **and** preselects the currently-linked target,
      and its rows show `name · db_name · host:path`.
- [ ] Picking the current target in the switcher is a no-op: INFO
      `already linked`, config file mtime unchanged, no SSH.
- [ ] `link --next` cycles to the next target and wraps at the end; with
      exactly two targets it toggles between them.
- [ ] `link --next` with 0 or 1 target is a usage error (exit 2).
- [ ] `link --next` from an unlinked directory binds the first target.
- [ ] `link --list` prints all targets with exactly one marked current,
      makes **no** SSH connection and **no** write; `--json` emits the
      documented shape and `[]` when there are no targets.
- [ ] `--next`, `--list`, `--show`, `--rm` and a target positional are
      pairwise mutually exclusive → `ErrUsage`.
- [ ] An actual rebind still saves **before** probing, and an unreachable
      remote is a WARNING, not a failure (Unit 18 invariant preserved).
- [ ] The `connect` flow's picker is visually unchanged (passes
      `current == ""`).
- [ ] Tests: `parseLinkArgs` (each new flag + every exclusivity pair);
      `runLinkNext` index math (wrap, unlinked start, `<2` error) as a
      pure helper over a target slice; `runLinkList` JSON sample + empty;
      the already-linked no-op asserting no save.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass;
      registry/help cross-check tests stay green.
