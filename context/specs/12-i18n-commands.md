# Unit 12: i18n-commands

## Goal

Add two translation commands that wrap Odoo's CLI i18n flags inside
the project's Odoo container:

- `i18n-export <mod> [lang]` — extract translations for a module into
  `<addons>/<mod>/i18n/<lang>.po` on the host, creating the `i18n/`
  folder when it doesn't exist.
- `i18n-update <mod> [lang]` — import the module's existing
  `<addons>/<mod>/i18n/<lang>.po` back into the configured database
  with `--i18n-overwrite`, replacing the in-DB translations.

`[lang]` is an optional positional that defaults to `es_MX` (matches
the wording in `project-overview.md`). Each command operates on a
**single module per invocation**; if `<mod>` is missing, Echo opens
the same fuzzy picker used by `install`.

## Design

### Why host↔container shuffling is needed

Odoo's CLI writes/reads files **inside the container**. Two options
were considered:

1. Write straight to a bind-mounted addons path. Cheap when the addons
   path is mounted into the container at the same relative path. Echo
   doesn't enforce a mount layout, so it can't assume this.
2. Use `/tmp/<uuid>.po` inside the container and shuffle it across the
   boundary with `docker cp`.

We use **option 2** for both commands. It works regardless of how the
project's compose file mounts (or doesn't mount) the addons path, and
it lets Echo guarantee the destination on the host without relying on
the user's mount layout.

The user-provided `--out <path>` flag overrides the host destination
for `i18n-export` only (useful for ad-hoc exports outside the
module's `i18n/` folder).

### `i18n-export` flow

1. Resolve `<mod>` (positional or fuzzy picker) and `<lang>` (positional
   or default `es_MX`).
2. Resolve the host destination:
   - With `--out <path>`: use the literal path (created parents).
   - Without `--out`: walk the addons paths (`cfg.AddonsPaths`,
     defaulting to the same fallback as `listAvailableModules`) and
     find the directory containing `<mod>/__manifest__.py`. The
     destination is `<that-dir>/<mod>/i18n/<lang>.po`. Create the
     `i18n/` directory if missing. If the module isn't found, return
     `ErrModuleNotFound`.
3. Pick a unique container-side temp path:
   `/tmp/echo-i18n-<unix-nano>-<random>.po`.
4. Run inside the Odoo container:
   ```
   odoo <conn-flags> --modules=<mod> -l <lang> \
        --i18n-export=<tmp> --stop-after-init
   ```
   Streams output through the same `runOdoo` pipeline as `install` /
   `update`, so log-level coloring and `runStats` apply.
5. `docker cp <odoo-container>:<tmp> <host-destination>` to move the
   file out. Use `compose exec ... cat <tmp>` piped to a host file if
   `docker cp` doesn't compose well with the configured compose flavor
   — preferred path is real `docker cp` against the resolved container
   name (`<compose> ps -q <service>` to get the container ID).
6. `compose exec -T <odoo> rm -f <tmp>` to clean up. Errors on cleanup
   are logged as warnings but don't fail the command — the export
   already landed.
7. Print `✓ i18n-export (<mod>, <lang>) → <host-path>` via `finalize`.

### `i18n-update` flow

The mirror image of export:

1. Resolve `<mod>` and `<lang>` (same rules as export).
2. Resolve the host source: `<addons>/<mod>/i18n/<lang>.po`. If the file
   doesn't exist on the host, return `ErrTranslationMissing` (a clear
   "no <lang>.po found in <mod>/i18n/ — run i18n-export first" error).
   `--out` does **not** apply to `i18n-update` (semantics would be
   ambiguous — keep it export-only).
3. **Prod confirm**: when `cfg.Stage == "prod"` and `--force` isn't in
   args, show a red `huh.Confirm`:
   ```
   ⚠  Importing translations into prod database "<db>" with --i18n-overwrite.
      Existing <lang> translations in the DB will be replaced.
   Continue? (y/N)
   ```
   Default `No`. Cancellation → `ErrCancelled`. `dev` and `staging`
   skip the confirm.
4. Pick a unique container-side temp path under `/tmp/`.
5. `docker cp <host-src> <odoo-container>:<tmp>`.
6. Run inside the Odoo container:
   ```
   odoo <conn-flags> --modules=<mod> -l <lang> \
        --i18n-import=<tmp> --i18n-overwrite --stop-after-init
   ```
7. `compose exec -T <odoo> rm -f <tmp>` (best-effort).
8. `finalize` prints `✓ i18n-update (<mod>, <lang>)` or `✗ ...` with
   the error count.

### Underlying primitives needed

