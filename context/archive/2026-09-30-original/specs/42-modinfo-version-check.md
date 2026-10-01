# Unit 42: `modinfo` — installed vs manifest version check

## Goal

A new read-only command `modinfo [<mod>]` that, for a single module, shows
the version Odoo recorded as installed in the database
(`ir_module_module.latest_version` + `state`) side by side with the version
declared in the module's `__manifest__.py`, and prints a one-line verdict
(`in sync` / `update pending` / `not installed` / `db ahead`). When no
module name is given, an interactive single-select picker chooses one. The
manifest version is normalized the way Odoo's `adapt_version` does before
comparing, so a short manifest version (`1.3.0`) is matched correctly
against the DB's adapted form (`17.0.1.3.0`).

Mirrors the SQL the developer runs by hand today:

```sql
SELECT name, latest_version, state
FROM ir_module_module
WHERE name = 'sale_goals_management';
```

## Design

`modinfo` is an inspection command, not a lifecycle one — it never runs
`odoo`, only one `psql` query plus one manifest read. It lives in the
`cmd` layer alongside the module commands because it reuses their plumbing
(`ModulesOpts`-style config + `.env`, `resolveModules`, `readContainerFile`,
the single-select picker). It is one-shot eligible (no TTY needed once a
module is named), so it composes with script mode (Unit 31) and recipes —
useful in CI to assert a module is in sync before/after an update.

Output is rendered as Odoo-style log lines (logger `echo.modinfo`), in
keeping with the rest of Echo's output — never an ASCII table. The verdict
drives the log level so the status reads at a glance:

- `in sync` → INFO
- `update pending` (manifest > DB) → WARNING
- `db ahead` (DB > manifest, a downgrade) → WARNING
- `not installed` / no row in `ir_module_module` → WARNING
- query/manifest read failure → ERROR (framed by `finalize`)

A `--copy` flag copies the rendered report to the clipboard (reusing
`internal/clipboard`), matching `report`/`copy-last`.

### Version normalization (Odoo `adapt_version`)

Odoo stores `latest_version` already adapted: it prepends the major series
(e.g. `17.0`) to any manifest version that doesn't already start with it.
Reproduce that so the comparison is apples-to-apples:

```
serie  = "<odoo_version>.0"          // e.g. "17" → "17.0"
adapt(v):
    if v == serie or not v.startswith(serie + "."):
        v = serie + "." + v
    return v
```

The project's series comes from `cfg.OdooVersion`. If it is empty, skip
normalization and compare the raw strings (best-effort), noting the
limitation.

Comparison is segment-wise numeric (`strings.Split` on `.`, compare each
part as int, missing parts treated as 0, non-numeric parts fall back to
string compare for that segment). `>`, `<`, `==` map to the verdicts above.

## Implementation

### `docker.ModuleVersion` (`internal/docker/postgres.go`)

New helper that runs the registered-version query and returns the three
columns, or `found=false` when the module has no row:

```go
// ModuleVersion returns the name, latest_version and state recorded in
// ir_module_module for a module, or found=false when there is no row.
func ModuleVersion(ctx context.Context, composeCmd, dir, dbContainer, user, db, module string) (name, version, state string, found bool, err error) {
    q := fmt.Sprintf(
        "SELECT name, latest_version, state FROM ir_module_module WHERE name = '%s'",
        strings.ReplaceAll(module, "'", "''"))
    out, err := psqlScalar(ctx, composeCmd, dir, dbContainer, user, db, q)
    if err != nil {
        return "", "", "", false, err
    }
    out = strings.TrimSpace(out)
    if out == "" {
        return "", "", "", false, nil
    }
    // psql -At joins columns with '|'.
    parts := strings.SplitN(out, "|", 3)
    if len(parts) < 3 {
        return "", "", "", false, nil
    }
    return parts[0], parts[1], parts[2], true, nil
}
```

`latest_version` may be NULL for a never-installed module — `-At` renders
NULL as an empty field, so `version == ""` is treated as "no recorded
version" in the verdict.

