# Unit 47: `modstate` — module state dump (table / JSON)

## Goal

A new read-only command `modstate [--all] [--json]` that queries
`ir_module_module` for the active project's database and reports every
module's name, state and version. By default it lists only installed
modules (`state = 'installed'`); `--all` includes every row regardless of
state. Two output modes:

- **human (default):** an aligned `name | state | version` table.
- **`--json`:** a single JSON array on **stdout only** — no ANSI, no Echo
  log lines — one object per module:

```json
[{"name":"real_estate_bits","state":"installed","version":"17.0.1.2.3"}, ...]
```

Fields: `name` (`ir_module_module.name`), `state`
(`installed|to upgrade|to install|uninstalled|uninstallable|…`), `version`
(`latest_version`, JSON `null` when the column is NULL).

The command is headless (no TTY, no picker), one-shot eligible, and runs
with `-C` from outside the project — just like `update`/`install`.

Mirrors the SQL the developer runs by hand today:

```sql
SELECT name, state, latest_version
FROM ir_module_module
WHERE state = 'installed'
ORDER BY name;
```

## Design

`modstate` is the bulk sibling of `modinfo` (Unit 42): where `modinfo`
inspects one module and compares it against its manifest, `modstate` dumps
the raw DB rows for many modules and does **no** manifest comparison. It
reuses the same plumbing layer — `db_container`/`db_name` from the
per-project config, `POSTGRES_USER` from `.env`, a single `psql` query via
the `docker` package — and adds no new dependency.

It lives in the `cmd` layer (`internal/cmd/modstate.go`) next to the module
commands, with a thin REPL wrapper (`internal/repl/modstate.go`) that owns
rendering and exit codes.

### Output routing — the new concern

Until now **every** Echo line (including logs) is printed to stdout via
`fmt.Println` (`session.print` / `emitOdooLog`). `--json` is the first
output contract that needs a *clean* stdout. Rules, decided with the user:

- **`--json`, success:** stdout receives **only** the marshaled JSON array
  (a single `json.Marshal` + one trailing newline, written straight to
  `os.Stdout` — bypassing `session.print` so no theme/ANSI touches it). **No
  start line, no `finalize` ✓ line, no log line** is emitted on success.
- **`--json`, error:** nothing goes to stdout; the error is written to
  **stderr** (an `echo.modstate` ERROR line via the normal renderer, which
  in `--json` mode is redirected to `os.Stderr`) and the process exits
  non-zero. Partial/garbage JSON must never reach stdout.
- **human mode:** unchanged from the rest of Echo — table + `finalize`
  frame stream to stdout through `session.print`.

Because the existing renderer hard-codes `os.Stdout`, the JSON path does
**not** go through it at all. The command returns structured data to the
REPL wrapper, and the wrapper chooses: marshal-to-stdout, or render-table.
Any diagnostic in JSON mode is emitted with an explicit `io.Writer`
(`os.Stderr`) — see Implementation.

### Default filter

Per the user's decision, the **default is installed-only** in both modes
(table and JSON), and `--all` widens to every row. This is uniform across
modes so the JSON contract never depends on the output flag. (Note: this
inverts the first-draft sketch where the default returned all rows and
`--installed` filtered — `--installed` is **not** added; `--all` is the
single filter flag.)

### Human table

`name | state | version` with a header row, columns left-aligned and
padded to the widest cell, rendered through the active theme:

- header row in `palette.Accent` (bold), matching how `keyColor` treats
  field keys elsewhere;
- `state` colored by a small map — `installed` → `palette.Success`/ok,
  `to upgrade`/`to install` → `palette.Info`, `uninstalled`/
  `uninstallable` → `Dim`, anything else → default;
- a NULL `latest_version` shows as `-` (dim) in the table (only the JSON
  uses real `null`);
- empty result → a single INFO line `no modules` (human) / `[]` (JSON).

Each rendered table line goes through `session.print` (so it is captured
for `report`/`copy-last` and respects the theme), but the table is built as
plain strings first. This is the one place Echo emits a multi-column table
rather than log lines; justified because `--json` already establishes
`modstate` as a data-dump command, and a `name | state | version` grid is
what the user asked for.

### Exit codes

Consistent with the rest of Echo's one-shot contract (Invariant 10):

- `0` — query ran, rows (or empty set) emitted;
- `1` — DB / execution error (query failed, marshal failed);
- `2` — usage (unknown flag) / project-not-configured (no
  `db_container`/`db_name`). No TTY is ever required, so there is no `3`
  path here.

## Implementation

### `internal/docker/postgres.go` — `ModuleStates`

New helper alongside `ModuleVersion`:

```go
// ModuleStates returns name/state/latest_version for modules in
// ir_module_module, ordered by name. When installedOnly is true the query
// filters to state = 'installed'. A NULL latest_version yields version=""
// with versionNull=true so callers can emit JSON null vs the empty string.
func ModuleStates(ctx context.Context, composeCmd, dir, dbContainer, user, db string, installedOnly bool) ([]ModuleStateRow, error)
```

- `ModuleStateRow{ Name, State, Version string; VersionNull bool }`.
- Query: `SELECT name, state, COALESCE(latest_version, '') , (latest_version IS NULL) FROM ir_module_module [WHERE state='installed'] ORDER BY name`.
  Use `psql -At` (pipe-separated). Because a value could in theory contain
  `|`, split each line with `SplitN(line, "|", 4)` on the 4 selected
  columns (name, state, version, null-flag) — module names never contain
  `|`, and selecting the null-flag as the **last** column keeps the version
  split unambiguous.
