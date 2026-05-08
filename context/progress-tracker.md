# Progress Tracker

## Current Phase

**Phase 1 — Foundation**: scaffolding, theme system, header, and basic REPL prompt.

## Current Goal

Unit 04: `up`, `down`, `restart`, `ps`, `logs` — streaming subprocess output.

## In Progress

_(none — ready for Unit 04)_

## Completed

- [x] Unit 01 — scaffold + theme system (4 palettes) + two-column header + REPL prompt with `ls`
- [x] Unit 02 — `internal/config/` package: `Load`, `SaveGlobal`, `SaveProject`; `~/.config/echo/` layout; `PaletteByName`/`StageFromString` in theme; wired into `main.go` and `repl.go`
- [x] Unit 03 — `init` command (v2): form `huh` 3 steps con iconos nerd-font, project root walk-up, auto-detect compose flavor (docker compose vs docker-compose) persistido en global.toml, `compose ps`/`psql -lqt` para listar containers/DBs, parser `.env` para POSTGRES_USER/DB, charm/log para fatales

## Open Questions

1. What ASCII logo should appear in the header by default? (`echo`, `planet`, `python`, `anchor`)
2. Should history persist between sessions (file-based) or stay in-memory only for v1?
3. Exact Odoo CLI flag differences between v17, v18, v19 for `install`/`update`/`test` — need to verify.

## Architecture Decisions

_(none yet)_

## Session Notes

- 2026-05-07: Project initialized with spec-driven-dev. Context files generated from
  DESIGN_TOKENS.md and initial conversation. First deliverable: header + `ls` command.
- 2026-05-08: Unit 02 complete. Config package with TOML, atomic writes, defaults, and tests. Theme and stage now come from `~/.config/echo/` instead of being hardcoded.
- 2026-05-08: Unit 03 complete. `init` command with `huh` form, docker-compose auto-detect, and live prompt update on confirm.
- 2026-05-08: Unit 03 reescrito (v2) tras feedback. Eliminado parsing YAML; ahora todo viene de docker live (`compose ps`, `psql -lqt`). Nuevo `internal/project/` (walk-up al root), `internal/docker/` (compose+psql), `internal/env/` (.env parser). Compose flavor (`docker compose` vs `docker-compose`) auto-detectado y persistido en `global.toml`. Iconos nerd-font en form, banner final y prompt. `charmbracelet/log` para fatales.
