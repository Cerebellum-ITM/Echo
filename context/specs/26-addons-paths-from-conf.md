# Unit 26: Addons paths from the instance's `odoo.conf`

## Goal

Let `modules` / `install` / `update` / `uninstall` / `test` discover the
available modules from the **Odoo instance's own `odoo.conf`** (read
inside the container) when the host-side addons-path scan finds nothing.
Echo currently only scans folders next to `docker-compose.yml` on the
**host** (`modules --config`), so an instance whose addons live inside
the container — declared via `addons_path` in `/etc/odoo/odoo.conf` —
yields `no modules found — configure addons paths with` modules --config``
even though the modules exist. This unit reads `addons_path` from the
container's conf, lists modules inside the container, and persists the
result so it survives across sessions, **without breaking** the existing
host scan.

Reported symptom:

```
echo.update.error: update failed err="no modules found — configure addons paths with `modules --config`"
```

## Design

Two **addons modes**, recorded per project:

- `host` (current, default): `addons_paths` are folders relative to the
  project root, scanned on the host with `os.ReadDir` + `__manifest__.py`.
- `conf`: `addons_paths` are **absolute container paths** taken from
  `addons_path` in the instance's `odoo.conf`; module listing runs
  **inside the Odoo container** (`docker exec`), not on the host.

**Trigger = automatic fallback.** When a module-listing operation in
`host` mode yields zero modules and the user has not explicitly pinned
host paths, Echo reads `odoo.conf` from the container, parses
`addons_path`, lists the modules in those container dirs, and — if it
finds any — switches the project to `conf` mode and persists it. The next
run goes straight to `conf` mode.

**Source of truth stays live.** In `conf` mode Echo re-reads `odoo.conf`
from the container on every listing (one `cat`), re-lists, and refreshes
the saved paths if they changed. That is the "se puede actualizar": the
list always reflects the instance, with no extra command. The persisted
`addons_paths` + `addons_mode` are the durable record (so `modules`
read-only output and future sessions know the mode without a probe).

**Coexistence / escape hatch.** `modules --config` (the host folder
picker) is unchanged and always sets `addons_mode = host`, overwriting
the conf-derived paths. So the user can always force back to host mode
explicitly. The conf path itself is configurable per instance
(`conf_path`, default `/etc/odoo/odoo.conf`).

## Implementation

### Config (`internal/config`)

- `Config` gains `AddonsMode string` and `ConfPath string`;
  `projectFile` gains `addons_mode` and `conf_path`.
- `applyDefaults`: `ConfPath` defaults to `/etc/odoo/odoo.conf` when
  empty; `AddonsMode` empty is treated as `host` (no default written
  until the fallback flips it to `conf`). Wire through `Load` /
  `SaveProject` and add to `Defaults`.

### Conf reading + parsing (`internal/cmd/modules.go`)

- `readContainerFile(ctx, opts, path) (string, error)` — runs
  `docker.Exec(... OdooContainer, []string{"cat", path}, collect)`,
  accumulating streamed lines into a single string. Reuses the existing
  `docker.Exec` helper (no compose change). A non-zero exit (missing
  file) returns an error so the caller can fall through quietly.
- `parseAddonsPath(conf string) []string` — **pure, table-tested**.
  Scans the INI text for a line whose trimmed form starts with
  `addons_path` (ignores section headers/comments `#`/`;`), splits the
  value on `=` once, splits the RHS on `,`, trims each entry, drops
  empties. Returns absolute container paths in declared order. Entries
  whose base name is `enterprise` (case-insensitive, trailing-slash
  tolerant via `filepath.Base`) are **dropped by default** — the
  Enterprise addons are noise in the update/install picker.

### Module listing inside the container (`internal/cmd/modules.go`)

- `listModulesInContainer(ctx, opts, paths []string) ([]string, error)`
  — one `docker exec` shell pass over the parsed paths:

  ```sh
  sh -c 'for d in "$@"; do
           for m in "$d"/*/__manifest__.py; do
             [ -f "$m" ] && basename "$(dirname "$m")"
           done
         done' _ <path1> <path2> ...
  ```

  Each stdout line is a module name; Go dedups + `sort.Strings`. Mirrors
  the host scan's "dir containing `__manifest__.py`" rule, one level deep.