- Parse the `t`/`f` null-flag into `VersionNull`. Empty `out` → empty slice,
  not an error.

### `internal/cmd/modstate.go`

```go
type ModstateOpts struct {
	Cfg  *config.Config
	Root string
	Args []string
}

type ModstateResult struct {
	Rows []ModuleStateRow // docker.ModuleStateRow re-exported or aliased
	JSON bool
	All  bool
}
```

`RunModstate(ctx, opts) (ModstateResult, error)`:

1. Guard config: `Cfg.DBName == "" || Cfg.DBContainer == ""` →
   `ErrNoDB` (mapped to exit 2 by the wrapper). No Odoo container is
   needed — this is a pure psql command.
2. Parse args: `--json` and `--all` only; any other `-…` token →
   `fmt.Errorf("unknown flag: %s", a)` (exit 2). No positional args.
3. `user := env.Load(opts.Root)["POSTGRES_USER"]`.
4. `rows, err := docker.ModuleStates(ctx, cfg.ComposeCmd, root, cfg.DBContainer, user, cfg.DBName, !all)`
   — `installedOnly = !all`.
5. Return `ModstateResult{Rows: rows, JSON: json, All: all}`; wrap query
   errors with `fmt.Errorf("query ir_module_module: %w", err)`.

`RunModstate` performs **no** rendering — it only fetches. Rendering and
output routing belong to the REPL wrapper so the table can reuse the
session theme.

### `internal/repl/modstate.go`

`func (sess *session) runModstate(ctx context.Context, args []string)`:

1. `res, err := cmd.RunModstate(...)`.
2. On error: if `res.JSON` (parse the flag locally before calling, or have
   `RunModstate` echo it back even on error — simplest: detect `--json` in
   `args` up front), write the error line to **stderr** via a stderr
   variant of `emitOdooLog` and set `exitCode` (`ErrNoDB`/unknown-flag → 2,
   else 1). In human mode route through `finalize("modstate", …)` as usual.
3. On success, `--json`:
   - Build `[]map[string]any` (or a typed struct with `omitempty`-free
     fields) where `version` is `nil` when `VersionNull`, else the string.
     Field order in the struct: `name`, `state`, `version`.
   - `b, err := json.Marshal(payload)`; marshal failure → stderr ERROR,
     exit 1, **nothing on stdout**.
   - `os.Stdout.Write(b); os.Stdout.Write([]byte("\n"))`. Do **not** call
     `session.print`. Empty slice marshals to `[]` — correct.
   - `exitCode = exitOK`. Emit no log line.
4. On success, human table:
   - If `len(Rows) == 0` → `emitOdooLog("INFO", "echo.modstate", "no modules", …)`, `exitOK`.
   - Else build the aligned `name | state | version` table (header +
     rows, per Design) and emit each line via `sess.print(Line{Kind:"out", Text: …})`
     so the theme + capture apply; color `state` and the header as Design
     specifies. Close with `sess.finalize("modstate", 0, 0, nil)` for the
     ✓ frame (consistent with other read commands? — match `modinfo`,
     which does **not** call finalize on success; instead end with a count
     line `emitOdooLog("INFO","echo.modstate","modules listed", {count, scope=installed|all})`).
   - `exitCode = exitOK`.

**stderr routing.** Add a minimal seam rather than refactoring every
renderer: a helper `emitOdooLogTo(w io.Writer, level, logger, msg, fields, …)`
that the existing `emitOdooLog` delegates to with `os.Stdout`. The JSON
error path calls it with `os.Stderr`. Keep the change surgical — only
`modstate`'s JSON branch uses the stderr seam in this unit.

### Wiring

- `Registry`: add `"modstate"` (Modules group, after `view`).
- `commandFlags["modstate"] = {"--all", "--json"}`.
- `dispatchNames` + `dispatchParsed`/`dispatch` switch → `sess.runModstate`.
- `helpSections` (Modules): `modstate [--all] [--json]` — "list module
  states from the DB".
- One-shot: `IsScriptCommand("modstate")` is true automatically via
  `dispatchNames`, so `echo modstate --json` (and `echo -C <dir> modstate
  --json`) works headless. Confirm the `registry_test`/`commandhl_test`
  cross-checks stay green.

## Dependencies

- none (stdlib `encoding/json`, `os`, `io`; reuses `internal/docker`,
  `internal/env`, `internal/config`).

## Verify when done

- [ ] `echo modstate` (human) lists only installed modules as an aligned
      `name | state | version` table; `--all` includes uninstalled/other
      states.
- [ ] `echo modstate --json` prints **only** a JSON array to stdout (verify
      `echo modstate --json | jq .` succeeds and `2>/dev/null` changes
      nothing on success); a NULL `latest_version` serializes as `null`.
- [ ] In `--json` mode, no Odoo-style log line or `finalize` ✓ reaches
      stdout on success; on error stdout is empty and the message is on
      stderr.
- [ ] Exit codes: `0` ok, `1` on a forced DB error, `2` on an unknown flag
      and `2` when `db_container`/`db_name` is unconfigured.
- [ ] Runs from outside the project with `-C <dir>` exactly like `update`.
- [ ] `docker.ModuleStates` unit-tested (parse of `-At` output incl. the
      NULL-flag column, installed-only vs all, empty result); `RunModstate`
      flag parsing tested (unknown flag, `--json`+`--all` combo).
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` all pass.
- [ ] `CHANGELOG.md` `[Unreleased]` gets an `Added` entry for `modstate`.
- [ ] `registry_test`/`commandhl_test` cross-checks (Registry ↔ help ↔
      flags) stay green with the new command.
