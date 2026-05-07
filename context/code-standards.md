# Code Standards

## General

- Keep packages small and single-purpose — one concern per file
- Fix root causes; do not layer workarounds or add `// TODO` hacks
- No global mutable state outside of the config loaded at startup
- Prefer explicit error returns over panics; only panic on programmer errors

## Go Conventions

- Go 1.22+ module with `go.mod` at the repo root
- Package names: lowercase, single word (`theme`, `cmd`, `repl`, `detect`)
- Error wrapping: `fmt.Errorf("context: %w", err)` — always wrap with context
- Unexported identifiers for everything that doesn't cross package boundaries
- Interface types defined at the point of use (consumer side), not the producer
- No init() functions; initialize explicitly in main or via constructor functions

## Styling Rules

- All terminal output goes through `lipgloss.Style` values in `internal/theme/Styles`
- Never call `lipgloss.NewStyle()` outside of `internal/theme/` — get a style from the `Styles` struct
- Never hardcode hex color strings outside of palette definitions in `internal/theme/`
- `Styles` is passed as a value, not a pointer; recreating it on theme switch is intentional

## Command Layer (`internal/cmd/`)

- Each command file exposes: `func Run(ctx context.Context, cfg *config.Config, args []string) (<-chan Line, error)`
- `Line` carries `{Kind string; Text string}` — kind matches the output kinds in `ui-context.md`
- Validate args before launching any subprocess — return error immediately if invalid
- All subprocess invocations use `exec.CommandContext(ctx, ...)` — never `exec.Command`
- Streaming: goroutine reads stdout+stderr line by line and sends to the channel; close channel when done
- Version-specific CLI flag differences are handled here with a switch on `cfg.Version`

## REPL Layer (`internal/repl/`)

- The REPL loop is the only consumer of cmd package functions
- It must not contain command business logic — only dispatch, stream rendering, and input handling
- History is in-memory ring buffer; not persisted between sessions (v1)
- Autocomplete suggestions come from a static command registry, updated at compile time

## Config (`internal/config/`)

- `.odev.toml` is the single source of truth for project-level settings
- Schema: `[project] version`, `stage`, `db`, `theme`, `logo`, `id`
- Write the file atomically (write to `.odev.toml.tmp`, rename) to avoid corrupt state
- Detect Odoo version in this order: `.odev.toml` → `docker-compose.yml` image tag → `Dockerfile FROM` → interactive prompt

## File Organization

```
echo/
  main.go                  — entry point, wires everything
  go.mod / go.sum
  DESIGN_TOKENS.md         — reference only, not imported
  context/                 — spec-driven-dev context files
  internal/
    theme/                 — Palette, Styles, PromptColor
    detect/                — version + stage detection
    config/                — .odev.toml read/write
    cmd/                   — docker.go, modules.go, db.go, i18n.go, shells.go, tests.go
    repl/                  — prompt loop, history, dispatch
    banner/                — ASCII logos, gradient rendering
```

## Error Handling

- Subprocess failures are sent as `Line{Kind: "err", Text: "..."}` to the stream — they do not crash the REPL
- Config file not found: use defaults and prompt the user; do not exit
- Unknown command: print a `warn` line suggesting `help`; do not exit
