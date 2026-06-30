# Unit 74: `deploy` — manually mark/unmark commits as deployed in the picker

## Goal

Let the operator toggle a commit's **"already deployed"** mark directly in
the deploy picker with `ctrl+d`, and persist that toggle to the target's
deploy history on confirm. This closes the gap left by Unit 65
([[65-deploy-history]]): the muted tint only ever appears for commits Echo
itself shipped, so a **brand-new branch**, a **rebased/amended commit**
(new SHA for already-shipped work), or a **first deploy from another
machine** all render every row as "new" even when the operator knows the
code is already on the server. `ctrl+d` lets them tell Echo the truth
without re-deploying, muting the row so it drops out of the "new since last
deploy" reading.

```
deploy --from prod
  ❯ a1b2c3d  [FIX] sale_order: …   ← ctrl+d → muted, recorded as deployed to prod
    e4f5g6h  [IMP] account_move…   ← already muted; ctrl+d → un-muted, mark removed
  [enter]                          ← marks persisted to prod's history
  [esc]                            ← marks discarded, nothing written
```

## Design

**Why a manual override at all.** Deploy history is keyed by **full SHA**
([[65-deploy-history]]): `LoadDeployedSHAs` mutes a row only when its exact
SHA was recorded by a prior successful deploy. SHA identity is correct for
the common case but blind to three real ones — a never-run branch (no
history), a `rebase`/`amend`/`cherry-pick` (the shipped content now carries
a different SHA), and a fresh clone / second machine (history is local,
per `~/.config/echo/…`). In all three the operator has out-of-band
knowledge — "this is already up" — that the SHA can't encode. `ctrl+d` is
the channel for that knowledge. It does **not** change the identity model;
it just seeds/edits the same per-target SHA set the auto-mark writes, so a
manually-marked commit that is later rebased will again read as new (that's
inherent and acceptable — the override is per concrete SHA).

**Interaction.** Inside the picker, `ctrl+d` toggles the `deployed` flag of
the **highlighted** row, but only for **commit rows** — dirty/uncommitted
modules have no SHA and are never deployable-marked, so `ctrl+d` is a no-op
on them. `ctrl+a` is the **bulk** counterpart: it marks **every visible
markable row** as deployed at once (clearing the whole "pending / por
desplegar" set in one keystroke — the new-branch case where the operator
knows the *entire* branch is already on the server). It respects the active
filter, so narrowing first turns it into "mark this filtered subset". It is
symmetric: if every visible markable row is *already* deployed, `ctrl+a`
un-marks them all instead. The filter input stays live (every printable key still filters);
`ctrl+d` is intercepted before the textinput, matching how `tab` is. The
mute updates **live** in the row's style as you toggle (a freshly-marked
row goes `p.Faint`, an un-marked one returns to `p.Fg`/normal), so the
screen always reflects the pending state.

**`ctrl+d` vs `tab`.** They are orthogonal: `tab` toggles **selection**
(this commit will be deployed now, `[×]`), `ctrl+d` toggles the **deployed
mark** (this commit is *already* deployed, muted). A row can be both
selected and marked, or either alone.

**Persist on confirm, not on toggle.** Toggles accumulate in the picker
model and are written to history **only when the user confirms with
enter**. `esc`/`ctrl+c` (cancel) and `ctrl+x` (quit) discard every pending
toggle — nothing is written. This makes the override safely reversible
mid-session, consistent with how selection itself is only acted on at
enter.

**Add and remove.** The toggle is symmetric: marking a never-deployed
commit **adds** its SHA to the target set; un-muting a previously-deployed
commit **removes** its SHA. `MarkDeployed` only ever appends, so this unit
adds the removal side and applies the net delta in one write.

**Scope = the same per-target set.** The marks land in exactly the store
Unit 65 defined — `~/.config/echo/deploy-history/<projectKey>.toml`, under
`[targets.<targetKey>]` for the resolved `(sshHost, remotePath)`. Marking a
commit deployed to *staging* does not mute it for *prod*. The history is
loaded and the target resolved in `RunDeploy` before the picker opens, so
both keys are already in hand.

**Build mode is excluded.** `runDeployBuild` opens the picker with no
resolved target (the real deploy is deferred to flags), so there is nowhere
to persist a mark. In build mode the picker is opened with marking
**disabled** (no markable rows), so `ctrl+d` does nothing and no delta is
produced.

**Help legend.** When the picker has markable rows, the help line gains
`· ctrl+d mark deployed`, alongside the existing `· muted = already
deployed` legend (which now appears whenever any row is — or becomes —
muted).

## Implementation

### `internal/config/deploy_history.go`

- Add `UnmarkDeployed(projectKey, targetKey string, shas []string) error` —
  load-remove-write: drop each given SHA from the target's set, rewrite the
  file (best-effort, same 0o600 / atomic write as `MarkDeployed`). A no-op
  (and no error) when the file/target is absent or none of the SHAs match.
