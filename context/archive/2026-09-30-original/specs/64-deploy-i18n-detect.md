# Unit 64: `deploy` — auto-detect i18n changes → `--i18n-overwrite`

## Goal

`deploy` detects when the selected commits change translation files and
adds `--i18n-overwrite` to the remote Odoo update run automatically: if
any resolved commit touches a path under its module's `i18n/` folder and
that module lands in the `-u` set, the combined `-i`/`-u` run carries the
flag, so the shipped `.po` terms replace the database's. Manual
overrides: `--i18n` forces the flag on, `--no-i18n` suppresses the
detection.

```
deploy                  # auto: -u carries --i18n-overwrite iff i18n/ touched
deploy --i18n           # force --i18n-overwrite even with no i18n/ change
deploy --no-i18n        # never add --i18n-overwrite this run
```

## Design

**Detection unit = the deployed commits, not the working tree.** Unit 61
already resolves each selected commit to a module and (in the diff
fallback) reads its changed paths via `git diff-tree`. The detection
reuses exactly that source of truth: a module "has i18n changes" when at
least one of its resolving commits changed a path matching
`<module>/i18n/…` (any file — `.po`, `.pot`, anything under the folder).
Commits resolved by the subject scheme don't read their diff today; with
this unit the diff is read for every resolved commit, so subject-resolved
commits detect i18n changes too. A failing `diff-tree` on a
subject-resolved commit degrades to "no i18n change" with a `WARNING`
(it must not turn a resolvable commit into a skipped one).

**Single run, global flag.** The deploy keeps Unit 61's one-Odoo-run
design. `--i18n-overwrite` is global to the process, so when any module
of the `-u` set triggered detection, every updated module's translations
are overwritten — acceptable because deployed modules are first-party and
their `.po` files are the source of truth (same rationale as Unit 49).
The plan line states this explicitly so the operator sees it before the
prod gate.

**Only the update set counts.** Modules in the `-i` set never trigger
the flag: a fresh install loads the module's translations anyway, and
`--i18n-overwrite` documents against update. If i18n changes are detected
only for install-set modules, the run carries no flag (and a log line says
so).

**Overrides.** `--i18n` and `--no-i18n` are mutually exclusive (usage
error if both). `--i18n` forces the flag regardless of detection
(symmetry with `update --i18n`, Unit 49); `--no-i18n` suppresses it even
when detected — the detection still runs and logs, so the operator sees
what was suppressed. Like Unit 49, nothing is persisted: detection and
overrides are per-invocation.

**Flag spelling.** Reuses `odoo.WithI18nOverwrite` (Unit 49) over the
`odoo.InstallUpdate` argv — both only append flags, so they compose. The
spelling is identical across Odoo 17/18/19 and `-l` is not an
alternative (it only scopes `--i18n-export/--i18n-import`, never `-u`).

**Log lines.** Within the existing `echo.deploy` family:

- per detection: `INFO i18n changes detected commit=<sha7> module=<m>`
- plan line gains a field: `plan update=… install=… i18n=on|off|forced|suppressed`
  (`on` = detected, `forced`/`suppressed` = override, `off` = nothing detected)
- when detection hits only install-set modules:
  `INFO i18n changes on install-set modules — no overwrite needed module=<m>`

`--dry-run` shows the detection outcome in the plan, same as the rest.

## Implementation

### `internal/cmd/deploy.go`

- `deployArgs` += `i18n, noI18n bool`; `parseDeployArgs` handles
  `--i18n`/`--no-i18n` (both set → usage error).
- `pathsTouchI18n(module string, paths []string) bool` — true when any
  path, slash-normalized, has the prefix `<module>/i18n/`.
- Resolution loop: after a commit resolves (either scheme), fetch its
  paths (`gitCommitPaths` — already available from the diff fallback;
  avoid the second call when the fallback already ran) and record
  `i18nTouched[module] = true` when `pathsTouchI18n` hits, logging the
  detection line.
- After `splitInstallUpdate`: compute
  `overwrite := p.i18n || (!p.noI18n && anyInUpdate(update, i18nTouched))`,
  add the `i18n=` field to the plan line, and wrap the run argv:
  `odoo.WithI18nOverwrite(odoo.InstallUpdate(conn, install, update), overwrite)`.

### Wiring

- `commandFlags["deploy"]` += `--i18n`, `--no-i18n` (flag highlight +
  Tab completion).
- `helpSections`: deploy's row mentions the auto-detection and the two
  overrides.

## Dependencies

- Unit 61 (`deploy`) and Unit 49 (`WithI18nOverwrite`) — both landed.
- none external.

## Verify when done

- [ ] A deploy whose selected commit changes `<mod>/i18n/es.po` (module
      in the `-u` set) runs the remote Odoo argv with `--i18n-overwrite`;
      the same deploy without i18n changes runs without it.
- [ ] A commit resolved by **subject** whose diff touches `i18n/` also
      triggers detection (the diff is read even when the subject
      resolves).
- [ ] i18n changes on an **install-set** module do not add the flag and
      log the "no overwrite needed" line.
- [ ] `--i18n` forces the flag with no detection; `--no-i18n` suppresses
      a positive detection (logged as suppressed); `--i18n --no-i18n` is
      a usage error.
- [ ] `--dry-run` reports `i18n=on/off/forced/suppressed` in the plan
      without executing anything remote.
- [ ] `pathsTouchI18n`, the override matrix in `parseDeployArgs`, and the
      overwrite decision are unit-tested with fixtures.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass; the
      `registry`/`commandhl` cross-checks stay green with the new flags;
      `CHANGELOG.md` `[Unreleased]` gets an `Added` entry.