### Resolver (`internal/cmd/modules.go`)

Introduce the single entry point that replaces the direct
`listAvailableModules(cfg, root)` calls:

```go
func resolveModules(ctx context.Context, opts ModulesOpts) ([]string, error)
```

Behavior:

1. `addons_mode == "conf"` → read `conf_path` from the container, parse,
   list in container. If the parsed paths differ from `cfg.AddonsPaths`,
   refresh them and `SaveProject` (auto-update). Return the modules.
2. Otherwise (`host`): `host := listAvailableModules(cfg, root)`
   (unchanged). If `len(host) > 0`, return it.
3. **Fallback** (host empty): read `conf_path` from the container, parse,
   list in container. If modules found, set `cfg.AddonsMode = "conf"`,
   `cfg.AddonsPaths = <parsed paths>`, `SaveProject`, and return them.
   If the conf can't be read or yields nothing, return the empty host
   result so the existing `ErrNoModulesAvailable` / "(no modules found…)"
   path is preserved verbatim.

`listAvailableModules` stays as the host-only helper (no signature
change). `pickModulesInteractive` gains `ctx` and calls `resolveModules`;
its callers (`RunInstall/RunUpdate/RunUninstall/RunTest`) already hold
`ctx`. `RunModules` calls `resolveModules` too; its `--config` branch is
untouched (still host picker, still sets host mode).

When the fallback flips to conf mode, emit one Odoo-style info line via
`StreamOut` so the user sees what happened, e.g.
`(addons paths read from /etc/odoo/odoo.conf — 3 paths, N modules)`.

### Picker scrolling (`internal/cmd/picker.go`)

Surfaced by this unit (a full conf-derived catalog can be hundreds of
modules), the shared `fuzzyPicker` gains a scrolling viewport so long
lists no longer overflow the terminal and hide rows. It tracks `offset`
+ `height` (from `tea.WindowSizeMsg`), renders only the `[offset,
offset+maxRows)` window (`maxRows = height - chrome`, default 15 before
the first size msg), keeps the cursor in view via `clampScroll`, adds
`pgup` / `pgdn` paging, and shows `↑ N more` / `↓ N more` hints. Benefits
every picker (modules, db-restore, connect, i18n).

### REPL (`internal/repl`)

No new command. `runModules` already threads `ctx` into the `cmd.Run*`
functions, so conf-mode resolution is transparent. `Registry` / help /
dispatch unchanged (no new verb or flag).

## Dependencies

None new. Uses `docker.Exec` (Docker CLI via compose, already required)
and the existing config TOML layer.

## Verify when done

- [ ] On an instance whose addons live only inside the container, a bare
      `update` (host scan empty) reads `/etc/odoo/odoo.conf`, lists the
      modules from its `addons_path`, and opens the picker instead of
      failing with `no modules found`.
- [ ] After that first run the project TOML has `addons_mode = "conf"`
      and `addons_paths` = the container dirs; the next `modules` lists
      them without re-probing host folders.
- [ ] Editing `addons_path` in the container's `odoo.conf` is reflected
      on the next `modules` run (live re-read refreshes saved paths).
- [ ] `modules --config` still scans host folders, saves them, and resets
      `addons_mode` to `host` — conf mode does not hijack the picker.
- [ ] A project with valid host addons paths keeps using the host scan
      (no container probe, behavior identical to before this unit).
- [ ] `conf_path` is configurable per project and defaults to
      `/etc/odoo/odoo.conf`.
- [ ] `parseAddonsPath` is covered by table tests (single path, multiple
      comma-separated, spaces, commented line, missing key, section
      header present, `enterprise` entry skipped).
- [ ] An `addons_path` entry named `enterprise` (any case, with or without
      a trailing slash) is excluded from the discovered modules.
- [ ] A module list longer than the terminal height scrolls in the picker
      (window follows the cursor, `pgup`/`pgdn` page, `↑/↓ N more` hints)
      instead of overflowing the screen.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
