# Unit 49: `update --i18n` — overwrite module translations on update

## Goal

A new `--i18n` flag on `update` that overwrites the updated modules'
translations from their shipped `.po` files. It appends Odoo's
`--i18n-overwrite` to the `-u` run so terms already translated in the
database are replaced by the modules' translations instead of being kept.
Applies to every active language and composes with `--all`, `--last`, and
`--level`.

```
update sale --i18n   →   odoo -u sale --i18n-overwrite --stop-after-init
```

## Design

`update` already drives the Odoo `-u` invocation; this is purely an extra
flag on the same single run — no second Odoo process, no `.po` path. The
flag spelling is identical across Odoo 17/18/19 (verified against the CLI
reference, see [[reference-odoo-cli-docs]]).

**Why all languages, no per-language option.** Odoo's `-l/--language`
documents as "specify the language of the translation file. Use it with
`--i18n-export` or `--i18n-import`" — it does **not** scope a `-u` update.
A `-u --i18n-overwrite` run overwrites terms for every active language;
there is no clean CLI way to restrict it to one language during an update.
Per-language overwrite is already covered by the existing
`i18n-update <mod> <lang>` (a scoped `--i18n-import` with `-l`, which Odoo
does honor). So `--i18n` is a boolean — the honest mapping to the CLI.

The flag is **not persisted** in the last-update record: it's a
per-invocation choice (unlike `--level`, which is inherited by `--last`),
so a plain `update --last` never silently re-overwrites translations.

## Implementation

### `internal/odoo/cmd.go`

`WithI18nOverwrite(cmd Cmd, on bool) Cmd` — appends `--i18n-overwrite` when
`on`, else a no-op. Mirrors `WithLogLevel`; keeps the Odoo flag spelling in
the odoo package while the cmd layer decides when to apply it.

### `internal/cmd/modules.go` — `RunUpdate`

- Parse `--i18n` into a bool in the same `rest` loop that handles
  `--all`/`--last` (so it's consumed, never treated as a module name).
  `extractLevel` runs first and passes `--i18n` through untouched.
- Wrap each of the four update argvs with
  `odoo.WithI18nOverwrite(odoo.WithLogLevel(odoo.Update…/UpdateAll…, level), i18n)`:
  the `--all` path, the explicit/picked-modules path, and both `--last`
  branches (all/modules). Order between the two wrappers is irrelevant —
  both only append flags.

### Wiring

- `commandFlags["update"]` += `--i18n` (flag highlight + Tab completion).
- `helpSections` (Modules): sub-row `--i18n` under `update`.

## Dependencies

- none.

## Verify when done

- [ ] `update <mod> --i18n` runs `odoo -u <mod> --i18n-overwrite
      --stop-after-init`; `--i18n` is stripped from the module list.
- [ ] Composes with `--all` (`-u all --i18n-overwrite`), `--last`, and
      `--level` (both flags present, any order).
- [ ] A plain `update --last` (no `--i18n`) does not add `--i18n-overwrite`.
- [ ] `WithI18nOverwrite` is unit-tested (on appends once, off is a no-op).
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass; the
      `registry`/`commandhl` cross-checks stay green with the new flag.
- [ ] `CHANGELOG.md` `[Unreleased]` gets an `Added` entry.