- **Container ID resolution** for `docker cp`: a new helper
  `docker.ContainerID(composeCmd, dir, service string) (string, error)`
  that runs `<compose> ps -q <service>` and returns the trimmed first
  line. Fails clearly when the service isn't up.
- **Bidirectional file shuffle**: helpers
  `docker.CopyFromContainer(ctx, container, src, dst string) error`
  and `docker.CopyToContainer(ctx, container, src, dst string) error`
  wrapping `docker cp`. They use the host docker binary (`docker`),
  not `compose` — `docker cp` doesn't have a compose flavor.
- **Cleanup**: a `docker.ExecQuiet(ctx, composeCmd, dir, container,
  argv) error` for the `rm -f /tmp/...` step (same as `Exec` but
  discards output). If `Exec` already accepts a nil/no-op streamer,
  reuse it instead of adding a new helper.

## Implementation

### `internal/odoo/cmd.go` — extend

Add two builders following the existing pattern:

```go
// ExportI18n builds the argv to extract a module's translations to a
// .po file inside the container.
func ExportI18n(c Conn, module, lang, outPath string) Cmd {
    args := append(Cmd{"odoo"}, c.flags()...)
    return append(args,
        "--modules="+module,
        "-l", lang,
        "--i18n-export="+outPath,
        "--stop-after-init",
    )
}

// UpdateI18n builds the argv to import a .po file into the DB with
// --i18n-overwrite.
func UpdateI18n(c Conn, module, lang, inPath string) Cmd {
    args := append(Cmd{"odoo"}, c.flags()...)
    return append(args,
        "--modules="+module,
        "-l", lang,
        "--i18n-import="+inPath,
        "--i18n-overwrite",
        "--stop-after-init",
    )
}
```

### `internal/docker/copy.go` — new file

```go
// ContainerID resolves a compose service to its docker container ID
// via `<compose> ps -q <service>`. Returns the first non-empty line
// trimmed. Empty result → error "service not running".
func ContainerID(ctx context.Context, composeCmd, dir, service string) (string, error)

// CopyFromContainer wraps `docker cp <container>:<src> <dst>`.
func CopyFromContainer(ctx context.Context, container, src, dst string) error

// CopyToContainer wraps `docker cp <src> <container>:<dst>`.
func CopyToContainer(ctx context.Context, container, src, dst string) error
```

All three call `docker` directly (not `<compose>`). Errors include
stderr verbatim.

### `internal/cmd/i18n.go` — new file

```go
type I18nOpts struct {
    Cfg       *config.Config
    Root      string
    Args      []string
    Palette   theme.Palette
    StreamOut func(string)
}

var (
    ErrModuleNotFound      = errors.New("module not found in configured addons paths")
    ErrTranslationMissing  = errors.New("no .po file found for this lang — run i18n-export first")
)

func RunI18nExport(ctx context.Context, opts I18nOpts) error
func RunI18nUpdate(ctx context.Context, opts I18nOpts) error
```

Shared helpers in the same file:

- `parseI18nArgs(args []string) (module, lang, outOverride string, force bool, remainder []string)` — parses
  `--out <path>` (export only), `--force` (update only),
  `<mod> [lang]` positional. Unknown flags return an error.
- `resolveModuleDir(cfg *config.Config, root, mod string) (string, error)` —
  walks addons paths one-deep looking for `<mod>/__manifest__.py`;
  returns the addons directory absolute path (the parent of `<mod>`).
- `defaultExportDest(addonsDir, mod, lang string) string` — returns
  `<addonsDir>/<mod>/i18n/<lang>.po`. Caller is responsible for
  `os.MkdirAll` of the `i18n/` parent.
- `confirmI18nProd(palette, db, lang string) error` — same skeleton as
  `confirmProd` from Unit 10; renders the red warning above.
- `tmpPathInContainer() string` — returns
  `/tmp/echo-i18n-<unix-nano>-<rand4>.po` using `crypto/rand` for the
  suffix. Pure function, doesn't touch the filesystem.

`RunI18nExport` sequence (single function, no further extraction):

1. `requireOdooConfig`.
2. Parse args. If `mod == ""`, run `pickModulesInteractive(opts*, "Module to export")` — translate `I18nOpts` to a minimal `ModulesOpts` view, or extract the picker into a smaller signature.
3. Default `lang = "es_MX"` if empty.
4. Resolve `hostDest`: if `outOverride != ""`, use it; else compute from `resolveModuleDir` + `defaultExportDest`, `MkdirAll` the parent.
5. Generate `containerTmp := tmpPathInContainer()`.
6. `runOdoo(ctx, opts*, odoo.ExportI18n(buildConn(opts*), mod, lang, containerTmp))`.
   Reuse `runStats` + `logColorer` via the REPL wrapper.
