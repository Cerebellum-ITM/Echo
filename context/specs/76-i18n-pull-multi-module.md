# Unit 76: `i18n-pull` — pull several modules at once

## Goal

Extend `i18n-pull` (Unit 50) so it can pull the translations of **multiple
modules in a single run**, instead of one module or the all-or-nothing
`--all`. `i18n-pull sale account purchase es_MX` pulls the three named
modules' `es_MX.po` from the remote in one go; running `i18n-pull` with no
module args opens a **multi-select** picker. The single-module and `--all`
behaviors stay exactly as they are.

```
i18n-pull sale account es_MX     # pull es_MX for sale AND account
i18n-pull sale account           # both modules, default lang (es_MX)
i18n-pull sale --from prod       # single module, unchanged
i18n-pull --all fr_FR            # every remote module, unchanged
i18n-pull                        # multi-select picker (was single-select)
i18n-pull sale account --lang fr_FR   # explicit language, all positionals are modules
```

## Design

**Positionals become a module list.** The parsed `module string` becomes
`modules []string`. The only real design question is telling the trailing
**language** positional apart from module names, because `i18n-pull sale
es_MX` must keep meaning "module `sale`, lang `es_MX`" while `i18n-pull
sale account` must mean "two modules, default lang".

Resolution, in order:

1. **`--lang <code>` / `--lang=<code>` (explicit, unambiguous).** When
   present, **every** positional is a module and `<code>` is the language.
   This is the escape hatch for the rare case where a module name collides
   with a locale shape.
2. **Trailing-locale heuristic (backward-compatible default).** With **two
   or more** positionals and no `--lang`, if the **last** positional matches
   the locale shape `^[a-z]{2,3}(_[A-Z]{2})?(@[a-z]+)?$` (e.g. `es`,
   `es_MX`, `pt_BR`, `sr@latin`), it is taken as the language and the rest
   are modules. If the last positional is not locale-shaped (e.g.
   `account`), **all** positionals are modules and the language defaults to
   `es_MX`.
3. **A single positional stays a module** (unchanged from Unit 50): `i18n-pull
   sale` → module `sale`, default lang — even if it were locale-shaped, to
   avoid changing today's behavior.

`--all` is unchanged: it still takes only an optional language positional and
`--all` + explicit modules is an error.

**Batch semantics.** A run covering more than one module (`--all`, several
positionals, or a multi-pick) is a **batch**: a module whose remote export
fails is logged `WARNING … skipped` and the run continues, closing with the
`pull complete pulled=N skipped=M` summary — exactly how `--all` already
behaves. A single-module run still surfaces the error (fail-fast). Concretely
`batch := p.all || len(modules) > 1`, replacing the `p.all` checks inside the
per-module loop. Empty-`.po` skips (no translations for the lang) are
unchanged — they already skip rather than clobber.

