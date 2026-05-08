# Unit 03: `init` Command

## Goal

Implement the `init` command — an interactive multi-step setup flow using
Charm `huh` that configures a project in three stages: **Odoo** (version +
stage), **Containers** (Odoo and DB containers, listed via `<compose> ps`),
and **Database** (DB name, listed via `psql -lqt`). All container/db data
comes from live docker calls — no YAML parsing.

This unit also introduces two pieces of foundation that the rest of the
binary depends on:

1. **Project root detection** — walking up from cwd to find a directory
   containing `docker-compose.yml`/`.yaml`. Required for every docker
   command in later units.
2. **Compose command auto-detection** — `docker compose` vs
   `docker-compose`, decided once and persisted in `global.toml`.

## Design

### Startup sequence

`main.go` runs in this order:

1. **Locate project root** — `project.FindRoot(cwd)` walks up from cwd
   searching for `docker-compose.yml` or `docker-compose.yaml`. If not
   found, log a fatal error via `charmbracelet/log` and `os.Exit(1)`.
   Example error: `no docker-compose.yml found in <cwd> or any parent`.
2. **Load global config** — `config.Load(root)`.
3. **Detect compose flavor** — if `cfg.ComposeCmd == ""`:
   - Try `docker compose version` → set `compose_cmd = "docker compose"`
   - Else try `docker-compose --version` → set `compose_cmd = "docker-compose"`
   - Else fatal log: `neither 'docker compose' nor 'docker-compose' found in PATH`
   - Persist via `config.SaveGlobal(cfg)`.
4. **Render header + start REPL** — pass `cfg` and `root` (not cwd) into
   `repl.Start`.

The REPL header now shows `root` (e.g. `~/projects/tek_odoo`) regardless of
the directory the user invoked the binary from.

### Init form layout

A single `huh.Form` with three groups; the user navigates between them with
Tab / Shift+Tab. Group titles use Nerd Font icons.

```
  Odoo
  ────────────────────────
  Odoo version    › [17 / 18 / 19]
  Stage           › [dev / staging / prod]


  Containers
  ────────────────────────
  Odoo container  › [<list from compose ps>]
  DB container    › [<list from compose ps>]


  Database
  ────────────────────────
  DB name         › [<list from psql -l>]
```

### Containers step

When the Containers group is reached, the form shows a select for each
container field. Options come from `docker.ListContainers(cfg.ComposeCmd, root)`,
which runs `<compose> ps --services --status=running` and returns the
service names.

If the list is empty (no containers running):

- The form pauses and shows a confirm dialog (`huh.NewConfirm`):
  `"No running containers found. Start them now? (compose up -d)"`
- If yes → run `docker.Up(cfg.ComposeCmd, root)`, stream output via the
  REPL line channel, then re-list and continue.
- If no → `form.Run()` returns; init prints
  `s.Warn.Render("init cancelled — start containers first")` and exits
  without saving.

### Database step

After containers are confirmed, the DB step shows a select listing all
databases on the chosen DB container via:

```
<compose> exec -T <db_container> psql -U postgres -lqt
```

The output is parsed: each non-empty line's first column is a database
name. System databases (`postgres`, `template0`, `template1`) are filtered
out.

If the query fails (psql error, container not actually running postgres),
the field falls back to a text input pre-filled with `cfg.DBName` or the
default (`"odoo"`).

### Confirmation banner (Nerd Font)

After save, print:

```
  ✔ Project configured

   version    18
   stage      dev
   odoo       odoo
   db         db
   db name    mydb
```

Each icon line uses `s.Dim`. The check icon `` uses `s.Ok`.

### REPL prompt icon

The prompt gains a leading nerd-font icon (representing the project), e.g.:

```
 echo-01 [dev/18.0]:~$
```

The icon comes from a small mapping based on `cfg.Logo`:
`echo` → ``, `planet` → ``, `python` → ``, `anchor` → ``.

## Implementation

### `internal/project/root.go`

```go
package project

import (
    "errors"
    "os"
    "path/filepath"
)

var ErrNoRoot = errors.New("no docker-compose.yml found")

func FindRoot(cwd string) (string, error) {
    dir := cwd
    for {
        for _, name := range []string{"docker-compose.yml", "docker-compose.yaml"} {
            if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
                return dir, nil
            }
        }
        parent := filepath.Dir(dir)
        if parent == dir { // reached filesystem root
            return "", ErrNoRoot
        }
        dir = parent
    }
}
```

### `internal/docker/compose.go`