- Add `UpdateDeployedMarks(projectKey, targetKey string, add, remove []string) error`
  — a single load-merge-write applying both sides at once (append `add`
  deduped, then strip `remove`), so a mixed toggle is one atomic write
  rather than a `MarkDeployed`+`UnmarkDeployed` pair. `RunDeploy` calls this
  one. Honors `deployHistoryCap`. Empty `add` **and** empty `remove` → no
  write.
- Unit-test: add-only, remove-only, mixed, remove-absent (no-op), and that
  a mixed delta round-trips through `LoadDeployedSHAs`.

### `internal/cmd/picker.go`

- `pickerItem` += `markable bool` — only markable rows respond to `ctrl+d`.
- `newFuzzyPicker` and `runFuzzyPickerCore` gain a `markable []string`
  param (label set), threaded next to `recent`/`deployed`; non-deploy
  callers pass `nil`. Items whose name is in the set get `markable: true`.
- `Update`: add `case "ctrl+d":` — if `len(m.visible) > 0` and the cursor
  item `markable`, flip `m.items[idx].deployed`; return without falling
  through to the filter. (Intercepted like `tab`, so it never reaches the
  textinput.) Add `case "ctrl+a":` — bulk: scan the **visible** rows for any
  markable-and-not-deployed; if found, set every visible markable row's
  `deployed = true` (mark all pending), else set them all to `false`
  (un-mark all). Also intercepted before the filter.
- `runFuzzyPickerCore` additionally returns the **final** deployed-label
  set (`fm.deployedNames()`, parallel to `selectedNames()`), so the caller
  can diff it against the initial set. Update its two call sites:
  `runFuzzyPicker` ignores the new return; `pickDeployItems` consumes it.
- `View()`: the help line appends `· ctrl+d mark · ctrl+a all` when
  `hasMarkable()` is true; `hasDeployed()` keeps gating the `· muted =
  already deployed` legend (now reflecting live toggles too).

### `internal/cmd/deploy.go`

- `pickDeployItems` signature gains a returned delta. Build the **markable
  label** set = every commit label (dirty labels stay non-markable), pass
  it through. After the run, map the picker's final deployed labels back to
  full SHAs via `byCommit`, and compute:
  - `added`   = final-deployed SHAs **not** in the incoming `deployedSet`,
  - `removed` = incoming `deployedSet` SHAs **no longer** in the final set.
  Return `(pickedCommits, pickedDirty, deployMarkDelta{added, removed}, err)`.
  A cancel returns a zero delta (nothing to persist).
- In `RunDeploy`, right after `pickDeployItems` returns successfully and
  **before** any remote work / prod gate, if the delta is non-empty call
  `config.UpdateDeployedMarks(projectKey, targetKey, delta.added,
  delta.removed)` and emit a `deploy.history` log line, e.g.
  `INFO deploy.history: updated marks added=<n> removed=<m> for target`.
  This is the "persist on confirm" point — it runs regardless of whether
  the operator later declines the prod gate or the deploy fails. Best-effort
  (a write error is logged, never fatal), like the existing auto-mark.
- The end-of-run auto-`MarkDeployed` (Unit 65) is unchanged and composes
  cleanly: re-marking an already-marked SHA is idempotent.

### `internal/cmd/build_deploy.go`

- `runDeployBuild` calls `pickDeployItems` with marking disabled (markable
  set empty / a flag), so `ctrl+d` is inert and the returned delta is
  ignored — build mode has no target to persist to.

## Dependencies

- Unit 61 (`deploy`) and Unit 65 (`deploy-history`) — this extends both.
  No external packages.

## Verify when done

- [ ] In `deploy --from X`, `ctrl+d` on a commit row mutes it live; `enter`
      then persists it, and re-running `deploy --from X` shows that commit
      muted from history (no actual deploy happened).
- [ ] `ctrl+d` on an already-muted (history) commit un-mutes it; on `enter`
      its SHA is removed from the target set and it renders normally next run.
- [ ] `ctrl+d` is a no-op on dirty/uncommitted module rows (no SHA).
- [ ] `ctrl+a` marks every visible commit row deployed in one keystroke;
      with all of them already deployed it un-marks them; it respects the
      active filter (only visible rows).
- [ ] `esc` / `ctrl+x` discard all pending `ctrl+d` toggles — history is
      unchanged.
- [ ] Marks are per target: a commit hand-marked for staging is NOT muted
      when deploying to a different target.
- [ ] A mixed toggle (some added, some removed) results in exactly one
      history write with the correct net set.
- [ ] Build mode (`--build`) ignores `ctrl+d` and writes no marks.
- [ ] `UnmarkDeployed` / `UpdateDeployedMarks` (add, remove, mixed, absent)
      and the picker's `ctrl+d` toggle + `deployedNames()` are unit-tested.
- [ ] `go build/vet/test ./...` pass; `registry`/`commandhl` cross-checks
      stay green; `CHANGELOG.md` `[Unreleased]` gets an `Added` entry.