**Empty-args picker becomes multi-select.** When no modules are given (and
not `--all`), the picker switches from `runSingleFuzzyPickerStaged` to the
shared multi-select `runFuzzyPickerCore` (the same picker `install`/`update`/
`test` use, stage-colored by the remote profile's stage). Selecting several
modules pulls them all; selecting one behaves like the single case; Esc or an
empty selection returns `ErrCancelled`. This is the interactive half of
"varios módulos a la vez".

**Everything downstream is per-module already.** `pullRemotePO`, `pullDest`,
the `MkdirAll`/`WriteFile`, and the `exporting`/`pulled`/`skipped` log lines
are all inside the existing `for _, mod := range modules` loop — they need no
change beyond the loop now receiving more entries. One language per run
(unchanged): all modules are pulled for the same `p.lang`.

**Build mode unchanged.** `i18n-pull --build` keeps composing a single-module
line; extending its positional picker to multi-select is out of scope for
this unit.

## Implementation

### `internal/cmd/i18n_pull.go`

- `i18nPullArgs`: replace `module string` with `modules []string`; add
  `lang` already exists. No new bool needed for `--lang` (its presence is
  captured by setting `lang` + a `langSet` local in the parser).
- `parseI18nPullArgs(args)`:
  - Parse `--lang <v>` / `--lang=<v>` alongside the existing `--from` /
    `--all` / `--installed`; track `langSet bool`.
  - `--all` branch unchanged (optional single language positional; error on
    extra positionals). Honor `--lang` there too (if set, no language
    positional is allowed).
  - Non-`--all` branch:
    - If `langSet`: `out.modules = positional` (all of them), lang already
      set.
    - Else if `len(positional) >= 2` and `isLocale(positional[last])`:
      `out.lang = last`, `out.modules = positional[:last]`.
    - Else: `out.modules = positional` (may be empty → picker), default lang.
  - Add `isLocale(s string) bool` using the regex above (compile once at
    package scope with `regexp.MustCompile`).
- `RunI18nPull`: the module-selection `switch`:
  - `case p.all:` unchanged (list remote → all).
  - `case len(p.modules) > 0:` → `modules = p.modules`.
  - `default:` (empty) → `runFuzzyPickerCore("Modules to pull translations
    for", avail, nil, nil, nil, opts.Palette, prof.Stage)`; on `canceled ||
    len(picked) == 0` return `ErrCancelled`; else `modules = picked`.
  - After the switch: `batch := p.all || len(modules) > 1`.
  - In the loop, replace the two `if p.all` conditions with `if batch`; the
    trailing summary condition becomes `if batch || skipped > 0`.

### `internal/repl/repl.go` — help

Update the `i18n-pull` entries under the i18n section:

```go
{"i18n-pull [<mod>...] [lang]", "Pull one or more modules' <lang>.po from a remote into the repo"},
{"  --from <target>", "Use a named connect target (default: project's [connect])"},
{"  --lang <code>", "Language to pull (default es_MX); makes every positional a module"},
{"  --all", "Pull every candidate module"},
{"  --installed", "List candidates from the DB (all installed), not just the project's addons"},
```

### `internal/repl/commands.go` — flags

```go
"i18n-pull":     {"--from", "--lang", "--all", "--installed"},
```

### Tests

`internal/cmd/i18n_pull_test.go` (extend the existing `parseI18nPullArgs`
table, or add one):

- `i18n-pull sale account es_MX` → modules=`[sale account]`, lang=`es_MX`.
- `i18n-pull sale account` → modules=`[sale account]`, lang=`es_MX`.
- `i18n-pull sale es_MX` → modules=`[sale]`, lang=`es_MX` (unchanged).
- `i18n-pull sale` → modules=`[sale]`, lang=`es_MX` (unchanged).
- `i18n-pull sale account --lang fr_FR` → modules=`[sale account]`,
  lang=`fr_FR`.
- `i18n-pull sale es_MX account --lang pt_BR` → modules=`[sale es_MX
  account]`, lang=`pt_BR` (explicit flag wins; `es_MX` is a module here).
- `i18n-pull --all fr_FR` → all=true, lang=`fr_FR`, modules empty (a single
  positional under `--all` is the language — unchanged from Unit 50).
- `i18n-pull --all sale fr_FR` → error (two positionals under `--all`).
- `i18n-pull` → modules empty, all=false (picker path).
- `isLocale`: `es`, `es_MX`, `pt_BR`, `sr@latin` true; `sale`, `account`,
  `Sale`, `account_move` false.

## Dependencies

- `regexp` (stdlib) — new import in `i18n_pull.go` for `isLocale`.

## Verify when done

- [ ] `i18n-pull sale account es_MX` pulls both modules' `es_MX.po` in one
      run; a failing module is skipped with a `WARNING`, the run continues,
      and it closes with `pull complete pulled=N skipped=M`.
- [ ] `i18n-pull sale account` pulls both with default lang `es_MX`.
- [ ] `i18n-pull sale es_MX` and `i18n-pull sale` behave exactly as before
      (single module; single-module errors still surface).
- [ ] `i18n-pull sale account --lang fr_FR` treats all positionals as
      modules and pulls `fr_FR`.
- [ ] `i18n-pull` (no args) opens a **multi-select** picker; picking several
      pulls all of them; Esc / empty selection cancels cleanly.
- [ ] `--all` and `--installed` behave exactly as before.
- [ ] `go build ./...`, `go vet ./...`, and
      `go test ./internal/cmd/... ./internal/repl/...` all pass.