```go
package docker

import (
    "context"
    "errors"
    "os/exec"
    "strings"
)

var ErrComposeNotFound = errors.New("neither 'docker compose' nor 'docker-compose' found")

// DetectCompose returns "docker compose" or "docker-compose", whichever works.
func DetectCompose(ctx context.Context) (string, error) {
    if err := exec.CommandContext(ctx, "docker", "compose", "version").Run(); err == nil {
        return "docker compose", nil
    }
    if err := exec.CommandContext(ctx, "docker-compose", "--version").Run(); err == nil {
        return "docker-compose", nil
    }
    return "", ErrComposeNotFound
}

// ListContainers returns the names of running services in the project.
func ListContainers(ctx context.Context, composeCmd, dir string) ([]string, error) {
    args := append(splitCompose(composeCmd), "ps", "--services", "--status=running")
    out, err := exec.CommandContext(ctx, args[0], args[1:]...).Dir(dir).Output()
    if err != nil {
        return nil, err
    }
    var services []string
    for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
        if line != "" {
            services = append(services, line)
        }
    }
    return services, nil
}

// Up runs `compose up -d` and streams output line by line.
func Up(ctx context.Context, composeCmd, dir string, out chan<- string) error { ... }

func splitCompose(cmd string) []string {
    return strings.Fields(cmd) // handles "docker compose" → ["docker", "compose"]
}
```

Note: `exec.CommandContext(...).Dir(...)` — set `cmd.Dir = dir` before
calling `.Output()` (using a small helper since `Dir` isn't a chainable
method).

### `internal/docker/postgres.go`

```go
package docker

import (
    "context"
    "os/exec"
    "strings"
)

// ListDatabases queries psql -lqt inside the given DB container.
// Filters out system databases (postgres, template0, template1).
func ListDatabases(ctx context.Context, composeCmd, dir, dbContainer string) ([]string, error) {
    args := append(splitCompose(composeCmd),
        "exec", "-T", dbContainer,
        "psql", "-U", "postgres", "-lqt",
    )
    cmd := exec.CommandContext(ctx, args[0], args[1:]...)
    cmd.Dir = dir
    out, err := cmd.Output()
    if err != nil {
        return nil, err
    }

    skip := map[string]bool{"postgres": true, "template0": true, "template1": true}
    var dbs []string
    for _, line := range strings.Split(string(out), "\n") {
        // psql -lqt format: " name | owner | encoding | ..."
        parts := strings.SplitN(line, "|", 2)
        if len(parts) == 0 {
            continue
        }
        name := strings.TrimSpace(parts[0])
        if name == "" || skip[name] {
            continue
        }
        dbs = append(dbs, name)
    }
    return dbs, nil
}
```

### `internal/config/` updates

Add `ComposeCmd` field to `Config`:

```go
type Config struct {
    Theme         string
    Logo          string
    ComposeCmd    string  // NEW: "docker compose" or "docker-compose"
    OdooVersion   string
    OdooContainer string
    DBContainer   string
    DBName        string
    Stage         string
    ProjectPath   string
    ProjectKey    string
}
```

`globalFile` struct gains `ComposeCmd string \`toml:"compose_cmd"\``. Load
and SaveGlobal include it. Defaults remain empty for `ComposeCmd` (it gets
filled by detection).

### `internal/cmd/init.go` (rewrite)

```go
package cmd

import (
    "context"
    "fmt"
    "os"

    "github.com/charmbracelet/huh"
    "github.com/pascualchavez/echo/internal/config"
    "github.com/pascualchavez/echo/internal/docker"
)

type InitOpts struct {
    Cfg       *config.Config
    Root      string
    StreamOut func(string) // for streaming compose up output to the REPL
}

func RunInit(ctx context.Context, opts InitOpts) (*config.Config, error) {
    cfg := opts.Cfg

    version := firstNonEmpty(cfg.OdooVersion, config.Defaults.OdooVersion)
    stage := firstNonEmpty(cfg.Stage, config.Defaults.Stage)

    // Step 2: list containers
    services, err := docker.ListContainers(ctx, cfg.ComposeCmd, opts.Root)
    if err != nil || len(services) == 0 {
        // Ask if user wants to bring containers up
        var bringUp bool
        confirm := huh.NewForm(huh.NewGroup(
            huh.NewConfirm().
                Title("No running containers").
                Description("Start them now with `compose up -d`?").
                Affirmative("Yes").
                Negative("No").
                Value(&bringUp),
        )).WithTheme(huh.ThemeBase()).WithInput(os.Stdin).WithOutput(os.Stdout)
        if err := confirm.Run(); err != nil {
            return nil, err
        }
        if !bringUp {
            return nil, fmt.Errorf("init cancelled — containers not running")
        }
        if err := docker.Up(ctx, cfg.ComposeCmd, opts.Root, opts.StreamOut); err != nil {
            return nil, err
        }
        services, err = docker.ListContainers(ctx, cfg.ComposeCmd, opts.Root)
        if err != nil || len(services) == 0 {
            return nil, fmt.Errorf("containers still not running after up")
        }
    }

    odooSvc := firstNonEmpty(cfg.OdooContainer, defaultOdoo(services))
    dbSvc := firstNonEmpty(cfg.DBContainer, defaultDB(services))

    // Build full multi-step form. Step 3 dynamically refreshes DB list
    // when the user moves to it, but for v1 we list once with the
    // current odooSvc/dbSvc selection.
    dbs, _ := docker.ListDatabases(ctx, cfg.ComposeCmd, opts.Root, dbSvc)
    dbName := firstNonEmpty(cfg.DBName, firstStr(dbs), config.Defaults.DBName)

    form := huh.NewForm(
        huh.NewGroup(
            huh.NewSelect[string]().Title(" Odoo version").
                Options(opt("17"), opt("18"), opt("19")).Value(&version),
            huh.NewSelect[string]().Title("Stage").
                Options(opt("dev"), opt("staging"), opt("prod")).Value(&stage),
        ).Title(" Odoo"),

        huh.NewGroup(
            huh.NewSelect[string]().Title("Odoo container").
                Options(toOptions(services)...).Value(&odooSvc),
            huh.NewSelect[string]().Title("DB container").
                Options(toOptions(services)...).Value(&dbSvc),
        ).Title(" Containers"),

        huh.NewGroup(
            dbField(dbs, &dbName),
        ).Title(" Database"),
    ).WithTheme(huh.ThemeBase()).WithInput(os.Stdin).WithOutput(os.Stdout)

    if err := form.Run(); err != nil {
        return nil, err
    }

    cfg.OdooVersion = version
    cfg.Stage = stage
    cfg.OdooContainer = odooSvc
    cfg.DBContainer = dbSvc
    cfg.DBName = dbName
    if err := config.SaveProject(cfg); err != nil {
        return nil, err
    }
    return cfg, nil
}

// dbField returns a Select if dbs is non-empty, else an Input fallback.
func dbField(dbs []string, val *string) huh.Field { ... }
```