### `internal/cmd/modinfo.go` (new)

```go
type ModinfoOpts struct {
    Cfg       *config.Config
    Root      string
    Args      []string
    Palette   theme.Palette
    Interactive bool
}

type ModinfoResult struct {
    Module    string
    DBVersion string   // raw latest_version, "" if none
    DBState   string   // ir_module_module.state, "" if no row
    DBFound   bool
    Manifest  string   // raw manifest version, "" if absent
    Adapted   string   // normalized manifest version compared against DB
    Status    string   // "in sync" | "update pending" | "db ahead" | "not installed" | "no version"
    Copy      bool
}
```

`RunModinfo(ctx, opts) (ModinfoResult, error)`:

1. Validate `cfg`: require `OdooContainer`, `DBContainer`, `DBName`
   (reuse the existing `ErrNoOdooContainer` / `ErrNoDB`). Parse `--copy`
   out of `opts.Args`; the first remaining positional is the module name.
2. Resolve the module name: if none given, call the single-select picker
   over `resolveModules` (reuse `pickModuleInteractive` — a single-select
   sibling of `pickModulesInteractive`, or `cmd.PickOne` with the resolved
   names). Non-TTY with no module → `ErrNonInteractive` (fails closed).
3. DB side: `user := env.Load(opts.Root)["POSTGRES_USER"]`, then
   `docker.ModuleVersion(ctx, cfg.ComposeCmd, opts.Root, cfg.DBContainer, user, cfg.DBName, module)`.
4. Manifest side: `manifestVersion(ctx, opts, module)` (below) → raw
   version string (may be "").
5. Normalize + compare → `Status`. Build and return the `ModinfoResult`.

#### Locating + reading the manifest (`modinfo.go`)

A module-directory locator that honors the addons mode, returning the
manifest text:

```go
// moduleManifest returns the __manifest__.py text for a module, reading
// from the host addons paths in host mode, or from inside the Odoo
// container in conf mode (matching resolveModules' source of truth).
func moduleManifest(ctx context.Context, opts ModinfoOpts, module string) (string, error)
```

- conf mode (`cfg.AddonsMode == addonsModeConf`): for each path in
  `cfg.AddonsPaths`, try `readContainerFile(<path>/<module>/__manifest__.py)`;
  first that succeeds wins. (Re-use the same `docker.Exec cat` helper —
  factor `readContainerFile` to accept a `ModinfoOpts`-compatible signature,
  or pass the raw fields: compose cmd, root, odoo container, path.)
- host mode: for each path in `cfg.AddonsPaths` (or the `.`/`addons`/`custom`
  defaults), `os.ReadFile(filepath.Join(root, path, module, "__manifest__.py"))`;
  first hit wins.
- none found → return `("", nil)` (manifest absent → `Status` reflects it).

#### Parsing the manifest version (`modinfo.go`)

The manifest is a Python dict literal; a focused regex avoids a Python
parser:

