# Unit 65: `deploy` — remember already-deployed commits, tint them in the picker

## Goal

`deploy` remembers which commits it has already deployed **to a given
remote target** and tints those rows in the commit picker so the operator
sees, at a glance, what's new since the last deploy. The memory is local
(per the operator's machine), keyed by the local addons repo and the
target, and survives even though the deploy itself runs entirely on the
remote — the commits and the picker are local, so the record is too.

```
deploy --from prod      # commits already deployed to prod render muted
deploy                  # picks the link/target; tint reflects THAT target
```

## Design

**Why local even though deploy is remote.** The set of commits comes from
the local repo (`git log`), the picker is local, and "have I shipped this
commit?" is a fact about the operator's workflow, not server state. So the
history lives next to the rest of Echo's local recall
(`~/.config/echo/…`), mirroring `update --last` ([[reference-odoo-cli-docs]]
not involved here — this is pure local state like `last_update.go`).

**Scope = per target, not global.** A commit shipped to *staging* is not
shipped to *prod*. The record is keyed by `(localRepo, target)` so the
tint answers "already deployed **to this destination**". The local repo is
identified by `config.ProjectKey(opts.Root)` (sha256 of the abs path, the
same key `update --last` uses); the target by a hash of
`sshHost + "\x1f" + remotePath` (stable across runs, valid as a TOML map
key).

**What counts as deployed.** A commit is recorded only when the deploy run
reaches the end successfully:

- `--dry-run` records nothing (it executes nothing remote).
- A failed step (`stop`/`up`/odoo run) or a declined prod gate returns
  before the mark — nothing is recorded.
- Only the **selected commits that resolved to a module** (the ones that
  actually entered the `-i`/`-u` run) are recorded; unresolved/skipped
  commits never were deployed, so they stay unmarked.

**Storage.** `~/.config/echo/deploy-history/<projectKey>.toml`, mirroring
`last-updates/`:

```toml
[targets.<targetKey>]
shas = ["<full-sha>", "<full-sha>", …]
saved_at = 2026-06-12T17:45:00Z
```

Best-effort exactly like `LoadLastUpdate`: a missing or unparseable file,
or an absent target, yields an empty set and no error — the tint is an
optimization, never a hard dependency. `MarkDeployed` merges the new SHAs
into the target's set (dedup, preserve prior), bounded to the most recent
`deployHistoryCap = 1000` SHAs so the file can't grow without limit.

**Picker tint.** The `fuzzyPicker` already tints `recent` items (the last
`update`'s modules) in `p.Info`. This adds a parallel `deployed` flag:
deployed rows render their name in `p.Faint` (muted/greyed) — semantically
"already shipped, deprioritized", and visually distinct from both the
default `p.Fg` and `recent`'s bright `p.Info` (the deploy picker has no
`recent` concept, so they never collide). The cursor row keeps its accent
highlight regardless. When any row is deployed, the help line gains a
`· muted = already deployed` legend (mirroring `· highlighted = last
update`).

## Implementation

### `internal/config/deploy_history.go` (new)

- `DeployHistory{ Targets map[string]DeployTarget }`,
  `DeployTarget{ SHAs []string; SavedAt time.Time }`.
- `deployHistoryPath(projectKey) → ~/.config/echo/deploy-history/<key>.toml`
  (via `configRoot()`, like `lastUpdatesPath`).
- `DeployTargetKey(sshHost, remotePath string) string` — sha256 hex of
  `host\x1fpath`.
- `LoadDeployedSHAs(projectKey, targetKey string) map[string]bool` —
  best-effort, empty map on any miss.
- `MarkDeployed(projectKey, targetKey string, shas []string) error` —
  load-merge-dedup-cap-write; creates the dir/file as needed (0o600,
  matching the other recall files).

### `internal/cmd/picker.go`

- `pickerItem` += `deployed bool`.
- Thread a `deployed []string` set through `runFuzzyPickerCore`
  (alongside `recent`), marking items whose name is in the set; the other
  callers pass `nil`. `newFuzzyPicker` gains the param.
- `View()`: name-style priority cursor → deployed (`p.Faint`) → recent
  (`p.Info`) → default. `hasDeployed()` gates the new help legend.

### `internal/cmd/deploy.go`

- After resolving the remote (`sshHost`, `remotePath`), compute
  `projectKey := config.ProjectKey(opts.Root)` and
  `targetKey := config.DeployTargetKey(sshHost, remotePath)`; load
  `deployedSet := config.LoadDeployedSHAs(projectKey, targetKey)`.
- `pickDeployCommits` takes the deployed-sha set, builds the deployed
  **label** set (`sha7  subject` for commits whose full sha is in the
  set), and passes it to the picker.
- In the resolution loop, collect `deployedShas` = the full SHA of each
  selected commit that resolved to a module.
- At the end of a successful run (after the odoo step, before/with the
  final summary log), `_ = config.MarkDeployed(projectKey, targetKey,
  deployedShas)`. Dry-run and every early-return path skip it.
- A `deploy.history` log line on save: `INFO … recorded n=<k> deployed
  commits for target` (so the persistence is visible in the stream).

## Dependencies

- Unit 61 (`deploy`) and the existing `config` recall plumbing
  (`configRoot`, `ProjectKey`). None external.

## Verify when done

- [ ] After a successful `deploy --from X`, re-running `deploy --from X`
      renders the just-deployed commits muted; commits newer than that
      deploy render normally.
- [ ] The tint is per target: a commit deployed to staging is NOT muted
      when deploying to a different target.
- [ ] `--dry-run`, a declined prod gate, and a failed step all leave the
      history unchanged (nothing recorded).
- [ ] Only resolved/deployed commits are recorded; skipped (unresolved)
      commits are never marked.
- [ ] A missing/corrupt history file degrades to "nothing deployed yet"
      with no error.
- [ ] `DeployTargetKey`, `LoadDeployedSHAs`/`MarkDeployed` (merge, dedup,
      cap), and the picker's deployed tint are unit-tested.
- [ ] `go build/vet/test ./...` pass; `registry`/`commandhl` cross-checks
      stay green; `CHANGELOG.md` `[Unreleased]` gets an `Added` entry.
