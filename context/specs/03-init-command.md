# Unit 03: `init` Command

## Goal

Implement the `init` command — an interactive setup flow using Charm `huh`
that collects the five per-project values (Odoo version, Odoo container name,
DB container name, DB name, stage), auto-detects defaults from
`docker-compose.yml`, and persists the result via `config.SaveProject`.
Running `init` again re-opens the form pre-filled with existing values so
the user can update any field.

## Design

The flow is a single `huh.Form` with one group, shown inline in the terminal
(not full-screen). Each field shows its current/detected value as the default.

```
Echo — Project Setup
────────────────────────────────────────

  Odoo version      › [17 / 18 / 19]      (select)
  Odoo container    › odoo                 (text input)
  DB container      › db                  (text input)
  DB name           › odoo                (text input)
  Stage             › [dev / staging / prod] (select)

  Press Enter to confirm · Esc to cancel
```

Colors follow the active theme (`s.Label` for field titles, `s.Info` for
selected values, `s.Dim` for hints).

After saving:
- Print `s.Ok.Render("✔ Project configured")` 
- Print each saved value as `s.Dim` lines for confirmation
- The REPL prompt immediately reflects the new stage color

If the user cancels (Esc), print `s.Warn.Render("init cancelled — no changes saved")`.

## Implementation

### `internal/detect/detect.go`

```go
package detect

// FromCompose reads docker-compose.yml in dir and returns best-guess values.
// Returns zero-value DetectResult if the file is absent or unparseable.
type DetectResult struct {
    OdooVersion   string // from image tag e.g. "odoo:18" → "18"
    OdooContainer string // service name whose image starts with "odoo"
    DBContainer   string // service name whose image starts with "postgres"
    DBName        string // from POSTGRES_DB env var if present
}

func FromCompose(dir string) DetectResult { ... }
```

Detection rules for `docker-compose.yml`:
1. Find a service with `image: odoo:NN` or `image: odoo:NN.0` → `OdooVersion = "NN"`, `OdooContainer = <service name>`
2. Find a service with `image: postgres:*` → `DBContainer = <service name>`
3. Find `POSTGRES_DB` in environment of the db service → `DBName`
4. Fallback: leave field empty (the form will show the config default instead)

### `internal/cmd/init.go`

```go
package cmd

import (
    "github.com/charmbracelet/huh"
    "github.com/pascualchavez/echo/internal/config"
    "github.com/pascualchavez/echo/internal/detect"
)

// RunInit opens the huh form, saves config on confirm, returns updated Config.
func RunInit(cfg *config.Config, projectDir string) (*config.Config, error) {
    detected := detect.FromCompose(projectDir)

    // Merge: detected > existing config > defaults
    version   := firstNonEmpty(cfg.OdooVersion,   detected.OdooVersion,   config.Defaults.OdooVersion)
    odooSvc   := firstNonEmpty(cfg.OdooContainer,  detected.OdooContainer, config.Defaults.OdooContainer)
    dbSvc     := firstNonEmpty(cfg.DBContainer,    detected.DBContainer,   config.Defaults.DBContainer)
    dbName    := firstNonEmpty(cfg.DBName,         detected.DBName,        config.Defaults.DBName)
    stage     := firstNonEmpty(cfg.Stage,                                  config.Defaults.Stage)

    form := huh.NewForm(
        huh.NewGroup(
            huh.NewSelect[string]().
                Title("Odoo version").
                Options(huh.NewOption("17", "17"), huh.NewOption("18", "18"), huh.NewOption("19", "19")).
                Value(&version),

            huh.NewInput().
                Title("Odoo container").
                Value(&odooSvc),

            huh.NewInput().
                Title("DB container").
                Value(&dbSvc),

            huh.NewInput().
                Title("DB name").
                Value(&dbName),

            huh.NewSelect[string]().
                Title("Stage").
                Options(
                    huh.NewOption("dev", "dev"),
                    huh.NewOption("staging", "staging"),
                    huh.NewOption("prod", "prod"),
                ).
                Value(&stage),
        ),
    )

    if err := form.Run(); err != nil {
        return nil, err // user cancelled or error
    }

    cfg.OdooVersion   = version
    cfg.OdooContainer = odooSvc
    cfg.DBContainer   = dbSvc
    cfg.DBName        = dbName
    cfg.Stage         = stage

    if err := config.SaveProject(cfg); err != nil {
        return nil, err
    }
    return cfg, nil
}
```

### Wire into `repl.go` dispatch

Add `init` as a handled command in `session.dispatch`:

```go
case "init":
    newCfg, err := cmd.RunInit(sess.cfg, sess.projectDir)
    if err != nil {
        // huh returns a sentinel when user cancels — detect and print warn
        sess.print(Line{Kind: "warn", Text: "init cancelled — no changes saved"})
        return
    }
    sess.cfg = newCfg
    // Update styles to reflect new stage
    sess.styles = theme.New(sess.palette, theme.Stage(newCfg.Stage))
    sess.print(Line{Kind: "ok", Text: "✔ Project configured"})
    sess.print(Line{Kind: "dim", Text: fmt.Sprintf("  version       %s", newCfg.OdooVersion)})
    sess.print(Line{Kind: "dim", Text: fmt.Sprintf("  odoo          %s", newCfg.OdooContainer)})
    sess.print(Line{Kind: "dim", Text: fmt.Sprintf("  db container  %s", newCfg.DBContainer)})
    sess.print(Line{Kind: "dim", Text: fmt.Sprintf("  db name       %s", newCfg.DBName)})
    sess.print(Line{Kind: "dim", Text: fmt.Sprintf("  stage         %s", newCfg.Stage)})
```

The `session` struct gains `cfg *config.Config` and `projectDir string` fields.
The prompt re-renders with the updated stage color on the next input cycle (no
extra work needed — `renderPrompt` reads from `sess.styles` which was just updated).

### `huh` theming

Apply the active lipgloss theme to the huh form using `huh.WithTheme`:

```go
form.WithTheme(huh.ThemeBase())
```

For unit 03, use `ThemeBase()`. A proper lipgloss-integrated theme can be
added later in unit 12.

## Dependencies

- `github.com/charmbracelet/huh` — interactive form (add to go.mod)
- `gopkg.in/yaml.v3` — parse `docker-compose.yml` (add to go.mod)

## Verify when done

- [ ] `go build ./...` passes
- [ ] Typing `init` in the REPL opens the huh form with pre-filled defaults
- [ ] Auto-detected values from a real `docker-compose.yml` appear as form defaults
- [ ] Confirming the form writes `~/.config/echo/projects/<hash>.toml`
- [ ] Running `init` again re-opens the form with the previously saved values
- [ ] Pressing Esc cancels without writing any file
- [ ] After confirming a new stage, the prompt color changes on the next line
- [ ] No files are created inside the project directory