```go
var manifestVersionRe = regexp.MustCompile(
    `['"]version['"]\s*:\s*['"]([^'"]+)['"]`)

func manifestVersion(text string) string {
    m := manifestVersionRe.FindStringSubmatch(text)
    if len(m) == 2 {
        return strings.TrimSpace(m[1])
    }
    return ""
}
```

#### Normalization + compare (`modinfo.go`)

```go
func adaptVersion(version, serie string) string // Odoo adapt_version
func compareVersions(a, b string) int           // -1 / 0 / 1, segment-wise
func verdict(r ModinfoResult) string            // maps to Status strings
```

`serie` from `cfg.OdooVersion` (`""` → skip adaptation, compare raw).

Verdict rules:

- no DB row (`!DBFound`) **or** `DBState != "installed"` → `not installed`
  (carry the actual state into the output).
- `DBVersion == "" || Manifest == ""` → `no version` (can't compare).
- `compareVersions(Adapted, DBVersion)`: `0` → `in sync`, `>0` →
  `update pending`, `<0` → `db ahead`.

### `--last` (session-only)

`modinfo --last` replays the last module inspected **in this session**,
skipping the picker — so a result first reached interactively can be copied
(`modinfo --last --copy`). The target lives only in memory
(`session.lastModinfoModule`), never persisted to disk (unlike `update
--last`). The REPL strips `--last` (shared `stripFlag` helper), and when set
prepends the stored module to the args before calling `cmd.RunModinfo`; an
empty store warns `no previous modinfo this session` (exit 2). Every
successful `modinfo` updates the stored module. `--last` is added to
`commandFlags["modinfo"]` and gets a help sub-row.

### REPL wiring (`internal/repl/`)

- `modinfo.go` (new) `runModinfo(sess, args)`: builds `ModinfoOpts`
  (Interactive from the session), calls `cmd.RunModinfo`, and on success
  emits a single Odoo-style summary line via `emitOdooLog`:

  ```
  echo.modinfo  module=sale_goals_management db=17.0.1.2.0 state=installed manifest=17.0.1.3.0 status="update pending"
  ```

  Level chosen from `Status` (in sync → INFO, update pending / db ahead /
  not installed → WARNING). On `--copy`, render the same line plain and
  push it through `internal/clipboard.WriteAll`, then log `copied=true`.
  Errors route to `finalize` (ERROR line), same as the module commands.
  `ErrNonInteractive` is mapped to exit 2 in one-shot mode by the existing
  guard plumbing.
- `commands.go` `Registry`: add `"modinfo"`. `commandFlags["modinfo"] =
  []string{"--copy"}`.
- `repl.go` `dispatch` / `dispatchNames`: route `modinfo` to `runModinfo`.
  Add to `helpSections()` Modules section:
  `{"modinfo [<mod>]", "Compare DB-installed version vs manifest version"}`
  and a `{"  --copy", "Copy the report to the clipboard"}` sub-row.
- `IsScriptCommand` picks it up automatically from `dispatchNames` (it's
  read-only, one-shot eligible).

### Tests

- `internal/cmd/modinfo_test.go`:
  - `manifestVersion`: extracts from single- and double-quoted dicts;
    returns `""` when absent.
  - `adaptVersion`: `("1.3.0", "17.0") → "17.0.1.3.0"`,
    `("17.0.1.3.0", "17.0") → "17.0.1.3.0"`, `("17.0", "17.0") →
    "17.0.17.0"` (Odoo's literal behavior), empty serie → unchanged.
  - `compareVersions`: equal, greater, lesser, differing lengths
    (`17.0.1` vs `17.0.1.0` → equal), non-numeric segment fallback.
  - `verdict`: table over DBFound/state/versions → each Status string.
- `internal/repl/commandhl_test.go` `commandFlags`↔`Registry` guard and
  `registry_test.go` help/Registry cross-check stay green (add `modinfo`).

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: `modinfo [<mod>]` — compare the
  DB-installed module version against its manifest.
- `context/progress-tracker.md` → mark Unit 42 done with a session note.

## Dependencies

None new. Reuses `psql` (via `psqlScalar`), `docker.Exec`, the
single-select picker, `internal/clipboard`, and `internal/env`.

## Verify when done

- [ ] `modinfo sale_goals_management` prints `module / db / state /
      manifest / status` and the status matches reality (install, then it
      reads `in sync`; bump the manifest version, it reads `update
      pending`).
- [ ] A short manifest version (`1.3.0`) is correctly compared against the
      DB's adapted `17.0.1.3.0` (no false `update pending`).
- [ ] A module not present in `ir_module_module` reads `not installed`.
- [ ] `modinfo` with no argument opens the single-select picker; cancelling
      it warns (not ✗); in one-shot/non-TTY with no module it fails closed
      (exit 2), it does not hang.
- [ ] conf mode reads the manifest from inside the container; host mode
      reads it from disk — both resolve the same module.
- [ ] `--copy` puts the report on the clipboard and logs `copied=true`.
- [ ] `echo modinfo sale_goals_management` works one-shot and exits 0.
- [ ] `modinfo` highlights as a known command, `--copy` as a known flag.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
