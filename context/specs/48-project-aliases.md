# Unit 48: project-aliases — `-C <alias>` for local projects

## Goal

Let `-C` accept a short **alias** in place of a directory path, so
`echo -C habitta modstate` works from anywhere without typing the full
project root. Aliases are a user-level `name → absolute-path` registry in
`global.toml` (the same architecture as `connect_targets`). A new `alias`
command manages them (`alias <name>` / `alias --list` / `alias --rm <name>`
/ `alias --migrate`), `init` offers to register one at the end, and `-C`
resolution falls back to a `connect_target`'s `remote_path` when it
resolves to a local directory. Nothing about the existing `-C <dir>` path
changes — a real directory always wins.

## Design

Aliases live in `global.toml` under `[project_aliases]` as a flat
`name = "/abs/path"` table, mirroring `[connect_targets]`:

```toml
[project_aliases]
habitta = "/Users/me/work/habitta"
demo    = "/Users/me/work/demo"
```

**Resolution order** in `-C <value>` (in `main.go`, before `FindRoot`):

1. `value` is an existing directory → use as-is (current behavior, always
   wins so a real path never changes meaning).
2. `value` matches a key in `project_aliases` → use its path (error if the
   stored path no longer exists).
3. `value` matches a `connect_targets` name **and** that target's
   `remote_path` is a local directory → use `remote_path`. This is the
   free reuse of connect names for people running Echo on the server where
   the remote path is local; for a genuinely remote target (path only
   exists over SSH) this step finds nothing and falls through.
4. Otherwise → usage error (`%q is neither a directory nor a known alias`),
   exit 2.

Aliases are **local-only** (a directory on this machine). The registry
deliberately does not store remote info — that's what `connect_targets`
is for. The two registries share a namespace by convention (an alias and a
connect target can have the same name and mean the same project) but are
kept as separate maps so neither feature can break the other.

The `alias` command is one of Echo's read/write meta commands: it edits
`global.toml` and reports via Odoo-style `echo.alias` log lines, like the
rest of the REPL. It is one-shot eligible (`echo alias --list`).

### Migration (`alias --migrate`)

Explicit, never automatic. Iterates `connect_targets`; for each whose
`remote_path` `os.Stat`s as a local directory and whose name isn't already
an alias, adds `name → remote_path`. Reports how many were added and how
many skipped (already-aliased or non-local). Idempotent: re-running adds
nothing new. For a laptop with only remote targets it adds zero and says
so — which is the honest outcome.

## Implementation

### `internal/config` — registry + helpers

- `Config.ProjectAliases map[string]string` (new field).
- `globalFile.ProjectAliases map[string]string` with
  `toml:"project_aliases"`.
- `Load` and `LoadGlobal`: `cfg.ProjectAliases = g.ProjectAliases`.
- `SaveGlobal`: `ProjectAliases: cfg.ProjectAliases` in the `globalFile`
  literal (preserved like `ConnectTargets`).
- New `internal/config/project_alias.go`:
  - `validAliasName(name) error` — non-empty, no whitespace, no `/`, not
    starting with `-` (so it can't be confused with a flag or a path), not
    `.`/`..`.
  - `SetProjectAlias(name, absPath string) error` — `LoadGlobal` → validate
    → set map key → `SaveGlobal` (preserves all other global fields, like
    `SaveConnectTarget`).
  - `RemoveProjectAlias(name string) (bool, error)` — load → delete →
    save; returns whether it existed.
  - `ResolveProjectAlias(name string) (path string, source string, ok bool)`
    — load → check `project_aliases` (source `"alias"`), then
    `connect_targets` with a local `remote_path` (source `"connect"`); the
    local-dir check lives here. Returns ok=false when nothing matches.
  - `MigrateConnectAliases() (added, skipped []string, err error)`.
  - `ProjectAliasList() ([]ProjectAlias, error)` — sorted `{Name, Path}`
    slice for display.

### `main.go` — resolution

After `extractProjectDir`, when `projectDir != ""` and it isn't an
existing directory, call `config.ResolveProjectAlias(projectDir)`:

```go
if projectDir != "" && !isDir(projectDir) {
    if p, _, ok := config.ResolveProjectAlias(projectDir); ok {
        projectDir = p
    } else {
        log.Error("unknown project alias or directory", "value", projectDir,
            "hint", "pass a directory, or register it with `alias <name>`")
        os.Exit(exitUsage)
    }
}
```

`isDir` is a small local helper (`os.Stat` + `IsDir`). The resolution runs
before the `connect` projectless branch is irrelevant (connect has its own
path); it only affects the `-C` value.

### `alias` command — `internal/cmd/alias.go` + `internal/repl/alias.go`

`cmd.RunAlias(opts) (AliasResult, error)` parses `opts.Args`:

- `--list` (or no args) → `AliasResult{Action: "list", Aliases: …}`.
- `--rm <name>` → remove; result carries removed/notfound.
- `--migrate` → run `MigrateConnectAliases`; result carries added/skipped.
- bare `<name>` → `SetProjectAlias(name, opts.Root)` for the current
  project; result carries the name + path. A non-empty extra positional or
  unknown flag → usage error.

`opts.Root` is the resolved project root (alias points there). The cmd
layer does the config mutation; the repl wrapper renders `echo.alias` lines
and sets the exit code (`exitUsage` for bad args / invalid name, `exitOK`
otherwise). No TTY needed — fully headless.

Wiring (REPL): add `"alias"` to `Registry`, `dispatchNames`, the
`dispatchParsed` switch (`case "alias": sess.runAlias(ctx, args)`),
`commandFlags["alias"] = {"--list", "--rm", "--migrate"}`, and a
`helpSections` entry under Meta/Config.

### `init` — optional alias step

Add a fourth `huh.NewGroup` with an optional `huh.NewInput` ("Alias
(optional) — use with `-C <alias>`; blank to skip"). After the existing
`SaveProject`, if the trimmed alias is non-empty call
`config.SetProjectAlias(alias, opts.Root)`; on a validation error, surface
a non-fatal warning via `opts.StreamOut` (the project is already saved —
a bad alias must not roll back init).

## Dependencies

- none (stdlib `os`, `sort`; reuses `internal/config`, `huh` already in
  `init`).

## Verify when done

- [ ] `echo -C <dir>` still works unchanged; a real directory always wins
      over an alias of the same name.
- [ ] After `alias foo` in a project, `echo -C foo modstate` (or any
      command) resolves to that project from any cwd.
- [ ] `alias --list` shows all aliases; `alias --rm foo` removes one;
      both persist across processes in `global.toml`.
- [ ] `-C <connect-target-name>` resolves when that target's `remote_path`
      is a local directory, and errors cleanly (exit 2) when it's remote
      or unknown.
- [ ] `alias --migrate` backfills connect targets with local paths only,
      is idempotent, and reports added/skipped counts.
- [ ] `init` offers an optional alias and registering it makes `-C <alias>`
      work; leaving it blank skips with no error.
- [ ] Exit codes: `0` ok, `2` for invalid alias name / unknown `-C` value.
- [ ] `validAliasName`, `ResolveProjectAlias` (alias hit, connect-local
      hit, remote miss, unknown), and `MigrateConnectAliases`
      (idempotency, non-local skip) are unit-tested.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass;
      `registry_test`/`commandhl_test` cross-checks stay green.
- [ ] `CHANGELOG.md` `[Unreleased]` gets an `Added` entry.
