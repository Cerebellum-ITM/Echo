# Unit 70: `update --installed` — pick from all installed modules

## Goal

`update` with no module args opens a picker scoped to the **project's
addons** (host folders or the instance's `odoo.conf` `addons_path`). That
never lists Odoo's own core/base modules, so there's no way to *discover*
and update something like `base`, `web`, or `account` from the picker —
you have to know the name and type `update base` explicitly.

Add an `--installed` flag to `update`: it sources the picker from **every
module marked installed in the active database** (`ir_module_module`),
not just the repo, so any installed module (core or third-party) can be
selected and updated.

## Design

`update base` already works today — explicit module args skip the picker
and run `odoo -u base` directly. The gap is purely the picker's candidate
list. So `--installed` only swaps the picker source; everything downstream
(the `-u` run, `--last` recording, the start line, `--i18n`/`--level`) is
unchanged.

The installed list comes from the same query `modstate` uses:
`docker.ModuleStates(…, installedOnly=true)` over the project's DB
container — names already sorted by the query. This needs DB access
(`DBContainer` + `DBName`); a missing container is a clear error, not a
silent empty picker.

`--installed` is meaningful only on the picker path: combined with
explicit modules, `--all`, or `--last` (all of which skip the picker) it
is simply inert — no error, to keep the flag composable.

## Implementation

### `internal/cmd/modules.go`

- `RunUpdate`: parse `--installed` into a bool alongside `--all`/`--last`/
  `--i18n`; thread it into the no-modules picker branch.
- `pickModulesForUpdate(ctx, opts, recent, installed)`: when `installed`,
  source candidates from `installedModules` and title the picker
  "Installed modules to update"; otherwise `resolveModules` as today.
- `installedModules(ctx, opts) ([]string, error)`: guard
  `DBContainer`/`DBName`, then `docker.ModuleStates(…, true)` →
  `installedModuleNames`.
- `installedModuleNames(rows) []string`: pure name extractor (testable).

### `internal/repl/commands.go` / `repl.go`

- `commandFlags["update"]` += `"--installed"`.
- Help: add `{"  --installed", "Pick from all installed modules (e.g. base), not just the repo"}`.

### Demo (DOC, separate commit)

- `demo/sim/echo-sim.sh`: `update()` branches on a `--installed` arg to
  render the installed-modules picker (base/web/mail/account/sale + a repo
  module) then an `update base` stream. Logger pastel for
  `echo.update.module.base[.start]` computed via the binary's FNV-1a%8.
- `demo/tapes/update-installed.tape` + rendered
  `demo/gifs/update-installed.gif`, embedded in the README Modules section.

### Tests (`internal/cmd/modules_test.go`)

- `TestInstalledModuleNames`: rows → names, drops blanks, preserves order.

## Verify when done

- [ ] `update --installed` opens a picker listing core modules (`base`,
      `web`, …) plus the repo's; picking `base` runs `odoo -u base`.
- [ ] `update base` (explicit) still works without the flag.
- [ ] `--installed` with `--all`/`--last`/explicit modules is inert.
- [ ] Missing DB container → clear error, not an empty picker.
- [ ] `go build/vet/test` pass; flag highlights/Tab-completes.
