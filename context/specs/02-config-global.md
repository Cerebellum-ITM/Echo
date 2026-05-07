# Unit 02: Global Config Package

## Goal

Implement `internal/config/` — the package that reads and writes Echo's
configuration from `~/.config/echo/`. After this unit, every other package
can call `config.Load(projectPath)` to get a fully-populated `Config` struct
without knowing where the files live.

## Design

Two files on disk, never inside the user's project repo:

```
~/.config/echo/
  global.toml                      ← theme, logo (shared across all projects)
  projects/
    <sha256-of-abs-path>.toml      ← per-project: version, containers, db, stage
```

`Config` struct returned to callers:

```go
type Config struct {
    // global
    Theme string // "charm" | "hacker" | "odoo" | "tokyo"
    Logo  string // "echo" | "planet" | "python" | "anchor"

    // per-project
    OdooVersion    string // "17" | "18" | "19"
    OdooContainer  string // docker container name for Odoo
    DBContainer    string // docker container name for PostgreSQL
    DBName         string // postgres database name
    Stage          string // "dev" | "staging" | "prod"

    // derived (not stored)
    ProjectPath string // absolute path passed to Load()
    ProjectKey  string // sha256 hex of ProjectPath
}
```

## Implementation

### `internal/config/config.go`

```go
package config

import (
    "crypto/sha256"
    "fmt"
    "os"
    "path/filepath"

    "github.com/BurntSushi/toml"
)

const configDir = ".config/echo"

type Config struct {
    Theme         string
    Logo          string
    OdooVersion   string
    OdooContainer string
    DBContainer   string
    DBName        string
    Stage         string
    ProjectPath   string
    ProjectKey    string
}

// Load reads global + per-project config for the given project path.
// Missing files are silently treated as empty (defaults apply).
func Load(projectPath string) (*Config, error) { ... }

// SaveGlobal writes theme and logo to global.toml atomically.
func SaveGlobal(cfg *Config) error { ... }

// SaveProject writes per-project fields to projects/<key>.toml atomically.
func SaveProject(cfg *Config) error { ... }
```

### `internal/config/paths.go`

```go
// configRoot returns ~/.config/echo
func configRoot() (string, error) {
    home, err := os.UserHomeDir()
    if err != nil {
        return "", err
    }
    return filepath.Join(home, configDir), nil
}

// projectKey returns sha256 hex of the absolute project path
func projectKey(absPath string) string {
    sum := sha256.Sum256([]byte(absPath))
    return fmt.Sprintf("%x", sum)
}
```

### `internal/config/defaults.go`

```go
// Defaults applied when config fields are missing/empty.
var Defaults = Config{
    Theme:         "charm",
    Logo:          "echo",
    OdooVersion:   "18",
    OdooContainer: "odoo",
    DBContainer:   "db",
    DBName:        "odoo",
    Stage:         "dev",
}

// applyDefaults fills zero-value fields from Defaults.
func applyDefaults(cfg *Config) { ... }
```

### Atomic write helper

```go
// writeAtomic writes data to path via a temp file + rename.
func writeAtomic(path string, data []byte) error {
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, data, 0o600); err != nil {
        return err
    }
    return os.Rename(tmp, path)
}
```

### TOML dependency

Add `github.com/BurntSushi/toml` to `go.mod`.

`global.toml` schema:
```toml
theme = "charm"
logo  = "echo"
```

`projects/<key>.toml` schema:
```toml
odoo_version   = "18"
odoo_container = "odoo"
db_container   = "db"
db_name        = "odoo"
stage          = "dev"
```

### Wire into `main.go`

Replace the hardcoded palette/stage in `main.go` with:

```go
cwd, _ := os.Getwd()
cfg, err := config.Load(cwd)
if err != nil {
    // non-fatal: use defaults, warn the user
}

palette := theme.PaletteByName(cfg.Theme)  // new helper in theme package
stage   := theme.Stage(cfg.Stage)
styles  := theme.New(palette, stage)
```

Add `theme.PaletteByName(name string) Palette` to `internal/theme/theme.go`:
```go
func PaletteByName(name string) Palette {
    switch name {
    case "hacker": return Hacker
    case "odoo":   return Odoo
    case "tokyo":  return Tokyo
    default:       return Charm
    }
}
```

Pass `cfg` through to `repl.Start` so the banner can show the real theme name,
stage, and version.

## Dependencies

- `github.com/BurntSushi/toml` — TOML encode/decode

## Verify when done

- [ ] `go build ./...` passes
- [ ] `config.Load("/some/path")` on a machine with no `~/.config/echo/` returns a Config with all defaults filled
- [ ] Writing global.toml and reloading returns the written values
- [ ] Writing per-project toml and reloading with the same path returns the written values
- [ ] Two different project paths produce different `ProjectKey` values
- [ ] No files are created inside the project directory
- [ ] The banner header shows the theme name and stage from the loaded config (not hardcoded "charm" / "dev")
