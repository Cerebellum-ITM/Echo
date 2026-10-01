# Unit 86: `compare --all` — whole-module sync status

## Goal

Extend `compare` (Unit 80) with a module-level mode:
`compare <mod> --all [--from <t>|--remote] [--copy]` diffs the **entire
module** against its Docker copy and prints a per-file status table —
`changed / added / missing / equal` — closing with a one-line verdict.
On a TTY, the differing files then feed an interactive loop: pick a
file, see its diff (the Unit 80 renderer), come back, until `esc`. The
"what exactly is out of sync?" check that belongs right before a
`push`/`deploy`.

## Design

**Checksums, not N reads.** Comparing a module file-by-file with `cat`
would cost one exec/SSH round-trip per file. Instead each side is
hashed in **one command** and compared as maps:

- Local: walk the module dir (Unit 80's `hostModuleFiles`) and compute
  MD5s in-process (`crypto/md5` — integrity check, not security).
- Container (local): one
  `find <dir> -type f -exec md5sum {} +` via `docker.Exec`.
- Remote: the same find+md5sum through Unit 79's transport split — one
  `runSSH` on the host filesystem, or one `remoteContainerCmd` exec
  for conf-mode remotes.

`md5sum` output parses to `hash → module-relative path`;
`skipViewPath` filters both sides. (BusyBox and coreutils both ship
`md5sum` with this output shape; a missing binary falls back to
size+mtime-less `cksum` is **not** attempted — it errors with a clear
message instead, this has not come up on any Odoo base image.)

**Statuses.** For the union of both file sets:

| status    | meaning                                   |
|-----------|-------------------------------------------|
| `changed` | both sides, different hash                |
| `added`   | local only (not yet pushed/deployed)      |
| `missing` | container only (deleted locally)          |
| `equal`   | same hash (counted, not listed)           |

**Output = the established table style.** A `modstate`-pattern aligned
table (`file | status`), status colored (changed=Warn, added=Info,
missing=Err, header accent), only non-equal rows listed, then the
verdict line:

```
echo.compare: module compared module=sale from=prod changed=3 added=1 missing=0 equal=41
```

All-equal short-circuits to a single `in sync` INFO line (the Unit 80
`identical` analog). `--copy` copies the plain table + verdict instead
of entering the interactive loop.

**Interactive drill-down.** On a TTY (and without `--copy`), after the
table: a single-select fuzzy picker (`"Changed files in <mod>"`) over
the changed+added+missing files; picking one renders its unified diff
with the Unit 80 pipeline (`unifiedDiff` + `ShowWithBat`, missing side
= empty content, same all-`+`/all-`-` semantics), then returns to the
picker; `esc` ends the loop and prints the close frame. Non-TTY skips
the loop (table only) — no fail-closed needed since the table is the
headless deliverable.

**Flag surface.** `--all` requires a module (positional or the standard
module picker); it composes with `--from`/`--remote` (remote side via
Unit 79 primitives) and `--copy`. `compare` without `--all` is
byte-for-byte Unit 80. A module with no host checkout errors exactly as
in Unit 80 (the local side is always the subject).

## Implementation

### `internal/cmd/compare_all.go` — new file

- `parseCompareArgs` (Unit 80) gains an `all bool` return for `--all`.
- `type fileStatus struct{ Rel, Status string }`;
  `diffModuleSets(local, container map[string]string) (rows
  []fileStatus, equal int)` — pure, the union/hash comparison, rows
  sorted by status then path.
- `localModuleHashes(moduleDir string) (map[string]string, error)` —
  walk + md5 in-process.
- `containerModuleHashes(ctx, vopts, module) (map[string]string, bool,
  error)` — local container: resolve the module dir with the
  Unit 80 probe (`containerAddonsPathsFor` + `test -f`), then one
  `find -exec md5sum`; `false` when the module is absent (every local
  file becomes `added`).
- `remoteModuleHashes(ctx, rv, module)` — same via `remoteModuleBase` +
  one SSH exec.
- `parseMD5Sums(out, prefix string) map[string]string` — pure parser
  (hash, two spaces, path; trims prefix, applies `skipViewPath`).
- `RunCompareAll(ctx, opts CompareOpts) (CompareAllResult, error)`
  returning module, from-label, rows, equal count; the drill-down loop
  lives in the REPL layer (it needs the pager + pickers interleaved).

### `internal/repl/compare.go` — branch

`runCompare` routes `--all` to a new `runCompareAll`: render the table
(`pad` helper, `modstate` style), emit the verdict frame, `--copy`
path, then the TTY drill-down loop (`runSingleFuzzyPicker` over the
non-equal rows → fetch both contents for the picked file → `unifiedDiff`
→ `ShowWithBat`; `ErrCancelled` from the picker ends the loop
normally).

### Registration

- Help rows under the `compare` block:
  `{"  --all", "Compare the whole module: changed/added/missing table"}`.
- `commandFlags["compare"]` += `--all`.
- No new command — registry/dispatch untouched.

### Tests (`internal/cmd/compare_all_test.go`)

- `parseMD5Sums`: coreutils and BusyBox spacing, prefix trimming,
  noise filtering.
- `diffModuleSets`: all four statuses in one table; all-equal → empty
  rows + correct count; sort order pinned.
- `localModuleHashes` over a temp module tree matches expected MD5s and
  skips `__pycache__`.
- `--all` flag parse composes with `--from`/`--copy`.

## Dependencies

None new — `crypto/md5` (stdlib) locally, `md5sum` in the containers
(present on the Odoo base images). Requires Units 79/80.

## Verify when done

- [ ] `compare sale --all` (conf-mode project) prints the table with a
      locally-edited file as `changed`, a new local file as `added`, a
      locally-deleted one as `missing`, and the correct `equal` count —
      using exactly one container exec for hashing.
- [ ] `compare sale --all --from prod` does the same against the remote
      (one SSH hash command), label `from=prod` in the verdict.
- [ ] An in-sync module prints the single `in sync` line and skips the
      drill-down.
- [ ] Picking a `changed` file in the drill-down shows its unified
      diff (Unit 80 renderer) and returns to the picker; `esc` closes
      with the verdict frame intact.
- [ ] `--copy` copies the plain table + verdict; no interactive loop.
- [ ] Non-TTY (`echo compare sale --all` piped) prints the table only
      and exits 0.
- [ ] A module absent from the container lists every file as `added`
      (no crash).
- [ ] `compare` without `--all` is unchanged (Unit 80 behavior).
- [ ] `help` shows the `--all` row; consistency tests stay green.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/cmd/...
      ./internal/repl/...` pass.
