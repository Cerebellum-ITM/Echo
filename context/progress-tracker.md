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
- [x] Unit 03 — `init` command: `huh` form con auto-detect desde `docker-compose.yml`, persiste vía `SaveProject`, actualiza stage/version en el prompt al confirmar

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