7. Resolve container ID via `docker.ContainerID`. `docker.CopyFromContainer(ctx, id, containerTmp, hostDest)`.
8. Best-effort cleanup: `docker.Exec(ctx, ..., []string{"rm", "-f", containerTmp}, noopStream)`. Log a warning on failure but return nil.
9. Stream a final-success message (the `finalize` line is printed by the REPL wrapper).

`RunI18nUpdate` sequence:

1. `requireOdooConfig`.
2. Parse args. Picker fallback for missing `<mod>` like export.
3. Default `lang = "es_MX"`.
4. Resolve `hostSrc` (always module-based — `--out` does not apply).
   `os.Stat` to check existence → `ErrTranslationMissing` on miss.
5. If `cfg.Stage == "prod"` and `!force`, `confirmI18nProd` → may return `ErrCancelled`.
6. Generate `containerTmp`. `docker.CopyToContainer(ctx, id, hostSrc, containerTmp)`.
7. `runOdoo(ctx, opts*, odoo.UpdateI18n(buildConn(opts*), mod, lang, containerTmp))`.
8. Cleanup container tmp (best-effort).

### `internal/repl/repl.go` — dispatch + registry + help

- Add `"i18n-export", "i18n-update"` to `Registry` between the Modules
  group and the Database group (the order influences the Tab
  completion list).
- Add the same two names to `dispatchNames`.
- Add a new `case "i18n-export", "i18n-update":` in `dispatch` that
  calls `sess.runI18n(ctx, cmd, args)`.
- `runI18n` mirrors `runModules`: prints `$ <name> <args>` info line,
  builds an `I18nOpts` with the `logColorer` + `runStats` wrapper, calls
  `RunI18nExport` / `RunI18nUpdate`, and calls `sess.finalize` with a
  summary like `i18n-export (sale, es_MX)`.
- Add a new help section "i18n" between "Modules" and "Database":
  ```
  i18n
    i18n-export <mod> [lang]  Export <mod>/i18n/<lang>.po (default es_MX)
      --out <path>            Write to <path> instead of the module's i18n/
    i18n-update <mod> [lang]  Import the module's <lang>.po into the DB (--i18n-overwrite)
      --force                 Skip the prod-stage confirmation prompt
  ```

Picker reuse: refactor `pickModulesInteractive` to take the bits it
needs (addons paths, root, palette, title) instead of the full
`ModulesOpts` — a smaller signature `pickModuleSingle(cfg, root,
palette, title)` returning `(string, error)`. Update the install/
update/uninstall call sites accordingly. Multi-select stays via
`pickModulesInteractive`; the new single-select wraps
`runSingleFuzzyPicker` (already added in Unit 09).

## Dependencies

None new. Reuses:
- `internal/odoo` (two new builders).
- `internal/docker` (new copy helpers + existing `Exec`).
- `internal/cmd/picker.go` (`runFuzzyPicker`, `runSingleFuzzyPicker`).
- `internal/cmd/init.go` (`ErrCancelled`, `BuildHuhTheme`).
- `huh` (prod confirm).
- `crypto/rand` (temp path suffix).

## Verify when done

- [ ] `i18n-export <mod>` writes `<addons>/<mod>/i18n/es_MX.po` on the host; the file is a valid PO file with the module's strings.
- [ ] `i18n-export <mod> en_US` writes `en_US.po` in the same folder.
- [ ] `i18n-export <mod> --out ./tmp/x.po` writes to `./tmp/x.po` and creates `./tmp/` if missing.
- [ ] `i18n-export` with no positional opens the fuzzy picker (same module list as `install`).
- [ ] `i18n-export <mod>` creates the `<addons>/<mod>/i18n/` directory when it doesn't exist.
- [ ] The container-side temp `/tmp/echo-i18n-*.po` is deleted after the export, regardless of success or failure.
- [ ] `i18n-update <mod>` imports `<addons>/<mod>/i18n/es_MX.po` and the new translations appear in the configured DB.
- [ ] `i18n-update <mod>` returns a clear `ErrTranslationMissing` when the `.po` file isn't present.
- [ ] In `cfg.Stage == "prod"`, both `i18n-update` and the `--force` skip behave like `bash`/`psql`/`shell` (red confirm; `--force` bypass).
- [ ] In `dev`/`staging`, no confirmation is shown.
- [ ] `help` shows the new `i18n` section.
- [ ] Tab autocompletes `i18n-` to `i18n-` (LCP) and a second Tab lists both names.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` all pass.