Helpers (`opt`, `toOptions`, `defaultOdoo`, `defaultDB`, `firstNonEmpty`,
`firstStr`) live alongside `RunInit` in the same file.

`defaultOdoo` picks the first service whose name contains `"odoo"`;
`defaultDB` picks the first service whose name contains `"db"` or
`"postgres"`. If none match, fall back to the first item in the list.

### `internal/repl/` integration

The session struct gains nothing new — it already has `cfg` and
`projectDir` from Unit 03 v1. The dispatch handler updates to pass an
`InitOpts` and a streaming callback that sends lines through `sess.print`.

The prompt rendering gains a leading icon. Add `LogoIcon(name string) string`
helper to `internal/banner/` (since icons are presentational and the
banner package already deals with logo art):

```go
// LogoIcon returns the nerd-font glyph for a logo name.
func LogoIcon(name string) string {
    switch name {
    case "planet": return ""
    case "python": return ""
    case "anchor": return ""
    default:       return ""  // echo
    }
}
```

`renderPrompt` prepends `s.Accent.Render(banner.LogoIcon(sess.cfg.Logo)) + " "`.

### Confirmation banner formatting

After `RunInit` returns, `runInit` in `repl/` prints:

```go
sess.print(Line{Kind: "ok", Text: "  Project configured"})
sess.print(Line{Kind: "dim", Text: fmt.Sprintf("    version    %s", cfg.OdooVersion)})
sess.print(Line{Kind: "dim", Text: fmt.Sprintf("    stage      %s", cfg.Stage)})
sess.print(Line{Kind: "dim", Text: fmt.Sprintf("    odoo       %s", cfg.OdooContainer)})
sess.print(Line{Kind: "dim", Text: fmt.Sprintf("    db         %s", cfg.DBContainer)})
sess.print(Line{Kind: "dim", Text: fmt.Sprintf("    db name    %s", cfg.DBName)})
```

### Removed

- `internal/detect/` — deleted, no more `docker-compose.yml` parsing.

## Dependencies

- `github.com/charmbracelet/log` — fatal startup logging.
- `github.com/charmbracelet/huh` (already added in v1).
- No YAML library: removed.

## Verify when done

- [ ] `go build ./...` passes.
- [ ] Running the binary outside a project root prints a charm-log fatal
      and exits non-zero.
- [ ] Running inside `tek_odoo/addons/` (where `tek_odoo/` has the compose
      file) resolves the project root to `tek_odoo/`.
- [ ] First run on a fresh machine writes `compose_cmd` to `global.toml`.
- [ ] `init` shows three steps with nerd-font icons; user navigates with Tab.
- [ ] When containers are not running, the confirm dialog appears; saying
      "No" exits cleanly without writing config.
- [ ] DB select populates from `psql -lqt`, excluding system databases.
- [ ] Confirmation banner uses nerd-font icons and the active theme colors.
- [ ] The REPL prompt shows the logo's nerd-font glyph after init.
- [ ] No files are created inside the project repo.
