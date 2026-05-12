# Unit 05: Module Commands

## Goal

Implement the REPL commands for managing Odoo modules: `install`, `update`,
`uninstall`, and `modules` (list local modules). Each one runs the Odoo CLI
inside the configured Odoo container via `compose exec` and streams output
back to the REPL, reusing the streaming pattern from Unit 04.

The flag set is stable across Odoo 17 / 18 / 19, so no version-specific
branching is needed; `cfg.OdooVersion` stays as informational metadata only.

## Design

### Subcommands

| Command                    | Effective call                                                                                  |
|----------------------------|-------------------------------------------------------------------------------------------------|
| `install mod1 mod2`        | `odoo -d <db> -i mod1,mod2 --stop-after-init --without-demo=all`                                |
| `install --with-demo mod1` | `odoo -d <db> -i mod1 --stop-after-init` (demo data included)                                   |
| `update mod1`              | `odoo -d <db> -u mod1 --stop-after-init`                                                        |
| `update --all`             | `odoo -d <db> -u all --stop-after-init`                                                         |
| `uninstall mod1`           | `odoo -d <db> --uninstall mod1 --stop-after-init`                                               |
| `modules`                  | Walk the project root one level deep for directories containing `__manifest__.py`; print as list |

All Odoo invocations run inside the container:

```
<compose> exec -T <odoo_container> odoo -d <db> ... --stop-after-init
```

DB credentials are not passed explicitly — the Odoo container reads
`HOST`/`USER`/`PASSWORD`/`PORT` from its environment (provided by
docker-compose via `.env`). If a project misconfigures this, a follow-up
unit can layer overrides; for v1 we keep the call clean.

### Defaults and flags

| Command     | Default flag                | Override flag       |
|-------------|-----------------------------|---------------------|
| `install`   | `--without-demo=all`        | `--with-demo`       |
| `update`    | _(none)_                    | `--all` (=`-u all`) |
| `uninstall` | _(none)_                    |                     |
| `modules`   | one-level scan from `root`  |                     |

Every install/update/uninstall always appends `--stop-after-init` — the
project's Odoo server (the long-running container) is not affected; we run
a one-shot inside the same container image to perform the action.

### Behaviour

- `install`/`update`/`uninstall` without module names → print a usage line
  via `Line{Kind: "warn"}` (no Odoo call). Exception: `update --all`.
- Streamed output uses the existing `Line{Kind: "out"}` convention; stderr
  collapses into the same stream.
- Non-zero exit from Odoo surfaces a single `Line{Kind: "err"}` with the
  subprocess's last message; the rest of the streamed lines still appear.
- After the call, the REPL prompt returns; nothing else changes in
  session state.

### Module discovery for `modules`

For Unit 05 we keep it simple:

1. Iterate the immediate children of the project root.
2. For each child that is a directory, check for `__manifest__.py`.
3. Print the directory name (one per line, `Line{Kind: "out"}`).
4. If `./addons/` exists, also iterate its children.
5. If `./custom/` exists, also iterate its children.

No deep walks — this is the local custom-module list, not a full
addons-path scan. Filterable list integration comes in Unit 10.

## Implementation

### `internal/odoo/cmd.go` (new)

```go
package odoo

import "strings"

// Cmd is the argv (excluding the leading `compose exec -T <container>`)
// that runs Odoo inside the container.
type Cmd []string

func Install(db string, modules []string, withDemo bool) Cmd {
    args := Cmd{"odoo", "-d", db, "-i", strings.Join(modules, ","), "--stop-after-init"}
    if !withDemo {
        args = append(args, "--without-demo=all")
    }
    return args
}

func Update(db string, modules []string) Cmd {
    return Cmd{"odoo", "-d", db, "-u", strings.Join(modules, ","), "--stop-after-init"}
}

func UpdateAll(db string) Cmd {
    return Cmd{"odoo", "-d", db, "-u", "all", "--stop-after-init"}
}

func Uninstall(db string, modules []string) Cmd {
    return Cmd{"odoo", "-d", db, "--uninstall", strings.Join(modules, ","), "--stop-after-init"}
}
```

### `internal/docker/exec.go` (new)

A small helper that pipes `compose exec -T <container> <args...>` through
the same streaming pattern used in `runStreamed`. Reuses
`runStreamed` by prefixing args.

```go
// Exec runs `<compose> exec -T <container> <argv...>` in dir, streaming
// combined stdout/stderr to onLine.
func Exec(ctx context.Context, composeCmd, dir, container string, argv []string, onLine func(string)) error {
    args := append([]string{"exec", "-T", container}, argv...)
    return runStreamed(ctx, composeCmd, dir, onLine, args...)
}
```

### `internal/cmd/modules.go` (new)

```go
package cmd

import (
    "context"
    "errors"
    "os"
    "path/filepath"

    "github.com/pascualchavez/echo/internal/docker"
    "github.com/pascualchavez/echo/internal/odoo"
)

type ModulesOpts struct {
    Cfg       *config.Config
    Root      string
    Args      []string
    StreamOut func(string)
}

func RunInstall(ctx context.Context, opts ModulesOpts) error
func RunUpdate(ctx context.Context, opts ModulesOpts) error
func RunUninstall(ctx context.Context, opts ModulesOpts) error
func RunModules(ctx context.Context, opts ModulesOpts) error
```

Argument parsing:

- `RunInstall` recognises `--with-demo`; everything else is a module name.
- `RunUpdate` recognises `--all`; everything else is a module name.
- `RunUninstall` only accepts module names.

`RunModules` scans `Root`, `Root/addons`, `Root/custom` one level deep for
directories with `__manifest__.py`.

### `internal/repl/repl.go` dispatch

Add cases:

```go
case "install", "update", "uninstall", "modules":
    sess.runModules(ctx, cmd, args)
```

Helper `runModules` mirrors `runDocker`: build `ModulesOpts`, dispatch on
the command name to the right `cmd.Run*` function, surface errors.

### Help update

Add a "Modules" section to `runHelp`:

```
Modules
  install <mod...>     Install modules in the current DB
    --with-demo          Include demo data
  update <mod...>      Update modules
    --all                Update every installed module
  uninstall <mod...>   Uninstall modules
  modules              List local modules (addons, custom, root)
```

## Dependencies

None beyond what is already in `go.mod`.

## Verify when done

- [ ] `go build ./...` passes.
- [ ] `modules` lists every directory under `./`, `./addons/`, and
      `./custom/` that contains `__manifest__.py`.
- [ ] `install foo bar` runs `odoo -d <db> -i foo,bar --stop-after-init
      --without-demo=all` inside the configured container, streaming output.
- [ ] `install --with-demo foo` omits `--without-demo=all`.
- [ ] `update sale` runs with `-u sale --stop-after-init`.
- [ ] `update --all` runs with `-u all --stop-after-init`.
- [ ] `uninstall foo` runs with `--uninstall foo --stop-after-init`.
- [ ] Non-zero exit produces a single error line; streamed output appears
      before it.
- [ ] Calling any of these without an Odoo container running produces a
      clear error (compose exec fails) instead of hanging.
- [ ] `help` shows the new Modules section.
